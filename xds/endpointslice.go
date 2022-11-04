// Contains functions to translate Kubernetes EndpointSlice resources into xDS
// server config.
package xds

import (
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"google.golang.org/protobuf/types/known/wrapperspb"
	discoveryv1 "k8s.io/api/discovery/v1"
)

const (
	endpointSliceServiceLabel = "kubernetes.io/service-name"
)

// ServiceEndpoints groups of Kubernetes EndpointSlices for each Service
type ServiceEndpoints map[string][]*discoveryv1.EndpointSlice

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

// readServiceEndpoints lists all watched EndpointSlices into ServiceEndpoints.
// It will group endpointslices based on the `kubernetes.io/service-name` label.
func readServiceEndpoints(endpointSlices []*discoveryv1.EndpointSlice) (ServiceEndpoints, error) {
	seps := ServiceEndpoints{}
	for _, ep := range endpointSlices {
		if serviceName, ok := ep.Labels[endpointSliceServiceLabel]; !ok {
			log.Logger.Warn("Ignoring endpointSlice with missing ownership label", "endpointSlice", ep.Name, "label", endpointSliceServiceLabel)
			continue
		} else {
			if _, ok := seps[serviceName]; !ok {
				seps[serviceName] = []*discoveryv1.EndpointSlice{ep}
			} else {
				seps[serviceName] = append(seps[serviceName], ep)
			}
		}
	}
	return seps, nil
}

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
func createClustersForServiceEndpoints(seps ServiceEndpoints) EdsClusters {
	clusters := make(EdsClusters)
	for service, endpointSlices := range seps {
		for _, e := range endpointSlices {
			for _, ce := range endpointSliceToClusterEndpoints(e) {
				clusterName := makeClusterName(service, e.Namespace, ce.port) // Let's assuume that Service and the respective EndpointSlice live in the same namespace
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
func endpointSlicesToClusterLoadAssignments(endpointSlices []*discoveryv1.EndpointSlice) ([]types.Resource, error) {
	eds := []types.Resource{}
	seps, err := readServiceEndpoints(endpointSlices)
	if err != nil {
		return nil, err
	}
	clusters := createClustersForServiceEndpoints(seps)
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
