package xds

import (
	"fmt"
	"strings"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	routev3 "github.com/envoyproxy/go-control-plane/envoy/config/route/v3"
	"github.com/envoyproxy/go-control-plane/pkg/cache/types"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
)

// makeDummyResources blindly creates default configuration resources to snapshot based on the requested names.
// For endpoints it will create a single localhost one
func makeDummyResources(typeURL string, resources []string) ([]types.Resource, error) {
	res := []types.Resource{}
	if typeURL == resource.ListenerType {
		for _, r := range resources {
			clusterName := strings.Replace(r, ":", ".", 1) // Replace expected format of name.namespace:port -> name.namespace.port to match cluster names due to xds naming
			routeConfig := dummyRoute(r, clusterName, r, []string{r}, nil, []*routev3.RouteAction_HashPolicy{})
			manager, err := makeManager(routeConfig)
			if err != nil {
				return res, fmt.Errorf("Cannot generate listener manager: %v", err)
			}
			l := listener(r, manager)
			res = append(res, l)
		}
	}
	if typeURL == resource.RouteType {
		for _, r := range resources {
			clusterName := strings.Replace(r, ":", ".", 1) // Replace expected format of name.namespace:port -> name.namespace.port to match cluster names due to xds naming
			res = append(res, dummyRoute(r, clusterName, r, []string{r}, nil, []*routev3.RouteAction_HashPolicy{}))
		}
	}
	if typeURL == resource.ClusterType {
		for _, r := range resources {
			res = append(res, cluster(r, clusterv3.Cluster_ROUND_ROBIN))
		}
	}
	if typeURL == resource.EndpointType {
		for _, r := range resources {
			res = append(res, localhostClusterLoadAssignment(r))
		}
	}
	return res, nil
}

func dummyRoute(routeName, clusterName, virtualHostName string, domains []string, retry *routev3.RetryPolicy, hashPolicies []*routev3.RouteAction_HashPolicy) *routev3.RouteConfiguration {
	return &routev3.RouteConfiguration{
		Name: routeName,
		VirtualHosts: []*routev3.VirtualHost{
			{
				Name:    virtualHostName,
				Domains: domains,
				Routes: []*routev3.Route{
					{
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
					},
				},
				RetryPolicy: retry,
			},
		},
	}
}
