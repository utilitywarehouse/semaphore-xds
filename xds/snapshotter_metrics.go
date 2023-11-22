package xds

import (
	"fmt"
	"strings"

	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/utilitywarehouse/semaphore-xds/log"
)

var (
	xdsClientOnStreamOpen = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "semaphore_xds_on_stream_open",
		Help: "Total number of client open stream requests to the xds server",
	},
		[]string{"address"},
	)
	xdsClientOnStreamClosed = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "semaphore_xds_on_stream_close",
		Help: "Total number of close stream notifications from the xds server",
	},
		[]string{"address"},
	)
	xdsClientOnStreamRequest = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "semaphore_xds_on_stream_request",
		Help: "Total number of client requests for resources discovery to the xds server",
	},
		[]string{"node_id", "address", "typeURL"},
	)
	xdsClientOnStreamResponse = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "semaphore_xds_on_stream_response",
		Help: "Total number of server responses for resources discovery to the xds server",
	},
		[]string{"node_id", "address", "typeURL"},
	)
)

func metricOnStreamOpenInc(address string) {
	xdsClientOnStreamOpen.With(prometheus.Labels{
		"address": address,
	}).Inc()
}

func metricOnStreamClosedInc(address string) {
	xdsClientOnStreamClosed.With(prometheus.Labels{
		"address": address,
	}).Inc()
}

func metricOnStreamRequestInc(nodeID, address, typeURL string) {
	xdsClientOnStreamRequest.With(prometheus.Labels{
		"node_id": nodeID,
		"address": address,
		"typeURL": typeURL,
	}).Inc()
}

func metricOnStreamResponseInc(nodeID, address, typeURL string) {
	xdsClientOnStreamResponse.With(prometheus.Labels{
		"node_id": nodeID,
		"address": address,
		"typeURL": typeURL,
	}).Inc()
}

func init() {
	prometheus.MustRegister(
		xdsClientOnStreamOpen,
		xdsClientOnStreamClosed,
		xdsClientOnStreamRequest,
		xdsClientOnStreamResponse,
	)
}

// A snapMetricscollector is a prometheus.Collector for a Snapshotter.
type snapMetricsCollector struct {
	ClusterInfo  *prometheus.Desc
	EndpointInfo *prometheus.Desc
	ListenerInfo *prometheus.Desc
	RouteInfo    *prometheus.Desc
	NodeInfo     *prometheus.Desc

	snapshotter *Snapshotter
}

func InitSnapMetricsCollector(snapshotter *Snapshotter) {
	mc := newSnapMetricsCollector(snapshotter)
	prometheus.MustRegister(mc)
}

func newSnapMetricsCollector(snapshotter *Snapshotter) prometheus.Collector {
	return &snapMetricsCollector{
		ClusterInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_cluster",
			"Metadata about an xDS cluster",
			[]string{"node_id", "type", "name", "lb_policy", "discovery_type"},
			nil,
		),
		ListenerInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_listener",
			"Metadata about an xDS listener",
			[]string{"node_id", "type", "name", "route_config"},
			nil,
		),
		EndpointInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_endpoint",
			"Metadata about an xDS cluster load assignment endpoint",
			[]string{"node_id", "type", "cluster_name", "locality_zone", "locality_subzone", "lb_address", "health_status", "priority"},
			nil,
		),
		RouteInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_route",
			"Metadata about an xDS route configuration",
			[]string{"node_id", "type", "name", "path_prefix", "domains", "virtual_host", "cluster_name"},
			nil,
		),
		NodeInfo: prometheus.NewDesc(
			"semaphore_xds_node_info",
			"Metadata about a registered node to xDS server",
			[]string{"node_id", "address"},
			nil,
		),
		snapshotter: snapshotter,
	}
}

// Describe implements prometheus.Collector.
func (c *snapMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ds := []*prometheus.Desc{
		c.ClusterInfo,
		c.EndpointInfo,
		c.ListenerInfo,
		c.RouteInfo,
		c.NodeInfo,
	}

	for _, d := range ds {
		ch <- d
	}
}

func (c *snapMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	nmap := c.snapshotter.NodesMap()
	nodes := make([]string, 0, len(nmap)+1)
	nodes = append(nodes, EmptyNodeID)
	for n, _ := range nmap {
		if n != EmptyNodeID { // be safe to avoid prometheus errors if an empty node id is in the list
			nodes = append(nodes, n)
		}
	}
	for _, node := range nodes {
		servicesSnap, err := c.snapshotter.ServicesSnapshot(node)
		if err != nil {
			log.Logger.Error("Failed to get services snapshot for metrics collection", "err", err)
			// TODO: Return an invalid metric?
			continue
		}
		c.collectListenerMetrics(ch, servicesSnap, node)
		c.collectRouteMetrics(ch, servicesSnap, node)
		c.collectClusterMetrics(ch, servicesSnap, node)

		endpointsSnap, err := c.snapshotter.EndpointsSnapshot(node)
		if err != nil {
			log.Logger.Error("Failed to get endpoints snapshot for metrics collection", "err", err)
			// TODO: Return an invalid metric?
			continue
		}
		c.collectEndpointsMetrics(ch, endpointsSnap, node)
	}

	c.collectNodeMetrics(ch)
}

