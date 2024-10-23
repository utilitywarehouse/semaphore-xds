package xds

import (
	"fmt"
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	"github.com/utilitywarehouse/semaphore-xds/log"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var testNodeID = ""

func init() {
	log.InitLogger("test-semaphore-xds", "debug")
}

func TestSnapServices_EmptyServiceList(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	snapshotter.SnapServices(serviceStore)
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will be empty
	assert.Equal(t, 0, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
}

func TestSnapServices_SingleService(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "test",
					Port: int32(80),
				}},
		},
	}
	serviceStore.AddOrUpdate(svc, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	snapshotter.SnapServices(serviceStore)
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will have a single listener, route and
	// cluster
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
}

func TestSnapServices_NoServicePorts(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		}}
	serviceStore.AddOrUpdate(svc, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	snapshotter.SnapServices(serviceStore)
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will be empty
	assert.Equal(t, 0, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
}

func TestSnapServices_MultipleServicePorts(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				{
					Name: "http",
					Port: int32(80),
				},
				{
					Name: "https",
					Port: int32(443),
				}},
		},
	}
	serviceStore.AddOrUpdate(svc, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	snapshotter.SnapServices(serviceStore)
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will have one listener, route and cluster
	// per port
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 2, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 2, len(snap.GetResources(resource.RouteType)))
}

func TestSnapEndpoints_EmptyEndpointStore(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	endpointStore := NewXdsEnpointStore()
	snapshotter.SnapEndpoints(endpointStore)
	snap, err := snapshotter.endpointsCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will be empty
	assert.Equal(t, 0, len(snap.GetResources(resource.EndpointType)))
}

func TestSnapEndpoints_MissingServiceOwnershipLabel(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	log.InitLogger("test-semaphore-xds", "debug")
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		}}
	endpointStore := NewXdsEnpointStore()
	endpointStore.Add("foo", "bar", endpointSlice, uint32(0))
	snapshotter.SnapEndpoints(endpointStore)
	snap, err := snapshotter.endpointsCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will be empty
	assert.Equal(t, 0, len(snap.GetResources(resource.EndpointType)))
}

func TestSnapEndpoints_UpdateAddress(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	// Create test EndpointSlice
	httpPortName := "http"
	httpPortValue := int32(80)
	httpsPortName := "https"
	httpsPortValue := int32(443)
	zone := "test"
	ready := true
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Labels: map[string]string{
				"kubernetes.io/service-name": "foobar",
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
	}
	endpointStore := NewXdsEnpointStore()
	endpointStore.Add("foo", "bar", endpointSlice, uint32(0))
	snapshotter.SnapEndpoints(endpointStore)
	snap, err := snapshotter.endpointsCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will contain 2 resources (one per port) with
	// the passed address
	assert.Equal(t, 2, len(snap.GetResources(resource.EndpointType)))
	for _, res := range snap.GetResources(resource.EndpointType) {
		eds, err := UnmarshalResourceToEndpoint(res)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 1, len(eds.Endpoints))
		assert.Equal(t, 1, len(eds.Endpoints[0].LbEndpoints))
		lbEndpoint := eds.Endpoints[0].LbEndpoints[0].GetEndpoint()
		assert.Equal(t, "10.2.1.1", lbEndpoint.Address.GetSocketAddress().Address)
		assert.Equal(t, uint32(0), eds.Endpoints[0].Priority)
	}
	// Update address and priority and check reflection on snapshot
	endpointSlice.Endpoints[0].Addresses = []string{"10.2.2.2"}
	endpointStore = NewXdsEnpointStore()
	endpointStore.Add("foo", "bar", endpointSlice, uint32(1))
	snapshotter.SnapEndpoints(endpointStore)
	snap, err = snapshotter.endpointsCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will contain 2 resources (one per port) with
	// the new address
	assert.Equal(t, 2, len(snap.GetResources(resource.EndpointType)))
	for _, res := range snap.GetResources(resource.EndpointType) {
		eds, err := UnmarshalResourceToEndpoint(res)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, 1, len(eds.Endpoints))
		assert.Equal(t, 1, len(eds.Endpoints[0].LbEndpoints))
		lbEndpoint := eds.Endpoints[0].LbEndpoints[0].GetEndpoint()
		assert.Equal(t, "10.2.2.2", lbEndpoint.Address.GetSocketAddress().Address)
		assert.Equal(t, uint32(1), eds.Endpoints[0].Priority)
	}
}

