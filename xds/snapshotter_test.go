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
	LbPolicyLabel = "xds.semaphore.uw.systems/lb-policy"
}

func TestSnapServices_EmptyServiceList(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{})
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
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{&v1.Service{
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
	}})
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
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		}}})
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
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "http",
					Port: int32(80),
				},
				v1.ServicePort{
					Name: "https",
					Port: int32(443),
				}},
		},
	}})
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

func TestSnapServices_DefaultLbPolicy(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "http",
					Port: int32(80),
				},
			}},
	}})
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will have one cluster configured with
	// round robin policy
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	}
}

func TestSnapServices_InvalidLbPolicy(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Labels:    map[string]string{LbPolicyLabel: "invalid"},
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "http",
					Port: int32(80),
				},
			}},
	}})
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will have one cluster configured with
	// round robin policy
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	}
}

func TestSnapServices_SetLbPolicy(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapServices([]*v1.Service{&v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
			Labels:    map[string]string{LbPolicyLabel: "ring_hash"},
		},
		Spec: v1.ServiceSpec{
			Ports: []v1.ServicePort{
				v1.ServicePort{
					Name: "http",
					Port: int32(80),
				},
			}},
	}})
	snap, err := snapshotter.servicesCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will have one cluster configured with
	// round robin policy
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_RING_HASH, cluster.LbPolicy)
	}
}

func TestSnapEndpoints_EmptyEndpointsList(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
	snapshotter.SnapEndpoints([]*discoveryv1.EndpointSlice{})
	snap, err := snapshotter.endpointsCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will be empty
	assert.Equal(t, 0, len(snap.GetResources(resource.EndpointType)))
}

func TestSnapEndpoints_MissingServiceOwnershipLabel(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
	log.InitLogger("test-semaphore-xds", "debug")
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo",
			Namespace: "bar",
		}}
	snapshotter.SnapEndpoints([]*discoveryv1.EndpointSlice{endpointSlice})
	snap, err := snapshotter.endpointsCache.GetSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	// Verify that our snapshot will be empty
	assert.Equal(t, 0, len(snap.GetResources(resource.EndpointType)))
}

func TestSnapEndpoints_UpdateAddress(t *testing.T) {
	snapshotter := NewSnapshotter(uint(0))
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
				EndpointSliceServiceLabel: "foobar",
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
	snapshotter.SnapEndpoints([]*discoveryv1.EndpointSlice{endpointSlice})
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
	}
	// Update address and check reflection on snapshot
	endpointSlice.Endpoints[0].Addresses = []string{"10.2.2.2"}
	snapshotter.SnapEndpoints([]*discoveryv1.EndpointSlice{endpointSlice})
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
	}
}
