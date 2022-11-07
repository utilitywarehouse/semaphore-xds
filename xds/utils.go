package xds

import (
	"fmt"
	"net"
	"strconv"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	managerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/golang/protobuf/ptypes"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	EmptyNodeID = ""
)

func makeClusterName(name, namespace string, port int32) string {
	//return net.JoinHostPort(fmt.Sprintf("%s.%s", name, namespace), strconv.Itoa(int(port)))
	return fmt.Sprintf("%s.%s.%s", name, namespace, strconv.Itoa(int(port)))
}

// This is a bit confusing but it seems simpler to name the listener, route and
//  virtual host as the service domain we expect to hit from the client.
func makeListenerName(name, namespace string, port int32) string {
	return makeGlobalServiceDomain(name, namespace, port)
}

func makeRouteConfigName(name, namespace string, port int32) string {
	return makeGlobalServiceDomain(name, namespace, port)
}

func makeVirtualHostName(name, namespace string, port int32) string {
	return makeGlobalServiceDomain(name, namespace, port)
}

func makeGlobalServiceDomain(name, namespace string, port int32) string {
	return net.JoinHostPort(fmt.Sprintf("%s.%s", name, namespace), strconv.Itoa(int(port)))
}

// UnmarshalResourceToListener parses a resource into a Listener
func UnmarshalResourceToListener(res types.Resource) (*listenerv3.Listener, error) {
	listener := &listenerv3.Listener{}
	data, _ := anypb.New(res)
	err := ptypes.UnmarshalAny(data, listener)
	if err != nil {
		return nil, err
	}
	return listener, nil
}

// UnmarshalResourceToRouteConfiguration parses a resource into RouteConfiguration
func UnmarshalResourceToRouteConfiguration(res types.Resource) (*routev3.RouteConfiguration, error) {
	route := &routev3.RouteConfiguration{}
	data, _ := anypb.New(res)
	err := ptypes.UnmarshalAny(data, route)
	if err != nil {
		return nil, err
	}
	return route, nil
}

// UnmarshalResourceToCluster parses configuration into a CLuster struct
func UnmarshalResourceToCluster(res types.Resource) (*clusterv3.Cluster, error) {
	cluster := &clusterv3.Cluster{}
	data, _ := anypb.New(res)
	err := ptypes.UnmarshalAny(data, cluster)
	if err != nil {
		return nil, err
	}
	return cluster, nil
}

// UnmarshalResourceToEndpoint parses configuration into a ClusterLoadAssignment
func UnmarshalResourceToEndpoint(res types.Resource) (*endpointv3.ClusterLoadAssignment, error) {
	eds := &endpointv3.ClusterLoadAssignment{}
	data, _ := anypb.New(res)
	err := ptypes.UnmarshalAny(data, eds)
	if err != nil {
		return nil, err
	}
	return eds, nil
}

// ExtractManagerFromListener unmarshals manager configuration from listener
func ExtractManagerFromListener(listener *listenerv3.Listener) (*managerv3.HttpConnectionManager, error) {
	apiListerner := listener.ApiListener.ApiListener
	manager := &managerv3.HttpConnectionManager{}
	data, _ := anypb.New(apiListerner)
	err := ptypes.UnmarshalAny(data, manager)
	if err != nil {
		return nil, err
	}
	return manager, nil
}

// ParseClusterLbPolicy returns the Cluster load balancing policy based on:
// https://pkg.go.dev/github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3#Cluster_LbPolicy
func ParseClusterLbPolicy(policy clusterv3.Cluster_LbPolicy) string {
	switch policy {
	case clusterv3.Cluster_ROUND_ROBIN:
		return "round_robin"
	case clusterv3.Cluster_LEAST_REQUEST:
		return "least_request"
	case clusterv3.Cluster_RING_HASH:
		return "ring_hash"
	case clusterv3.Cluster_RANDOM:
		return "random"
	case clusterv3.Cluster_MAGLEV:
		return "maglev"
	case clusterv3.Cluster_CLUSTER_PROVIDED:
		return "cluster_provided"
	case clusterv3.Cluster_LOAD_BALANCING_POLICY_CONFIG:
		return "load_balancing_policy_config"
	default:
		return ""
	}
}

// ParseClusterDiscoveryType returns the Cluster Discovery type based on:
// https://pkg.go.dev/github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3#Cluster_DiscoveryType
func ParseClusterDiscoveryType(discoveryType clusterv3.Cluster_DiscoveryType) string {
	switch discoveryType {
	case clusterv3.Cluster_STATIC:
		return "static"
	case clusterv3.Cluster_STRICT_DNS:
		return "strict_dns"
	case clusterv3.Cluster_LOGICAL_DNS:
		return "logical_dns"
	case clusterv3.Cluster_EDS:
		return "eds"
	case clusterv3.Cluster_ORIGINAL_DST:
		return "original_dst"
	default:
		return ""
	}
}

// ParseLbEndpointHealthStatus returns the Lb Endpoint Health Status based on:
// https://pkg.go.dev/github.com/envoyproxy/go-control-plane@v0.10.3/envoy/config/core/v3#HealthStatus
func ParseLbEndpointHealthStatus(status corev3.HealthStatus) string {
	switch status {
	case corev3.HealthStatus_UNKNOWN:
		return "unknown"
	case corev3.HealthStatus_HEALTHY:
		return "healthy"
	case corev3.HealthStatus_UNHEALTHY:
		return "unhealthy"
	case corev3.HealthStatus_DRAINING:
		return "draining"
	case corev3.HealthStatus_TIMEOUT:
		return "timeout"
	case corev3.HealthStatus_DEGRADED:
		return "degraded"
	default:
		return ""
	}
}
