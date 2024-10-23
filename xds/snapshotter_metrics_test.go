package xds

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
	serviceStore := NewXdsServiceStore()
	for _, s := range services {
		serviceStore.AddOrUpdate(s, Service{
			Policy:                   clusterv3.Cluster_ROUND_ROBIN,
			EnableRemoteEndpoints:    false,
			PrioritizeLocalEndpoints: false,
		})
	}
	endpointStore := NewXdsEnpointStore()
	for _, e := range endpointSlices {
		endpointStore.Add("foo", "bar", e, uint32(0))
	}
	tests := []struct {
		name           string
		nodeID         string
		streamID       int64
		nodeAddress    string
		services       []*v1.Service
		serviceStore   XdsServiceStore
		endpointSlices []*discoveryv1.EndpointSlice
		endpointStore  XdsEndpointStore
		metrics        []string
	}{
		{
			name:           "ok",
			nodeID:         "test-node",
			streamID:       int64(1),
			nodeAddress:    "10.0.0.1",
			services:       services,
			serviceStore:   serviceStore,
			endpointSlices: endpointSlices,
			endpointStore:  endpointStore,
			metrics: []string{
				fmt.Sprintf(`semaphore_xds_node_info{address="%s",node_id="%s"} 1`, "10.0.0.1", "test-node"),
				fmt.Sprintf(`semaphore_xds_snapshot_listener{name="%s",node_id="%s",route_config="%s",type="%s"} 1`, expectHttpListenerName, EmptyNodeID, expectHttpRouteName, resource.ListenerType),
				fmt.Sprintf(`semaphore_xds_snapshot_listener{name="%s",node_id="%s",route_config="%s",type="%s"} 1`, expectHttpsListenerName, EmptyNodeID, expectHttpsRouteName, resource.ListenerType),
				fmt.Sprintf(`semaphore_xds_snapshot_listener{name="%s",node_id="%s",route_config="%s",type="%s"} 1`, expectHttpListenerName, "test-node", expectHttpRouteName, resource.ListenerType),
				fmt.Sprintf(`semaphore_xds_snapshot_listener{name="%s",node_id="%s",route_config="%s",type="%s"} 1`, expectHttpsListenerName, "test-node", expectHttpsRouteName, resource.ListenerType),
				fmt.Sprintf(`semaphore_xds_snapshot_route{cluster_name="%s",domains="%s",name="%s",node_id="%s",path_prefix="",type="%s",virtual_host="%s"} 1`, expectHttpClusterName, expectHttpDomains, expectHttpRouteName, EmptyNodeID, resource.RouteType, expectHttpVhost),
				fmt.Sprintf(`semaphore_xds_snapshot_route{cluster_name="%s",domains="%s",name="%s",node_id="%s",path_prefix="",type="%s",virtual_host="%s"} 1`, expectHttpsClusterName, expectHttpsDomains, expectHttpsRouteName, EmptyNodeID, resource.RouteType, expectHttpsVhost),
				fmt.Sprintf(`semaphore_xds_snapshot_cluster{discovery_type="eds",lb_policy="round_robin",name="%s",node_id="%s",type="%s"} 1`, expectHttpClusterName, EmptyNodeID, resource.ClusterType),
				fmt.Sprintf(`semaphore_xds_snapshot_cluster{discovery_type="eds",lb_policy="round_robin",name="%s",node_id="%s",type="%s"} 1`, expectHttpsClusterName, EmptyNodeID, resource.ClusterType),
				fmt.Sprintf(`semaphore_xds_snapshot_endpoint{cluster_name="%s",health_status="healthy",lb_address="%s",locality_subzone="foo-xzf",locality_zone="test",node_id="%s",priority="0",type="%s"} 1`, expectHttpClusterName, "10.2.1.1:80", EmptyNodeID, resource.EndpointType),
				fmt.Sprintf(`semaphore_xds_snapshot_endpoint{cluster_name="%s",health_status="healthy",lb_address="%s",locality_subzone="foo-xzf",locality_zone="test",node_id="%s",priority="0",type="%s"} 1`, expectHttpsClusterName, "10.2.1.1:443", EmptyNodeID, resource.EndpointType),
			},
		},
	}
	log.InitLogger("test-semaphore-xds-metrics", "debug")
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
			snapshotter.SnapServices(tt.serviceStore)
			snapshotter.SnapEndpoints(tt.endpointStore)
			snapshotter.addOrUpdateNode(tt.nodeID, tt.nodeAddress, tt.streamID)
			if err := snapshotter.updateStreamNodeResources(tt.nodeID, resource.ListenerType, tt.streamID, []string{expectHttpListenerName}); err != nil {
				t.Fatal(err)
			}
			if err := snapshotter.updateStreamNodeResources(tt.nodeID, resource.ListenerType, tt.streamID, []string{expectHttpsListenerName}); err != nil {
				t.Fatal(err)
			}
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
