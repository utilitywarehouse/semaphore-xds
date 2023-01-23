// Contains functions to translate Kubernetes EndpointSlice resources into xDS
// server config.
package xds

import (
	"fmt"

	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"google.golang.org/protobuf/types/known/wrapperspb"
	discoveryv1 "k8s.io/api/discovery/v1"
)

// xdsEndpoint groups Kubernetes EndpointSlices for each Service
type xdsEndpoint struct {
	service        string                       // the name of the Kubernetes Service that owns the EndpointSlices
	namespace      string                       // the Kubernetes namespace which holds the resources
	endpointSlices []*discoveryv1.EndpointSlice // the Kubernetes EndpointSlices to feed endpoints
}

// XdsEndpointStore is a store of xdsEndpoint objects. It shall be used
// to populate a map of serviceEndpoint objects and passed to the snapshotter
// so only add and get functions should be implemented. For new snapshots we
// should create new stores
type XdsEndpointStore interface {
	All() map[string]xdsEndpoint
	Add(service, namespace string, eps *discoveryv1.EndpointSlice)
	Get(service, namespace string) xdsEndpoint
	key(service, namespace string) string
}

// ServiceEndpointStoreWrapper wraps the store interface and holds the store map
type xdsEndpointStoreWrapper struct {
	store map[string]xdsEndpoint
}

// NewServiceEnpointStore return a new ServiceEndpointStore
func NewXdsEnpointStore() *xdsEndpointStoreWrapper {
	return &xdsEndpointStoreWrapper{
		store: make(map[string]xdsEndpoint),
	}
}

// All returns everything in the store
func (s *xdsEndpointStoreWrapper) All() map[string]xdsEndpoint {
	return s.store
}

// Add adds an EndpointSlice to the store
func (s *xdsEndpointStoreWrapper) Add(service, namespace string, eps *discoveryv1.EndpointSlice) {
	key := s.key(service, namespace)
	if se, ok := s.store[key]; !ok {
		s.store[key] = xdsEndpoint{
			service:        service,
			namespace:      namespace,
			endpointSlices: []*discoveryv1.EndpointSlice{eps},
		}
	} else {
		se.endpointSlices = append(se.endpointSlices, eps)
		s.store[key] = se
	}
}

// Get returns the stored serviceEndpoint for the respective Service name and
// namespace
func (s *xdsEndpointStoreWrapper) Get(service, namespace string) xdsEndpoint {
	key := s.key(service, namespace)
	if se, ok := s.store[key]; !ok {
		return xdsEndpoint{}
	} else {
		return se
	}
}

// key constructs a store key from the given service name and namespace
func (s *xdsEndpointStoreWrapper) key(service, namespace string) string {
	return fmt.Sprintf("%s.%s", service, namespace)
}

func endpointAddress(address string, port int32) *corev3.Address {
	return &corev3.Address{Address: &corev3.Address_SocketAddress{
		SocketAddress: &corev3.SocketAddress{
			Address:  address,
			Protocol: corev3.SocketAddress_TCP,
			PortSpecifier: &corev3.SocketAddress_PortValue{
				PortValue: uint32(port),
			},
		},
	}}
}

func lbEndpoint(address string, port int32, healthy bool) *endpointv3.LbEndpoint {
	hs := corev3.HealthStatus_HEALTHY
	if !healthy {
		hs = corev3.HealthStatus_UNHEALTHY
	}
	return &endpointv3.LbEndpoint{
		HostIdentifier: &endpointv3.LbEndpoint_Endpoint{
			Endpoint: &endpointv3.Endpoint{
				Address: endpointAddress(address, port),
			}},
		HealthStatus: hs,
	}
}

