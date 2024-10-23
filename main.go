package main

import (
	"flag"
	"fmt"
	"net/http"
	"net/http/pprof"
	"os"
	"regexp"
	"strings"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/utilitywarehouse/semaphore-xds/controller"
	"github.com/utilitywarehouse/semaphore-xds/kube"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

var (
	flagAuthorityName            = flag.String("authority-name", getEnv("SXDS_AUTHORITY_NAME", ""), "Authority name of the server used in federation setups. If set the server will duplicate all resources in the snapshot using the naming pattern: `xdstp://<authority_name>/envoy.config.*.v3.*/%s`")
	flagClustersConfigPath       = flag.String("clusters-config", getEnv("SXDS_CLUSTERS_CONFIG", ""), "Path to a clusters config json file, if not provided the app will try to use a single in cluster client")
	flagLogLevel                 = flag.String("log-level", getEnv("SXDS_LOG_LEVEL", "info"), "Log level")
	flagNamespace                = flag.String("namespace", getEnv("SXDS_NAMESPACE", ""), "The namespace in which to watch for kubernetes resources")
	flagLabelSelector            = flag.String("label-selector", getEnv("SXDS_LABEL_SELECTOR", "xds.semaphore.uw.systems/enabled=true"), "Label selector for watched kubernetes resources")
	flagLbPolicyLabel            = flag.String("lb-policy-selector", getEnv("SXDS_LB_POLICY_SELECTOR", "xds.semaphore.uw.systems/lb-policy"), "Label to allow user to configure the lb policy for a Service clusters")
	flagLocalhostEndpoints       = flag.Bool("localhost-endpoints", false, "If enabled the server will create configuration with dummy endpoints on 127.0.0.1:18001 for all requested listeners and clusters")
	flagServerListenPort         = flag.Uint("server-listen-port", 18000, "xDS server listen port")
	flagMaxRequestsPerSecond     = flag.Float64("max-requests-per-second", 500.0, "maximum allowed requests to the server per second")
	flagMaxPeerRequestsPerSecond = flag.Float64("max-peer-requests-per-second", 50.0, "maximum allowed requests from a peer per second")
	flagMetricsListenPort        = flag.String("metrics-listen-port", "8080", "Listen port to serve prometheus metrics")
	flagPprofListenAddress       = flag.String("pprof-listen-address", "127.0.0.1:6060", "Listen address to expose pprof endpoints")

	bearerRe = regexp.MustCompile(`[A-Z|a-z0-9\-\._~\+\/]+=*`)
)

func usage() {
	flag.Usage()
	os.Exit(1)
}

func getEnv(key, defaultValue string) string {
	value := os.Getenv(key)
	if len(value) == 0 {
		return defaultValue
	}
	return value
}

func main() {
	flag.Parse()
	log.InitLogger("semaphore-xds", *flagLogLevel)

	controller.LbPolicyLabel = *flagLbPolicyLabel

	localClient, remoteClients := createClientsFromConfig(*flagClustersConfigPath)
	snapshotter := xds.NewSnapshotter(*flagAuthorityName, *flagServerListenPort, *flagMaxRequestsPerSecond, *flagMaxPeerRequestsPerSecond, *flagLocalhostEndpoints)
	xds.InitSnapMetricsCollector(snapshotter)
	if !*flagLocalhostEndpoints {
		go serveMetrics(fmt.Sprintf(":%s", *flagMetricsListenPort))
	}
	go servePprof(*flagPprofListenAddress)

	if !*flagLocalhostEndpoints {
		controller := controller.NewController(
			localClient,
			remoteClients,
			*flagNamespace,
			*flagLabelSelector,
			snapshotter,
			0,
		)
		controller.Run()
		defer controller.Stop()
	}

	snapshotter.ListenAndServe()
}

func serveMetrics(address string) {
	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	server := http.Server{
		Addr:    address,
		Handler: mux,
	}
	log.Logger.Error(
		"Listen and Serve",
		"err", server.ListenAndServe(),
	)
}

func servePprof(address string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/{action}", pprof.Index)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	server := http.Server{
		Addr:    address,
		Handler: mux,
	}
	log.Logger.Error(
		"Listen and Serve",
		"err", server.ListenAndServe(),
	)
}

func createClientsFromConfig(configPath string) (kube.Client, []kube.Client) {
	var localClient kube.Client
	var remoteClients []kube.Client
	var err error

	if configPath == "" {
		log.Logger.Info("No clusters config provied, will use single in cluster client")
		localClient, err = kube.NewClientFromConfig("")
		if err != nil {
			log.Logger.Error("cannot create kube client for local cluster", "err", err)
			usage()
		}
	} else {
		fileContent, err := os.ReadFile(configPath)
		if err != nil {
			log.Logger.Error("Cannot read clusters config file", "err", err)
			os.Exit(1)
		}
		config, err := parseConfig(fileContent)
		if err != nil {
			log.Logger.Error("Cannot parse clusters config", "err", err)
			os.Exit(1)
		}
		localClient, err = kube.NewClientFromConfig(config.Local.KubeConfigPath)
		if err != nil {
			log.Logger.Error("cannot create kube client for local cluster", "err", err)
			usage()
		}
		for _, remote := range config.Remotes {
			var client kube.Client
			if remote.KubeConfigPath != "" {
				client, err = kube.NewClientFromConfig(remote.KubeConfigPath)
				if err != nil {
					log.Logger.Error("cannot create kube client", "kubeconfig", remote.KubeConfigPath)
					os.Exit(1)
				}
			} else {
				data, err := os.ReadFile(remote.SATokenPath)
				if err != nil {
					log.Logger.Error("Cannot read SA token file", "path", remote.SATokenPath, "error", err)
					os.Exit(1)
				}
				saToken := string(data)
				if saToken != "" {
					saToken = strings.TrimSpace(saToken)
					if !bearerRe.Match([]byte(saToken)) {
						log.Logger.Error("The provided token does not match regex", "expr", bearerRe.String())
						os.Exit(1)
					}
				}
				client, err = kube.NewClient(saToken, remote.APIURL, remote.CAURL)
				if err != nil {
					log.Logger.Error("cannot create kube client for remote api", "api", remote.APIURL)
					os.Exit(1)
				}
			}
			remoteClients = append(remoteClients, client)
		}
	}
	return localClient, remoteClients
}
