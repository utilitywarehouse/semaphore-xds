package metrics

import (
	"fmt"
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/mdlayher/promtest"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

func TestSnapMetricsCollector(t *testing.T) {
	// Test kube public objects to be snapshotted
	var (
		httpPortName   = "http"
		httpPortValue  = int32(80)
		httpsPortName  = "https"
		httpsPortValue = int32(443)
		zone           = "test"
		ready          = true
		services       = []*v1.Service{&v1.Service{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: "bar",
			},
			Spec: v1.ServiceSpec{
				Ports: []v1.ServicePort{
					v1.ServicePort{
						Name: httpPortName,
						Port: httpPortValue,
					},
					v1.ServicePort{
						Name: httpsPortName,
						Port: httpsPortValue,
					},
				},
			},
		}}
		endpointSlices = []*discoveryv1.EndpointSlice{&discoveryv1.EndpointSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "foo",
				Namespace: "bar",
				Labels: map[string]string{
					"kubernetes.io/service-name": "foo", // TODO: importing from kube introduces a cycle
				},
			},
			Endpoints: []discoveryv1.Endpoint{
				discoveryv1.Endpoint{
					Addresses: []string{"10.2.1.1"},
					Zone:      &zone,
					TargetRef: &v1.ObjectReference{Name: "foo-xzf"},
					Conditions: discoveryv1.EndpointConditions{
						Ready: &ready,
					},
				}},
			Ports: []discoveryv1.EndpointPort{
				discoveryv1.EndpointPort{
					Name: &httpPortName,
					Port: &httpPortValue,
				},
				discoveryv1.EndpointPort{
					Name: &httpsPortName,
					Port: &httpsPortValue,
				},
			},
		}}
		// Expected values from the above resources
		expectHttpListenerName  = "foo.bar:80"
		expectHttpsListenerName = "foo.bar:443"
		expectHttpRouteName     = "foo.bar:80"
		expectHttpsRouteName    = "foo.bar:443"
		expectHttpVhost         = "foo.bar:80"
		expectHttpsVhost        = "foo.bar:443"
		expectHttpDomains       = "foo.bar:80"
		expectHttpsDomains      = "foo.bar:443"
		expectHttpClusterName   = "foo.bar.80"
		expectHttpsClusterName  = "foo.bar.443"
	)
	serviceStore := xds.NewXdsServiceStore()
	for _, s := range services {
		serviceStore.AddOrUpdate(s, clusterv3.Cluster_ROUND_ROBIN)
	}
	endpointStore := xds.NewXdsEnpointStore()
	for _, e := range endpointSlices {
		endpointStore.Add("foo", "bar", e)
	}
	tests := []struct {
		name           string
		services       []*v1.Service
		serviceStore   xds.XdsServiceStore
		endpointSlices []*discoveryv1.EndpointSlice
		endpointStore  xds.XdsEndpointStore
		metrics        []string
	}{
		{
			name:           "ok",
			services:       services,
			serviceStore:   serviceStore,
			endpointSlices: endpointSlices,
			endpointStore:  endpointStore,
			metrics: []string{
				fmt.Sprintf(`semaphore_xds_snapshot_listener{name="%s",route_config="%s",type="%s"} 1`, expectHttpListenerName, expectHttpRouteName, resource.ListenerType),
				fmt.Sprintf(`semaphore_xds_snapshot_listener{name="%s",route_config="%s",type="%s"} 1`, expectHttpsListenerName, expectHttpsRouteName, resource.ListenerType),
				fmt.Sprintf(`semaphore_xds_snapshot_route{cluster_name="%s",domains="%s",name="%s",path_prefix="",type="%s",virtual_host="%s"} 1`,
					expectHttpClusterName, expectHttpDomains, expectHttpRouteName, resource.RouteType, expectHttpVhost),
				fmt.Sprintf(`semaphore_xds_snapshot_route{cluster_name="%s",domains="%s",name="%s",path_prefix="",type="%s",virtual_host="%s"} 1`,
					expectHttpsClusterName, expectHttpsDomains, expectHttpsRouteName, resource.RouteType, expectHttpsVhost),
				fmt.Sprintf(`semaphore_xds_snapshot_cluster{discovery_type="eds",lb_policy="round_robin",name="%s",type="%s"} 1`, expectHttpClusterName, resource.ClusterType),
				fmt.Sprintf(`semaphore_xds_snapshot_cluster{discovery_type="eds",lb_policy="round_robin",name="%s",type="%s"} 1`, expectHttpsClusterName, resource.ClusterType),
				fmt.Sprintf(`semaphore_xds_snapshot_endpoint{cluster_name="%s",health_status="healthy",lb_address="%s",locality_subzone="foo-xzf",locality_zone="test",type="%s"} 1`,
					expectHttpClusterName, "10.2.1.1:80", resource.EndpointType),
				fmt.Sprintf(`semaphore_xds_snapshot_endpoint{cluster_name="%s",health_status="healthy",lb_address="%s",locality_subzone="foo-xzf",locality_zone="test",type="%s"} 1`,
					expectHttpsClusterName, "10.2.1.1:443", resource.EndpointType),
			},
		},
	}
	log.InitLogger("test-semaphore-xds-metrics", "debug")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshotter := xds.NewSnapshotter(uint(0))
			snapshotter.SnapServices(tt.serviceStore)
			snapshotter.SnapEndpoints(tt.endpointStore)
			body := promtest.Collect(t, newSnapMetricsCollector(snapshotter))

			if !promtest.Lint(t, body) {
				t.Fatal("one or more promlint errors found")
			}
			if !promtest.Match(t, body, tt.metrics) {
				t.Fatal("metrics did not match whitelist")
			}
		})
	}
}
