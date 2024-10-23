package main

import (
	"bytes"
	"path"
	"strings"
	"text/template"
)

type Listener struct {
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
	NodeID     string
	ClusterID  string
	Listeners  string
	Clusters   string
	XdsCluster string
}

func makeEnvoyConfig(nodeID, envoySidecarTargets, XdsServerAddress, XdsServerPort string) (string, error) {
	listeners, clusters := extractConfigFromTargets(envoySidecarTargets)
	// Generate Listeners Config
	listenersTmplPath := "./templates/listeners.tmpl"
	listenersTmplBase := path.Base(listenersTmplPath)
	tmpl, err := template.New(listenersTmplBase).ParseFiles(listenersTmplPath)
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
		NodeID:     nodeID,
		ClusterID:  nodeID, // needed by envoy, add node id here as a dummy value here
		Listeners:  renderedListeners.String(),
		Clusters:   renderedClusters.String(),
		XdsCluster: renderedXdsCluster.String(),
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
func extractConfigFromTargets(envoySidecarTargets string) ([]Listener, []Cluster) {
	listeners := []Listener{}
	for _, target := range strings.Split(envoySidecarTargets, ",") {
		listeners = append(listeners, Listener{
			RouteConfigName: target,
		})
	}
	clusters := []Cluster{}
	for _, l := range listeners {
		clusterName := strings.Join(strings.Split(l.RouteConfigName, ":"), ".")
		clusters = append(clusters, Cluster{
			Name: clusterName,
		})
	}
	return listeners, clusters
}