// localityEndpoints creates xds server locality endpoints configuration. We can
// specify priorities and localities here. Probably this explains how load
// balancing works:
// https://github.com/grpc/grpc/blob/adfd009d3a255b825ea91959620c11805418b22b/src/core/ext/filters/client_channel/lb_policy/address_filtering.h#L31-L81
func localityEndpoints(lbEndpoints []*endpointv3.LbEndpoint, zone, subzone string) *endpointv3.LocalityLbEndpoints {
	leps := &endpointv3.LocalityLbEndpoints{
		Locality: &corev3.Locality{
			Zone:    zone,
			SubZone: subzone,
		},
		LoadBalancingWeight: &wrapperspb.UInt32Value{Value: uint32(100)},
		LbEndpoints:         lbEndpoints,
	}
	return leps
}

// clusterLoadAssignment returns ClusterLoadAssignment EDS cluster config
func clusterLoadAssignment(clusterName string, lEndpoints []*endpointv3.LocalityLbEndpoints) *endpointv3.ClusterLoadAssignment {
	return &endpointv3.ClusterLoadAssignment{
		ClusterName: clusterName,
		Endpoints:   lEndpoints,
	}
}

// EdsClusterEndpoints will store EndpointSlice data needed in cluster creation
type EdsClusterEndpoints struct {
	addresses []string
	port      int32
	zone      string
	subzone   string
	healthy   bool
}

// EDSCluster holds the data to create an EDS cluster load assignment
type EdsCluster struct {
	endpoints []EdsClusterEndpoints
}

// EdsClusters hold a map of EDS clusters
type EdsClusters map[string]EdsCluster

// endpointSliceToClusterEndpoints will represent Kubernetes Endpoints as
// EdsClusterEndpoints structured vars to be used when rendering snapshots
func endpointSliceToClusterEndpoints(e *discoveryv1.EndpointSlice) []EdsClusterEndpoints {
	ceps := []EdsClusterEndpoints{}
	for _, p := range e.Ports {
		for _, ep := range e.Endpoints {
			ceps = append(ceps, EdsClusterEndpoints{
				addresses: ep.Addresses,
				port:      *p.Port,
				zone:      *ep.Zone,
				subzone:   ep.TargetRef.Name, // This should be the respective pod name, hacky way to have separate localities per endpoint.
				healthy:   *ep.Conditions.Ready,
			})
		}
	}
	return ceps
}

// createClustersForServiceEndpoints translates service endpoints into a list
// of EDSCluster objects
func createClustersFromEndpointStore(store XdsEndpointStore) EdsClusters {
	clusters := make(EdsClusters)
	for _, serviceEndpoint := range store.All() {
		for _, e := range serviceEndpoint.endpointSlices {
			for _, ce := range endpointSliceToClusterEndpoints(e) {
				clusterName := makeClusterName(serviceEndpoint.service, serviceEndpoint.namespace, ce.port)
				if c, ok := clusters[clusterName]; !ok {
					clusters[clusterName] = EdsCluster{
						endpoints: []EdsClusterEndpoints{ce},
					}
				} else {
					c.endpoints = append(c.endpoints, ce)
					clusters[clusterName] = c
				}
			}
		}
	}
	return clusters
}

// endpointSlicesToClusterLoadAssignments expects an EndpointSlice watcher and
// will list watched resources as a clusterLoadAssignment
func endpointSlicesToClusterLoadAssignments(endpointStore XdsEndpointStore) ([]types.Resource, error) {
	eds := []types.Resource{}
	clusters := createClustersFromEndpointStore(endpointStore)
	for name, cluster := range clusters {
		var localityEps []*endpointv3.LocalityLbEndpoints
		for _, endpoint := range cluster.endpoints {
			var lbes []*endpointv3.LbEndpoint
			for _, a := range endpoint.addresses {
				lbes = append(lbes, lbEndpoint(a, endpoint.port, endpoint.healthy))
			}
			localityEps = append(localityEps, localityEndpoints(lbes, endpoint.zone, endpoint.subzone))
		}
		eds = append(eds, clusterLoadAssignment(name, localityEps))
	}
	return eds, nil
}