func (c *snapMetricsCollector) collectListenerMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot, nodeID string) {
	listeners := snapshot.GetResources(resource.ListenerType)
	for _, l := range listeners {
		listener, err := UnmarshalResourceToListener(l)
		if err != nil {
			log.Logger.Error("Failed to unmarshal listener resource", "err", err)
			continue
		}
		manager, err := ExtractManagerFromListener(listener)
		if err != nil {
			log.Logger.Error("Failed to extract manager from listener", "err", err)
			continue
		}
		routeConfigName := manager.GetRouteConfig().Name
		ch <- prometheus.MustNewConstMetric(
			c.ListenerInfo,
			prometheus.GaugeValue,
			1,
			//"node_id", "type", "name", "route_config"
			nodeID, resource.ListenerType, listener.Name, routeConfigName,
		)
	}
}

func (c *snapMetricsCollector) collectRouteMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot, nodeID string) {
	routes := snapshot.GetResources(resource.RouteType)
	for _, r := range routes {
		routeConfig, err := UnmarshalResourceToRouteConfiguration(r)
		if err != nil {
			log.Logger.Error("Failed to unmarshal route configuration resource", "err", err)
			continue
		}
		for _, vhost := range routeConfig.VirtualHosts {
			for _, route := range vhost.Routes {
				ch <- prometheus.MustNewConstMetric(
					c.RouteInfo,
					prometheus.GaugeValue,
					1,
					//"node_id", "type", "name", "path_prefix", "domains", "virtual_host", "cluster_name"
					nodeID, resource.RouteType, routeConfig.Name, route.GetMatch().GetPath(), strings.Join(vhost.Domains, ","), vhost.Name, route.GetRoute().GetCluster(),
				)
			}
		}
	}
}

func (c *snapMetricsCollector) collectClusterMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot, nodeID string) {
	clusters := snapshot.GetResources(resource.ClusterType)
	for _, cl := range clusters {
		cluster, err := UnmarshalResourceToCluster(cl)
		if err != nil {
			log.Logger.Error("Failed to unmarshal cluster resource", "err", err)
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.ClusterInfo,
			prometheus.GaugeValue,
			1,
			// "node_id", "type", "name", "lb_policy", "discovery_type"
			nodeID, resource.ClusterType, cluster.Name, ParseClusterLbPolicy(cluster.LbPolicy), ParseClusterDiscoveryType(cluster.GetType()),
		)
	}
}

func (c *snapMetricsCollector) collectEndpointsMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot, nodeID string) {
	endpoints := snapshot.GetResources(resource.EndpointType)
	for _, e := range endpoints {
		endpoint, err := UnmarshalResourceToEndpoint(e)
		if err != nil {
			log.Logger.Error("Failed to unmarshal endpoint ClusterLoadAssignment", "err", err)
			continue
		}
		for _, lbEndpoints := range endpoint.Endpoints {
			for _, lbEndpoint := range lbEndpoints.GetLbEndpoints() {
				healthStatus := ParseLbEndpointHealthStatus(lbEndpoint.HealthStatus)
				socketAddress := lbEndpoint.GetEndpoint().Address.GetSocketAddress()
				address := fmt.Sprintf("%s:%s", socketAddress.GetAddress(), fmt.Sprint(socketAddress.GetPortValue()))
				priority := fmt.Sprint(lbEndpoints.Priority)
				ch <- prometheus.MustNewConstMetric(
					c.EndpointInfo,
					prometheus.GaugeValue,
					1,
					// "node_id", "type", "cluster_name", "locality_zone", "locality_subzone", "lb_address", "health_status", "priority"
					nodeID, resource.EndpointType, endpoint.ClusterName, lbEndpoints.GetLocality().Zone, lbEndpoints.GetLocality().SubZone, address, healthStatus, priority,
				)
			}
		}
	}
}

func (c *snapMetricsCollector) collectNodeMetrics(ch chan<- prometheus.Metric) {
	for nodeID, address := range c.snapshotter.NodesMap() {
		ch <- prometheus.MustNewConstMetric(
			c.NodeInfo,
			prometheus.GaugeValue,
			1,
			// "node_id", "address"
			nodeID, address,
		)
	}
}
