package xds

import (
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter := NewSnapshotter(uint(0), float64(0), float64(0))
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
	snapshotter.addNewNode(nodeID)
	// Request all listener resources for the node - Update the node snapshot
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80", "fooB.bar:80"}))
	if err := snapshotter.updateNodeSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80", "fooB.bar:80"}); err != nil {
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
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80"}))
	if err := snapshotter.updateNodeSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80"}); err != nil {
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
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80", "fooB.bar:80"}))
	if err := snapshotter.updateNodeSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80", "fooB.bar:80"}); err != nil {
		t.Fatal(err)
	}
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	// Requesting non existing resources should leave the list unaffected
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80", "fooB.bar:80", "fooC.bar:80"}))
	if err := snapshotter.updateNodeSnapshot(nodeID, resource.ListenerType, []string{"fooA.bar:80", "fooB.bar:80", "fooC.bar:80"}); err != nil {
		t.Fatal(err)
	}
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 2, len(snap.GetResources(resource.ListenerType)))
	// Requesting an empty list of resources package
	assert.Equal(t, true, snapshotter.needToUpdateSnapshot(nodeID, resource.ListenerType, []string{""}))
	if err := snapshotter.updateNodeSnapshot(nodeID, resource.ListenerType, []string{""}); err != nil {
		t.Fatal(err)
	}
	snap, err = snapshotter.servicesCache.GetSnapshot(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 0, len(snap.GetResources(resource.ListenerType)))
}
