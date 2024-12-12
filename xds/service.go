// Contains functions to translate Kubernetes Service resources into xDS server
// config
package xds

import (
	"fmt"

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

// Service holds the data we need to represent a Kubernetes Service in xds
// configuration
type Service struct {
	EnableRemoteEndpoints    bool
	Policy                   clusterv3.Cluster_LbPolicy
	RingHash                 *clusterv3.Cluster_RingHashLbConfig
	RingHashPolicies         []*routev3.RouteAction_HashPolicy
	PrioritizeLocalEndpoints bool
	Service                  *v1.Service
	Retry                    *routev3.RetryPolicy
}

// XdsServiceStore is a store of xdsService objects. It's meant It to be used
// to populate a map of xdsService objects and passed to the snapshotter
// so only add and get functions should be implemented. For new snapshots we
// should create new stores
type XdsServiceStore interface {
	All() map[string]Service
	AddOrUpdate(service *v1.Service, xdsService Service)
	Get(service, namespace string) (Service, error)
	key(service, namespace string) string
}

// xdsServiceStoreWrapper wraps the store interface and holds the store map
type xdsServiceStoreWrapper struct {
	store map[string]Service
}

// NewXdsServiceStore return a new ServiceEndpointStore
func NewXdsServiceStore() *xdsServiceStoreWrapper {
	return &xdsServiceStoreWrapper{
		store: make(map[string]Service),
	}
}

// All returns everything in the store
func (s *xdsServiceStoreWrapper) All() map[string]Service {
	return s.store
}

// AddOrUpdate adds or updates a Service in the store
func (s *xdsServiceStoreWrapper) AddOrUpdate(service *v1.Service, xdsService Service) {
	key := s.key(service.Name, service.Namespace)
	xdsService.Service = service
	s.store[key] = xdsService
}

// Get returns the stored xdsService for the respective Service name and
// namespace
func (s *xdsServiceStoreWrapper) Get(service, namespace string) (Service, error) {
	key := s.key(service, namespace)
	if svc, ok := s.store[key]; !ok {
		return Service{}, fmt.Errorf("Service not found in store")
	} else {
		return svc, nil
	}
}

// key constructs a store key from the given service name and namespace
func (s *xdsServiceStoreWrapper) key(service, namespace string) string {
	return fmt.Sprintf("%s.%s", service, namespace)
}

func makeRouteConfig(name, namespace, authority string, port int32, retry *routev3.RetryPolicy, hashPolicies []*routev3.RouteAction_HashPolicy) *routev3.RouteConfiguration {
	routeName := makeRouteConfigName(name, namespace, port)
	clusterName := makeClusterName(name, namespace, port)
	virtualHostName := makeVirtualHostName(name, namespace, port)
	domains := []string{makeGlobalServiceDomain(name, namespace, port)}
	if authority != "" {
		routeName = makeXdstpRouteConfigName(name, namespace, authority, port)
		clusterName = makeXdstpClusterName(name, namespace, authority, port)
		virtualHostName = makeXdstpVirtualHostName(name, namespace, authority, port)
		domains = append(domains, virtualHostName)
	}
	return routeConfig(routeName, clusterName, virtualHostName, domains, retry, hashPolicies)
}

func routeConfig(routeName, clusterName, virtualHostName string, domains []string, retry *routev3.RetryPolicy, hashPolicies []*routev3.RouteAction_HashPolicy) *routev3.RouteConfiguration {
	return &routev3.RouteConfiguration{
		Name: routeName,
		VirtualHosts: []*routev3.VirtualHost{
			{
				Name:    virtualHostName,
				Domains: domains,
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
							HashPolicy: hashPolicies,
						},
					},
				}},
				RetryPolicy: retry,
			},
		},
	}
}

