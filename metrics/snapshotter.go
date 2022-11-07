package metrics

import (
	"strings"

	cache "github.com/envoyproxy/go-control-plane/pkg/cache/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

// A snapMetricscollector is a prometheus.Collector for a Snapshotter.
type snapMetricsCollector struct {
	ClusterInfo  *prometheus.Desc
	EndpointInfo *prometheus.Desc
	ListenerInfo *prometheus.Desc
	RouteInfo    *prometheus.Desc

	snapshotter *xds.Snapshotter
}

func InitSnapMetricsCollector(snapshotter *xds.Snapshotter) {
	mc := newSnapMetricsCollector(snapshotter)
	prometheus.MustRegister(mc)
}

func newSnapMetricsCollector(snapshotter *xds.Snapshotter) prometheus.Collector {
	return &snapMetricsCollector{
		ClusterInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_cluster",
			"Metadata about an xDS cluster",
			[]string{"type", "name", "lb_policy", "discovery_type"},
			nil,
		),
		ListenerInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_listener",
			"Metadata about an xDS listener",
			[]string{"type", "name", "route_config"},
			nil,
		),
		EndpointInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_endpoint",
			"Metadata about an xDS cluster load assignment endpoint",
			[]string{"type", "cluster_name", "locality_zone", "locality_subzone", "lb_address", "health_status"},
			nil,
		),
		RouteInfo: prometheus.NewDesc(
			"semaphore_xds_snapshot_route",
			"Metadata about an xDS route configuration",
			[]string{"type", "name", "path_prefix", "domains", "virtual_host", "cluster_name"},
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
	}

	for _, d := range ds {
		ch <- d
	}
}

func (c *snapMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	servicesSnap, err := c.snapshotter.ServicesSnapshot(xds.EmptyNodeID)
	if err != nil {
		log.Logger.Error("Failed to get services snapshot for metrics collection", "err", err)
		// TODO: Return an invalid metric?
		return
	}
	c.collectListenerMetrics(ch, servicesSnap)
	c.collectRouteMetrics(ch, servicesSnap)
	c.collectClusterMetrics(ch, servicesSnap)

	endpointsSnap, err := c.snapshotter.EndpointsSnapshot(xds.EmptyNodeID)
	if err != nil {
		log.Logger.Error("Failed to get services snapshot for metrics collection", "err", err)
		// TODO: Return an invalid metric?
		return
	}
	c.collectEndpointsMetrics(ch, endpointsSnap)
}

func (c *snapMetricsCollector) collectListenerMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot) {
	listeners := snapshot.GetResources(resource.ListenerType)
	for _, l := range listeners {
		listener, err := xds.UnmarshalResourceToListener(l)
		if err != nil {
			log.Logger.Error("Failed to unmarshal listener resource", "err", err)
			continue
		}
		manager, err := xds.ExtractManagerFromListener(listener)
		if err != nil {
			log.Logger.Error("Failed to extract manager from listeber", "err", err)
			continue
		}
		routeConfigName := manager.GetRouteConfig().Name
		ch <- prometheus.MustNewConstMetric(
			c.ListenerInfo,
			prometheus.GaugeValue,
			1,
			resource.ListenerType, listener.Name, routeConfigName,
		)
	}
}

func (c *snapMetricsCollector) collectRouteMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot) {
	routes := snapshot.GetResources(resource.RouteType)
	for _, r := range routes {
		routeConfig, err := xds.UnmarshalResourceToRouteConfiguration(r)
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
					//"type", "name", "path_prefix", "domains", "virtual_host", "cluster_name"
					resource.RouteType, routeConfig.Name, route.GetMatch().GetPath(), strings.Join(vhost.Domains, ","), vhost.Name, route.GetRoute().GetCluster(),
				)
			}
		}
	}
}

func (c *snapMetricsCollector) collectClusterMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot) {
	clusters := snapshot.GetResources(resource.ClusterType)
	for _, cl := range clusters {
		cluster, err := xds.UnmarshalResourceToCluster(cl)
		if err != nil {
			log.Logger.Error("Failed to unmarshal cluster resource", "err", err)
			continue
		}
		ch <- prometheus.MustNewConstMetric(
			c.ClusterInfo,
			prometheus.GaugeValue,
			1,
			// "type", "name", "lb_policy", "discovery_type"
			resource.ClusterType, cluster.Name, xds.ParseClusterLbPolicy(cluster.LbPolicy), xds.ParseClusterDiscoveryType(cluster.GetType()),
		)
	}
}

func (c *snapMetricsCollector) collectEndpointsMetrics(ch chan<- prometheus.Metric, snapshot cache.ResourceSnapshot) {
	endpoints := snapshot.GetResources(resource.EndpointType)
	for _, e := range endpoints {
		endpoint, err := xds.UnmarshalResourceToEndpoint(e)
		if err != nil {
			log.Logger.Error("Failed to unmarshal endpoint ClusterLoadAssignment", "err", err)
			continue
		}
		for _, lbEndpoints := range endpoint.Endpoints {
			for _, lbEndpoint := range lbEndpoints.GetLbEndpoints() {
				healthStatus := xds.ParseLbEndpointHealthStatus(lbEndpoint.HealthStatus)
				ch <- prometheus.MustNewConstMetric(
					c.ClusterInfo,
					prometheus.GaugeValue,
					1,
					// "type", "cluster_name", "locality_zone", "locality_subzone", "lb_address", "health_status"
					resource.EndpointType, endpoint.ClusterName, lbEndpoints.GetLocality().Zone, lbEndpoints.GetLocality().SubZone, lbEndpoint.GetEndpoint().Address.String(), healthStatus,
				)
			}
		}
	}
}