func TestSnapServices_NodeSnapshotResources(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	// fooA.bar:80
	svcA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fooA",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "test",
					Port: int32(80),
				}},
		},
	}
	serviceStore.AddOrUpdate(svcA, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	// fooB.bar:80
	svcB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fooB",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "test",
					Port: int32(80),
				}},
		},
	}
	serviceStore.AddOrUpdate(svcB, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	// Snap to add services to the default snapshot
	snapshotter.SnapServices(serviceStore)

	// Add a new test node
	nodeID := "test-node"
	streamID := int64(1)
	nodeAddress := "10.0.0.1"
	snapshotter.addOrUpdateNode(nodeID, nodeAddress, streamID)
	// Request all listener resources for the node - Update the node snapshot
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80", "fooB.bar:80"}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80", "fooB.bar:80"}); err != nil {
		t.Fatal(err)
	}
	// Verify Listener resources are now in a snaphot for the node id
	snap, err := snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	// A new ADS request with fewer resources should remove from snapshot
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80"}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80"}); err != nil {
		t.Fatal(err)
	}
	// Verify Listener resources are now in a snaphot for the node id
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	// A full snap should not bring more resources in the node snapshot
	snapshotter.SnapServices(serviceStore)
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	// EmptyNodeID snapshot contains the full map of resources
	snap, err = snapshotter.servicesCache.GetSnapshot(EmptyNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 2, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 2, len(snap.GetResources(resource.RouteType)))
	// Client requesting more resources again should bring them back into the node snapshot
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80", "fooB.bar:80"}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80", "fooB.bar:80"}); err != nil {
		t.Fatal(err)
	}
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	// Requesting non existing resources should leave the list unaffected
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80", "fooB.bar:80", "fooC.bar:80"}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80", "fooB.bar:80", "fooC.bar:80"}); err != nil {
		t.Fatal(err)
	}
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	// Requesting an empty list of resources package
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{""}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{""}); err != nil {
		t.Fatal(err)
	}
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 0, len(snap.GetResources(resource.ListenerType)))
}

func TestSnapServices_MultipleStreams(t *testing.T) {
	snapshotter := NewSnapshotter("", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	// fooA.bar:80
	svcA := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fooA",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "test",
					Port: int32(80),
				}},
		},
	}
	serviceStore.AddOrUpdate(svcA, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	// fooB.bar:80
	svcB := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "fooB",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "test",
					Port: int32(80),
				}},
		},
	}
	serviceStore.AddOrUpdate(svcB, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	// Snap to add services to the default snapshot
	snapshotter.SnapServices(serviceStore)
	// Open a new stream (id:1) for test-node and request 1 of the above services
	nodeID := "test-node"
	streamID := int64(1)
	nodeAddress := "10.0.0.1"
	snapshotter.addOrUpdateNode(nodeID, nodeAddress, streamID)
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80"}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{"fooA.bar:80"}); err != nil {
		t.Fatal(err)
	}
	// Verify the requested listener resources are now in a snaphot for the node id
	snap, err := snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	// Open a new stream (id:2) for test-node and request the other resource in the store
	streamID = 2
	snapshotter.addOrUpdateNode(nodeID, nodeAddress, streamID)
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, streamID, []string{"fooB.bar:80"}))
	if err := snapshotter.updateStreamNodeResources(nodeID, resource.ListenerType, streamID, []string{"fooB.bar:80"}); err != nil {
		t.Fatal(err)
	}
	// Verify the requested listener resources are added to the node snapshot
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	// Close one of the streams (id:1)
	streamID = int64(1)
	snapshotter.deleteNodeStream(nodeID, streamID)
	// Verify that we still have a snapshot in cache but with less resources
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	// Close the 2nd stream (id:2) and verify we do not keep a snap for the node
	streamID = int64(2)
	snapshotter.deleteNodeStream(nodeID, streamID)
	_, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	assert.Equal(t, fmt.Errorf("no snapshot found for node %s", nodeID), err)
}

func TestSnapServices_SingleServiceWithAuthoritySet(t *testing.T) {
	snapshotter := NewSnapshotter("test-authority", uint(0), float64(0), float64(0), false)
	serviceStore := NewXdsServiceStore()
	svc := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "test",
					Port: int32(80),
				}},
		},
	}
	serviceStore.AddOrUpdate(svc, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	snapshotter.SnapServices(serviceStore)
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will have a 2 listeners, 2 routes and
	// 2 clusters
	expectedListeners := []string{makeListenerName("foo", "bar", int32(80)), makeXdstpListenerName("foo", "bar", "test-authority", int32(80))}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	for _, res := range snap.GetResources(resource.ListenerType) {
		listener, err := UnmarshalResourceToListener(res)
		if err != nil {
			t.Fatal(err)
		}
		assert.Contains(t, expectedListeners, listener.Name)
	}
	expectedClusters := []string{makeClusterName("foo", "bar", int32(80)), makeXdstpClusterName("foo", "bar", "test-authority", int32(80))}
	assert.Equal(t, 2, len(snap.GetResources(resource.ClusterType)))
	for _, res := range snap.GetResources(resource.ClusterType) {
		cluster, err := UnmarshalResourceToCluster(res)
		if err != nil {
			t.Fatal(err)
		}
		assert.Contains(t, expectedClusters, cluster.Name)
	}
	expectedRoutes := []string{
		makeRouteConfigName("foo", "bar", int32(80)),
		makeXdstpRouteConfigName("foo", "bar", "test-authority", int32(80)),
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.RouteType)))
	for _, res := range snap.GetResources(resource.RouteType) {
		route, err := UnmarshalResourceToRouteConfiguration(res)
		if err != nil {
			t.Fatal(err)
		}
		assert.Contains(t, expectedRoutes, route.Name)
	}

}
