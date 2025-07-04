// Contains functions to translate Kubernetes Service resources into xDS server
// config
package xds

import (
	"fmt"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	on_demandv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/http/on_demand/v3"
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

func makeVirtualHost(name, namespace, authority string, port int32, retry *routev3.RetryPolicy, hashPolicies []*routev3.RouteAction_HashPolicy) *routev3.VirtualHost {
	clusterName := makeClusterName(name, namespace, port)
	virtualHostName := makeVirtualHostName(name, namespace, port)
	domains := []string{makeGlobalServiceDomain(name, namespace, port)}
	if authority != "" {
		clusterName = makeXdstpClusterName(name, namespace, authority, port)
		virtualHostName = makeXdstpVirtualHostName(name, namespace, authority, port)
		domains = append(domains, virtualHostName)
	}
	return &routev3.VirtualHost{
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
	}
}
func makeRouteConfig(name, namespace, authority string, port int32, virtualHosts []*routev3.VirtualHost) *routev3.RouteConfiguration {
	routeName := makeRouteConfigName(name, namespace, port)
	if authority != "" {
		routeName = makeXdstpRouteConfigName(name, namespace, authority, port)
	}
	return &routev3.RouteConfiguration{
		Name:         routeName,
		VirtualHosts: virtualHosts,
	}
}

// makeKubeDynamicRouteConfig return config for dynamic virtual route discovery
// Needed as VHDS cannot work via static config but needs to be discovered via
// xDS to be able to trigger discivery requests:
// https://github.com/envoyproxy/envoy/issues/23263#issuecomment-1260344146
func makeKubeDynamicRouteConfig() *routev3.RouteConfiguration {
	return &routev3.RouteConfiguration{
		Name: "kube_dynamic",
		Vhds: &routev3.Vhds{
			ConfigSource: &corev3.ConfigSource{
				ResourceApiVersion: corev3.ApiVersion_V3, // Explicitly set to V3
				ConfigSourceSpecifier: &corev3.ConfigSource_ApiConfigSource{
					ApiConfigSource: &corev3.ApiConfigSource{
						ApiType:             corev3.ApiConfigSource_DELTA_GRPC,
						TransportApiVersion: corev3.ApiVersion_V3,
						GrpcServices: []*corev3.GrpcService{
							{
								TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
									EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
										ClusterName: "xds_cluster",
									},
								},
							},
						},
					},
				},
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
		cluster.EdsClusterConfig.ServiceName = makeXdstpClusterLoadAssignmentName(name, namespace, authority, port)
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
		EdsClusterConfig: &clusterv3.Cluster_EdsClusterConfig{
			EdsConfig: &corev3.ConfigSource{
				ConfigSourceSpecifier: &corev3.ConfigSource_Ads{
					Ads: &corev3.AggregatedConfigSource{},
				},
			},
		},
	}
}

// patchClusterDeltaEDS patches a cluster's EDS config to configure the clients
// to use Delta streams. It is meant to be used only for envoy clients where we
// configure an xds server as "xds_cluster" via injected config.
func patchClusterDeltaEDS(cluster *clusterv3.Cluster) *clusterv3.Cluster {
	cluster.Http2ProtocolOptions = &corev3.Http2ProtocolOptions{} // needed to force protocol on envoy
	cluster.EdsClusterConfig = &clusterv3.Cluster_EdsClusterConfig{
		EdsConfig: &corev3.ConfigSource{
			ConfigSourceSpecifier: &corev3.ConfigSource_ApiConfigSource{
				ApiConfigSource: &corev3.ApiConfigSource{
					ApiType:             corev3.ApiConfigSource_DELTA_GRPC,
					TransportApiVersion: corev3.ApiVersion_V3,
					GrpcServices: []*corev3.GrpcService{
						{
							TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
								EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
									ClusterName: "xds_cluster", // hacky, assumes envoy config naming for xDS cluster
								},
							},
						},
					},
				},
			},
		},
	}
	return cluster
}

