package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/utilitywarehouse/semaphore-xds/controller"
	"github.com/utilitywarehouse/semaphore-xds/kube"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/metrics"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

var (
	flagKubeConfigPath    = flag.String("kube-config", getEnv("SXDS_KUBE_CONFIG", ""), "Path of a kube config file, if not provided the app will try to get in cluster config")
	flagLogLevel          = flag.String("log-level", getEnv("SXDS_LOG_LEVEL", "info"), "Log level")
	flagNamespace         = flag.String("namespace", getEnv("SXDS_NAMESPACE", ""), "The namespace in which to watch for kubernetes resources")
	flagLabelSelector     = flag.String("label-selector", getEnv("SXDS_LABEL_SELECTOR", "xds.semaphore.uw.io/enabled=true"), "Label selector for watched kubernetes resources")
	flagServerListenPort  = flag.Uint("server-listen-port", 18000, "xDS server listen port")
	flagMetricsListenPort = flag.String("metrics-listen-port", "8080", "Listen port to serve prometheus metrics")
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

	client, err := kube.ClientFromConfig(*flagKubeConfigPath)
	if err != nil {
		log.Logger.Error(
			"cannot create kube client for local cluster",
			"err", err,
		)
		usage()
	}

	snapshotter := xds.NewSnapshotter(*flagServerListenPort)
	metrics.InitSnapMetricsCollector(snapshotter)
	serveMetrics(fmt.Sprintf(":%s", *flagMetricsListenPort))

	controller := controller.NewController(
		client,
		*flagNamespace,
		*flagLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()

	snapshotter.ListenAndServe()

	controller.Stop()
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
