// Contains functions to translate Kubernetes Service resources into xDS server
// config
package xds

import (
	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	routerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/router/v3"
	managerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/envoyproxy/go-control-plane/pkg/wellknown"
	"google.golang.org/protobuf/types/known/anypb"
	v1 "k8s.io/api/core/v1"

	"github.com/utilitywarehouse/semaphore-xds/log"
)

// LbPolicyLabel is the label used to specify load balancing policy for the
// generated clusters per Kubernetes Service
var LbPolicyLabel string

func makeRouteConfig(name, namespace string, port int32) *routev3.RouteConfiguration {
	routeName := makeRouteConfigName(name, namespace, port)
	clusterName := makeClusterName(name, namespace, port)
	virtualHostName := makeVirtualHostName(name, namespace, port)
	return &routev3.RouteConfiguration{
		Name: routeName,
		VirtualHosts: []*routev3.VirtualHost{
			{
				Name:    virtualHostName,
				Domains: []string{makeGlobalServiceDomain(name, namespace, port)},
				Routes: []*routev3.Route{{
					Match: &routev3.RouteMatch{
						PathSpecifier: &routev3.RouteMatch_Prefix{
							Prefix: "",
						},
					},
					Action: &routev3.Route_Route{
						Route: &routev3.RouteAction{
							ClusterSpecifier: &routev3.RouteAction_Cluster{
								Cluster: clusterName,
							},
						},
					},
				}},
			},
		},
	}

}

func makeManager(routeConfig *routev3.RouteConfiguration) (*anypb.Any, error) {
	router, _ := anypb.New(&routerv3.Router{})
	return anypb.New(&managerv3.HttpConnectionManager{
		HttpFilters: []*managerv3.HttpFilter{
			{
				Name: wellknown.Router,
				ConfigType: &managerv3.HttpFilter_TypedConfig{
					TypedConfig: router,
				},
			},
		},
		RouteSpecifier: &managerv3.HttpConnectionManager_RouteConfig{
			RouteConfig: routeConfig,
		},
	})
}

func makeListener(name, namespace string, port int32, manager *anypb.Any) *listenerv3.Listener {
	return &listenerv3.Listener{
		Name: makeListenerName(name, namespace, port),
		ApiListener: &listenerv3.ApiListener{
			ApiListener: manager,
		},
	}
}

func makeCluster(name, namespace string, port int32, policy clusterv3.Cluster_LbPolicy) *clusterv3.Cluster {
	clusterName := makeClusterName(name, namespace, port)
	return &clusterv3.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_EDS},
		LbPolicy:             policy,
		EdsClusterConfig: &clusterv3.Cluster_EdsClusterConfig{
			EdsConfig: &corev3.ConfigSource{
				ConfigSourceSpecifier: &corev3.ConfigSource_Ads{
					Ads: &corev3.AggregatedConfigSource{},
				},
			},
		},
	}
}

// extractClusterLbPolicy will parse the specified lb policy label in a
// Kubernetes Service (if found) and return a clusterv3.Cluster_LbPolicy to be
// used by the created clusters
func extractClusterLbPolicy(service *v1.Service) clusterv3.Cluster_LbPolicy {
	lbPolicyRaw, ok := service.Labels[LbPolicyLabel]
	if !ok {
		log.Logger.Info("No load balancing policy defined for service, defaulting to round robin", "service", service.Name)
		return clusterv3.Cluster_ROUND_ROBIN
	}
	lbPolicy, err := parseToClusterLbPolicy(lbPolicyRaw)
	if err != nil {
		log.Logger.Error("Cannot parse load balancing policy, defaulting to round robin", "service", service.Name, "err", err)
		return clusterv3.Cluster_ROUND_ROBIN
	}
	return lbPolicy
}

// servicesToResources will return a set of listener, routeConfiguration and
// cluster for each service port
func servicesToResources(services []*v1.Service) ([]types.Resource, []types.Resource, []types.Resource, error) {
	var cls []types.Resource
	var rds []types.Resource
	var lsnr []types.Resource
	for _, service := range services {
		for _, port := range service.Spec.Ports {
			routeConfig := makeRouteConfig(service.Name, service.Namespace, port.Port)
			rds = append(rds, routeConfig)
			manager, err := makeManager(routeConfig)
			if err != nil {
				log.Logger.Error("Cannot create listener manager", "error", err)
				continue
			}
			listener := makeListener(service.Name, service.Namespace, port.Port, manager)
			lsnr = append(lsnr, listener)
			cluster := makeCluster(service.Name, service.Namespace, port.Port, extractClusterLbPolicy(service))
			cls = append(cls, cluster)
		}
	}
	return cls, rds, lsnr, nil
}
