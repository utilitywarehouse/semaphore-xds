package main

import (
	"bytes"
	"fmt"
	"path"
	"strings"
	"text/template"
)

const (
	EnvoyMainListenPort = 18001
)

type MainListener struct {
	Port    int
	Domains []RouteDomain
}

type RouteDomain struct {
	Name   string
	Domain string
	Port   int
}
type TargetListener struct {
	Name            string
	Port            int
	RouteConfigName string
}

type Cluster struct {
	Name string
}

type XdsCluster struct {
	XdsServerAddress string
	XdsServerPort    string
}

type EnvoyConfig struct {
	NodeID       string
	ClusterID    string
	MainListener string
	Listeners    string
	Clusters     string
	XdsCluster   string
}

func makeEnvoyConfig(nodeID, envoySidecarTargets, XdsServerAddress, XdsServerPort string) (string, error) {
	main, listeners, clusters := extractConfigFromTargets(envoySidecarTargets)
	// Generate main listener config
	mainListenerTmplPath := "./templates/main-listener.tmpl"
	mainListenerTmplBase := path.Base(mainListenerTmplPath)
	tmpl, err := template.New(mainListenerTmplBase).ParseFiles(mainListenerTmplPath)
	if err != nil {
		return "", err
	}
	var renderedMainListener bytes.Buffer
	err = tmpl.Execute(&renderedMainListener, main)
	if err != nil {
		return "", err
	}
	// Generate Listeners Config
	listenersTmplPath := "./templates/target-listeners.tmpl"
	listenersTmplBase := path.Base(listenersTmplPath)
	tmpl, err = template.New(listenersTmplBase).ParseFiles(listenersTmplPath)
	if err != nil {
		return "", err
	}
	var renderedListeners bytes.Buffer
	err = tmpl.Execute(&renderedListeners, listeners)
	if err != nil {
		return "", err
	}
	// Generate Clusters Config
	clustersTmplPath := "./templates/clusters.tmpl"
	clustersTmplBase := path.Base(clustersTmplPath)
	tmpl, err = template.New(clustersTmplBase).ParseFiles(clustersTmplPath)
	if err != nil {
		return "", err
	}
	var renderedClusters bytes.Buffer
	err = tmpl.Execute(&renderedClusters, clusters)
	if err != nil {
		return "", err
	}
	// Generate XdsCluster Config
	xdsCluster := XdsCluster{
		XdsServerAddress: XdsServerAddress,
		XdsServerPort:    XdsServerPort,
	}
	XdsClusterTmplPath := "./templates/xds-cluster.tmpl"
	XdsClusterTmplBase := path.Base(XdsClusterTmplPath)
	tmpl, err = template.New(XdsClusterTmplBase).ParseFiles(XdsClusterTmplPath)
	if err != nil {
		return "", err
	}
	var renderedXdsCluster bytes.Buffer
	err = tmpl.Execute(&renderedXdsCluster, xdsCluster)
	if err != nil {
		return "", err
	}

	// Generate the Envoy config
	envoyConfig := EnvoyConfig{
		NodeID:       nodeID,
		ClusterID:    nodeID, // needed by envoy, add node id here as a dummy value here
		MainListener: renderedMainListener.String(),
		Listeners:    renderedListeners.String(),
		Clusters:     renderedClusters.String(),
		XdsCluster:   renderedXdsCluster.String(),
	}
	envoyConfigTmplPath := "./templates/envoy-config.tmpl"
	envoyConfigTmplBase := path.Base(envoyConfigTmplPath)
	tmpl, err = template.New(envoyConfigTmplBase).ParseFiles(envoyConfigTmplPath)
	if err != nil {
		return "", err
	}
	var renderedEnvoyConfig bytes.Buffer
	err = tmpl.Execute(&renderedEnvoyConfig, envoyConfig)
	if err != nil {
		return "", err
	}
	return renderedEnvoyConfig.String(), nil
}

// List expected upstream listeners in the form:
// <xds-address1>,<xds-address2>,<xds-address2>
// From this we should extract the xds addresses as listerner and route config
// names.
// XdsAddress is expected in the name that semaphore-xds would configure
// listeners: service.namespace:port and should be copied as is to the listener
// and routeConfig names. Clusters names should be derived from the above and
// follow the form: service.namespace.port to comply with the xds naming
// limitations and how semaphore-xds configures cluster names.
func extractConfigFromTargets(envoySidecarTargets string) (MainListener, []TargetListener, []Cluster) {
	port := EnvoyMainListenPort
	main := MainListener{
		Port: port,
	}
	domains := []RouteDomain{}
	listeners := []TargetListener{}
	for _, target := range strings.Split(envoySidecarTargets, ",") {
		port++
		lName := fmt.Sprintf("listener_%d", port)
		listeners = append(listeners, TargetListener{
			Name:            lName,
			Port:            port,
			RouteConfigName: target,
		})
		rName := fmt.Sprintf("route_%d", port)
		domains = append(domains, RouteDomain{
			Name:   rName,
			Domain: target,
			Port:   port,
		})
	}
	main.Domains = domains

	clusters := []Cluster{}
	for _, l := range listeners {
		clusterName := strings.Join(strings.Split(l.RouteConfigName, ":"), ".")
		clusters = append(clusters, Cluster{
			Name: clusterName,
		})
	}
	return main, listeners, clusters
}
