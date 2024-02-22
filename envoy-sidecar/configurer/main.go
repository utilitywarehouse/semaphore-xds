package main

import (
	"flag"
	"fmt"
	"os"
)

var (
	flagEnvoyNodeId         = flag.String("envoy-node-id", getEnv("ENVOY_NODE_ID", ""), "Node id to configure for envoy sidecar")
	flagEnvoySidecarTargets = flag.String("envoy-sidecar-targets", getEnv("ENVOY_SIDECAR_TARGETS", ""), "Map of xds listener addresses to local ports in the form: <xds-listener-1>=<local-port1>,<xds-listener-2>=<local-port2>")
	flagXdsServerAddress    = flag.String("xds-server-address", getEnv("XDS_SERVER_ADDRESS", ""), "The address of the xds server for envoy sidecar to fetch config")
	flagXdsServerPort       = flag.String("xds-server-port", getEnv("XDS_SERVER_PORT", ""), "The port of the xds server for envoy sidecar to fetch config")
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

	if *flagEnvoyNodeId == "" || *flagEnvoySidecarTargets == "" || *flagXdsServerAddress == "" || *flagXdsServerPort == "" {
		usage()
	}
	c, err := makeEnvoyConfig(*flagEnvoyNodeId, *flagEnvoySidecarTargets, *flagXdsServerAddress, *flagXdsServerPort)
	if err != nil {
		fmt.Print(err)
		os.Exit(1)
	}
	fmt.Print(c)
}
