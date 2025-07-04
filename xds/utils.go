package xds

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	corev3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	endpointv3 "github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3"
	listenerv3 "github.com/envoyproxy/go-control-plane/envoy/config/listener/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	managerv3 "github.com/envoyproxy/go-control-plane/envoy/extensions/filters/network/http_connection_manager/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	"github.com/golang/protobuf/ptypes"
	"github.com/golang/protobuf/ptypes/wrappers"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/durationpb"

	xdsTypes "github.com/utilitywarehouse/semaphore-xds/types"
)

func makeClusterName(name, namespace string, port int32) string {
	//return net.JoinHostPort(fmt.Sprintf("%s.%s", name, namespace), strconv.Itoa(int(port)))
	return fmt.Sprintf("%s.%s.%s", name, namespace, strconv.Itoa(int(port)))
}

// This is a bit confusing but it seems simpler to name the listener, route and
// virtual host as the service domain we expect to hit from the client.
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

// xdstp cluster name based on:
// https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/cluster/v3/cluster.proto
func makeXdstpClusterName(name, namespace, authority string, port int32) string {
	return fmt.Sprintf("xdstp://%s/envoy.config.cluster.v3.Cluster/%s", authority, makeClusterName(name, namespace, port))
}

func makeXdstpClusterLoadAssignmentName(name, namespace, authority string, port int32) string {
	return fmt.Sprintf("xdstp://%s/envoy.config.endpoint.v3.ClusterLoadAssignment/%s", authority, makeClusterName(name, namespace, port))
}

// xdstp listener name based on:
// https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/listener/v3/listener.proto#config-listener-v3-listener
func makeXdstpListenerName(name, namespace, authority string, port int32) string {
	return fmt.Sprintf("xdstp://%s/envoy.config.listener.v3.Listener/%s", authority, makeListenerName(name, namespace, port))
}

// xdstp route configuration name based on:
// https://www.envoyproxy.io/docs/envoy/latest/api-v3/config/route/v3/route.proto
func makeXdstpRouteConfigName(name, namespace, authority string, port int32) string {
	return fmt.Sprintf("xdstp://%s/envoy.config.route.v3.RouteConfiguration/%s", authority, makeClusterName(name, namespace, port))
}

func makeXdstpVirtualHostName(name, namespace, authority string, port int32) string {
	return fmt.Sprintf("xdstp://%s/envoy.config.listener.v3.Listener/%s", authority, makeVirtualHostName(name, namespace, port))
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
	err := ptypes.UnmarshalAny(apiListerner, manager)
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

// parseToClusterLbPolicy is the reverse of the above. Accepts a string and
// returns a clusterv3.Cluster_LbPolicy.
func ParseToClusterLbPolicy(policy string) clusterv3.Cluster_LbPolicy {
	switch policy {
	case "round_robin":
		return clusterv3.Cluster_ROUND_ROBIN
	case "least_request":
		return clusterv3.Cluster_LEAST_REQUEST
	case "ring_hash":
		return clusterv3.Cluster_RING_HASH
	case "random":
		return clusterv3.Cluster_RANDOM
	case "maglev":
		return clusterv3.Cluster_MAGLEV
	case "cluster_provided":
		return clusterv3.Cluster_CLUSTER_PROVIDED
	case "load_balancing_policy_config":
		return clusterv3.Cluster_LOAD_BALANCING_POLICY_CONFIG
	default:
		log.Logger.Warn("Failed to parse unkown policy, defaulting to round_robin", "policy", policy)
		return clusterv3.Cluster_ROUND_ROBIN
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

// ParsePriorityStrategy parses priorityStrategy attribute
func ParsePriorityStrategy(strategy xdsTypes.PolicyStrategy) xdsTypes.PolicyStrategy {
	switch strategy {
	case xdsTypes.NoPolicyStrategy:
		return xdsTypes.NoPolicyStrategy
	case xdsTypes.LocalFirstPolicyStrategy:
		return xdsTypes.LocalFirstPolicyStrategy
	default:
		log.Logger.Info("Empty or unknown policy strategy, defaulting to none", "policyStrategy", strategy)
		return xdsTypes.NoPolicyStrategy
	}
}

// PrioritizeLocal returns true if the strategy is set to local-first
func PrioritizeLocal(strategy xdsTypes.PolicyStrategy) bool {
	return strategy == xdsTypes.LocalFirstPolicyStrategy
}

// ParseRetryOn validates the retry_on value and returns it if valid.
func ParseRetryOn(on []string) string {
	valid := make([]string, 0, len(on))
	for _, s := range on {
		valid = append(valid, strings.TrimSpace(strings.ToLower(s)))
	}
	return strings.Join(valid, ",")
}

// ParseNumRetries parses the number of retries.
func ParseNumRetries(num *uint32) *wrappers.UInt32Value {
	if num == nil {
		return &wrappers.UInt32Value{Value: 1}
	}
	return &wrappers.UInt32Value{Value: *num}
}

// ParseRetryBackOff parses the retry backoff values.
// Default base is 25ms and the default max is 10x the base.
func ParseRetryBackOff(base, max string) *routev3.RetryPolicy_RetryBackOff {
	baseDuration, err := time.ParseDuration(base)
	if err != nil {
		baseDuration = 25 * time.Millisecond
	}

	maxDuration, err := time.ParseDuration(max)
	if err != nil {
		maxDuration = 10 * baseDuration
	}
	if maxDuration < baseDuration {
		maxDuration = baseDuration
	}

	return &routev3.RetryPolicy_RetryBackOff{
		BaseInterval: durationpb.New(baseDuration),
		MaxInterval:  durationpb.New(maxDuration),
	}
}

// resourcesMatch checks if 2 slices contain the same set of resources
func resourcesMatch(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, e := range b {
		inSlice := false
		for _, r := range a {
			if e == r {
				inSlice = true
				break
			}
		}
		if !inSlice {
			return false
		}
	}
	return true
}