// makeAllKubeServicesRouteConfig will return all the available routes in a
// Kubernetes cluster. It is meant to be combined with envoy clients that will
// also configure on-demand CDS and EDS for lazy resources discovery.
func makeAllKubeServicesRouteConfig(serviceStore XdsServiceStore) *routev3.RouteConfiguration {
	vh := []*routev3.VirtualHost{}
	for _, s := range serviceStore.All() {
		for _, port := range s.Service.Spec.Ports {
			clusterName := makeClusterName(s.Service.Name, s.Service.Namespace, port.Port)
			virtualHostName := makeVirtualHostName(s.Service.Name, s.Service.Namespace, port.Port)
			domains := []string{makeGlobalServiceDomain(s.Service.Name, s.Service.Namespace, port.Port)}
			vh = append(vh, &routev3.VirtualHost{
				Name:    virtualHostName,
				Domains: domains,
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
							HashPolicy: s.RingHashPolicies,
						},
					},
				}},
				RetryPolicy: s.Retry,
			})
		}
	}
	if len(vh) > 0 {
		return &routev3.RouteConfiguration{
			Name:         "all_kube_routes",
			VirtualHosts: vh,
		}
	}
	return nil
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

func makeListener(name, namespace, authority string, port int32, manager *anypb.Any) *listenerv3.Listener {
	listenerName := makeListenerName(name, namespace, port)
	if authority != "" {
		listenerName = makeXdstpListenerName(name, namespace, authority, port)
	}
	return listener(listenerName, manager)
}

func listener(listenerName string, manager *anypb.Any) *listenerv3.Listener {
	return &listenerv3.Listener{
		Name: listenerName,
		ApiListener: &listenerv3.ApiListener{
			ApiListener: manager,
		},
	}
}

func makeCluster(name, namespace, authority string, port int32, policy clusterv3.Cluster_LbPolicy, ringHash *clusterv3.Cluster_RingHashLbConfig) *clusterv3.Cluster {
	clusterName := makeClusterName(name, namespace, port)
	if authority != "" {
		clusterName = makeXdstpClusterName(name, namespace, authority, port)
	}

	cluster := cluster(clusterName, policy)
	if authority != "" {
		// This will be the name of the subsequently requested ClusterLoadAssignment. We need to set this
		// to the cluster name to hit resources in the cache and reply to EDS requests
		cluster.EdsClusterConfig.ServiceName = clusterName
	}

	switch policy {
	case clusterv3.Cluster_RING_HASH:
		cluster.LbConfig = &clusterv3.Cluster_RingHashLbConfig_{
			RingHashLbConfig: ringHash,
		}
	}

	return cluster
}

func cluster(clusterName string, policy clusterv3.Cluster_LbPolicy) *clusterv3.Cluster {
	return &clusterv3.Cluster{
		Name:                 clusterName,
		ClusterDiscoveryType: &clusterv3.Cluster_Type{Type: clusterv3.Cluster_EDS},
		LbPolicy:             policy,
		Http2ProtocolOptions: &corev3.Http2ProtocolOptions{}, // Set so that Envoy will assume that the upstream supports HTTP/2
		EdsClusterConfig: &clusterv3.Cluster_EdsClusterConfig{
			EdsConfig: &corev3.ConfigSource{
				ConfigSourceSpecifier: &corev3.ConfigSource_Ads{
					Ads: &corev3.AggregatedConfigSource{},
				},
			},
		},
	}
}

// servicesToResources will return a set of listener, routeConfiguration and
// cluster for each service port
func servicesToResources(serviceStore XdsServiceStore, authority string) ([]types.Resource, []types.Resource, []types.Resource, error) {
	var cls []types.Resource
	var rds []types.Resource
	var lsnr []types.Resource
	for _, s := range serviceStore.All() {
		for _, port := range s.Service.Spec.Ports {
			routeConfig := makeRouteConfig(s.Service.Name, s.Service.Namespace, authority, port.Port, s.Retry, s.RingHashPolicies)
			rds = append(rds, routeConfig)
			manager, err := makeManager(routeConfig)
			if err != nil {
				log.Logger.Error("Cannot create listener manager", "error", err)
				continue
			}
			listener := makeListener(s.Service.Name, s.Service.Namespace, authority, port.Port, manager)
			lsnr = append(lsnr, listener)
			cluster := makeCluster(s.Service.Name, s.Service.Namespace, authority, port.Port, s.Policy, s.RingHash)
			cls = append(cls, cluster)
		}
	}
	if allKubeRoutes := makeAllKubeServicesRouteConfig(serviceStore); allKubeRoutes != nil {
		rds = append(rds, allKubeRoutes)
	}
	return cls, rds, lsnr, nil
}