// patchVirtualHostForOnDemandDiscovery will patch VirtualHosts config to
// configure lazy cluster discovery per route. This is needed in addition to
// the envoy.filters.http.on_demand http filter definition in
// RouteConfiguration as mentioned in the workaround here:
// https://github.com/envoyproxy/envoy/issues/24726
func patchVirtualHostForOnDemandDiscovery(vh *routev3.VirtualHost) (*routev3.VirtualHost, error) {
	onDemandCds := &on_demandv3.OnDemandCds{
		Source: &corev3.ConfigSource{
			ResourceApiVersion: corev3.ApiVersion_V3,
			ConfigSourceSpecifier: &corev3.ConfigSource_ApiConfigSource{
				ApiConfigSource: &corev3.ApiConfigSource{
					ApiType:             corev3.ApiConfigSource_DELTA_GRPC,
					TransportApiVersion: corev3.ApiVersion_V3,
					GrpcServices: []*corev3.GrpcService{
						{
							TargetSpecifier: &corev3.GrpcService_EnvoyGrpc_{
								EnvoyGrpc: &corev3.GrpcService_EnvoyGrpc{
									ClusterName: "xds_cluster",
								},
							},
						},
					},
				},
			},
		},
	}
	// Create PerRouteConfig with OnDemandCds
	perRouteConfig := &on_demandv3.PerRouteConfig{
		Odcds: onDemandCds,
	}
	// Convert PerRouteConfig to protobuf.Any
	typedConfig, err := anypb.New(perRouteConfig)
	if err != nil {
		return nil, fmt.Errorf("Cannot create on demand typedConfig: %v", err)
	}

	for _, route := range vh.Routes {
		route.TypedPerFilterConfig = map[string]*anypb.Any{
			"envoy.filters.http.on_demand": typedConfig,
		}
	}
	return vh, nil
}

// servicesToResources will return a set of listener, routeConfiguration and
// cluster for each service port
func servicesToResources(serviceStore XdsServiceStore, authority string) ([]types.Resource, []types.Resource, []types.Resource, error) {
	var cls []types.Resource
	var rds []types.Resource
	var lsnr []types.Resource
	for _, s := range serviceStore.All() {
		for _, port := range s.Service.Spec.Ports {
			vh := makeVirtualHost(s.Service.Name, s.Service.Namespace, authority, port.Port, s.Retry, s.RingHashPolicies)
			routeConfig := makeRouteConfig(s.Service.Name, s.Service.Namespace, authority, port.Port, []*routev3.VirtualHost{vh})
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
	return cls, rds, lsnr, nil
}

// servicesToResourcesWithNames returns maps of VirtualHost and Cluster type
// resources as expected by a Linear cache
func servicesToResourcesWithNames(serviceStore XdsServiceStore, authority string) (map[string]types.Resource, map[string]types.Resource, map[string]types.Resource) {
	cls := make(map[string]types.Resource)
	vhds := make(map[string]types.Resource)
	rds := make(map[string]types.Resource)
	for _, s := range serviceStore.All() {
		for _, port := range s.Service.Spec.Ports {
			vh := makeVirtualHost(s.Service.Name, s.Service.Namespace, authority, port.Port, s.Retry, s.RingHashPolicies)
			vh.Name = fmt.Sprintf("kube_dynamic/%s", vh.Name) // patch virtual host name to prefix with kube_dynamic as requests will be expected based on route config name
			patchedVH, err := patchVirtualHostForOnDemandDiscovery(vh)
			if err != nil {
				log.Logger.Warn("Failed to patch on demand discovery configuration, skipping VirtualHost", "name", vh.Name, "error", err)
			}
			vhds[vh.Name] = patchedVH
			cluster := makeCluster(s.Service.Name, s.Service.Namespace, authority, port.Port, s.Policy, s.RingHash)
			cls[cluster.Name] = patchClusterDeltaEDS(cluster)
		}
	}
	rds["kube_dynamic"] = makeKubeDynamicRouteConfig() // kube_dynamic route added to Linear Caches for DELTA_GRPC clients (envoy)
	return cls, vhds, rds
}
