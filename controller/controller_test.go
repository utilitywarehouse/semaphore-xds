package controller

import (
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"

	"github.com/utilitywarehouse/semaphore-xds/apis/semaphorexds/v1alpha1"
	"github.com/utilitywarehouse/semaphore-xds/kube"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/utilitywarehouse/semaphore-xds/xds"
)

var (
	testSnapshotterListenPort = uint(8080)
	testNodeID                = ""
	testLabelSelector         = "xds.semaphore.uw.systems/enabled=true"
)

func init() {
	log.InitLogger("test-semaphore-xds", "debug")
	LbPolicyLabel = "xds.semaphore.uw.systems/lb-policy"
	// required in order to be able to parse XdsService manifersts
	err := v1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		panic(err)
	}
}

func TestReconcileServices_LabelledService(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/labelled_service.yaml",
		"./test-resources/endpointslice.yaml",
	)
	snapshotter := xds.NewSnapshotter(testSnapshotterListenPort)
	controller := NewController(
		client,
		"",
		testLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()
	defer controller.Stop()
	// Reconciling any service should trigger a full snap, since this is only to be called on XdsService or labelled Service Updates
	controller.reconcileServices("foo", "bar")
	// Verify that our snapshot will have a single listener, route and
	// cluster
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
	// Verify the default round robin policy is set on the clusters
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := xds.UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	}
	// Verify we will have one Endpoint respurce in the snapshot
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.EndpointType)))
}

func TestReconcileServices_LabelledServiceLbPolicy(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/labelled_service_ring_hash_balancer.yaml",
		"./test-resources/endpointslice.yaml",
	)
	snapshotter := xds.NewSnapshotter(testSnapshotterListenPort)
	controller := NewController(
		client,
		"",
		testLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()
	defer controller.Stop()
	// Reconciling any service should trigger a full snap, since this is only to be called on XdsService or labelled Service Updates
	controller.reconcileServices("foo", "bar")
	// Verify that our snapshot will have a single listener, route and
	// cluster
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
	// Verify the correct lb policy (ring hash) is set on the clusters
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := xds.UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_RING_HASH, cluster.LbPolicy)
	}
}

func TestReconcileServices_LabelledServiceInvalidLbPolicy(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/labelled_service_invalid_balancer.yaml",
		"./test-resources/endpointslice.yaml",
	)
	snapshotter := xds.NewSnapshotter(testSnapshotterListenPort)
	controller := NewController(
		client,
		"",
		testLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()
	defer controller.Stop()
	// Reconciling any service should trigger a full snap, since this is only to be called on XdsService or labelled Service Updates
	controller.reconcileServices("foo", "bar")
	// Verify that our snapshot will have a single listener, route and
	// cluster
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
	// Verify the default round robin policy is set on the clusters
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := xds.UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	}
}

func TestReconcileServices_XdsService(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/xds_service.yaml",
		"./test-resources/endpointslice.yaml",
	)
	snapshotter := xds.NewSnapshotter(testSnapshotterListenPort)
	controller := NewController(
		client,
		"",
		testLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()
	defer controller.Stop()
	// Reconciling any service should trigger a full snap, since this is only to be called on XdsService or labelled Service Updates
	controller.reconcileServices("foo", "bar")
	// Verify that our snapshot will have a single listener, route and
	// cluster
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
	// Verify the default round robin policy is set on the clusters
	for _, cl := range snap.GetResources(resource.ClusterType) {
		cluster, err := xds.UnmarshalResourceToCluster(cl)
		if err != nil {
			t.Fatal(err)
		}
		assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, cluster.LbPolicy)
	}
	// Verify we will have one Endpoint respurce in the snapshot
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.EndpointType)))
}

func TestReconcileServices_XdsServiceNotExistent(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/xds_service_not_existent.yaml",
		"./test-resources/endpointslice.yaml",
	)
	snapshotter := xds.NewSnapshotter(testSnapshotterListenPort)
	controller := NewController(
		client,
		"",
		testLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()
	defer controller.Stop()
	// Reconciling any service should trigger a full snap, since this is only to be called on XdsService or labelled Service Updates
	controller.reconcileServices("foo", "bar")
	// Verify that our snapshot will have a single listener, route and
	// cluster
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 0, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 0, len(snap.GetResources(resource.EndpointType)))
}

func TestReconcileServices_XdsServiceDelete(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/xds_service.yaml",
		"./test-resources/endpointslice.yaml",
	)
	snapshotter := xds.NewSnapshotter(testSnapshotterListenPort)
	controller := NewController(
		client,
		"",
		testLabelSelector,
		snapshotter,
		0,
	)
	controller.Run()
	defer controller.Stop()
	// Reconciling any service should trigger a full snap, since this is only to be called on XdsService or labelled Service Updates
	controller.reconcileServices("foo", "bar")
	// Verify that our snapshot will have a single listener, route and
	// cluster
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
	// Verify we will have one Endpoint respurce in the snapshot
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.EndpointType)))
	// flush client resources and check that we should have 0 resources in
	// the snapshot
	client.Clear()
	controller.reconcileServices("foo", "bar")
	snap, err = snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 0, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 0, len(snap.GetResources(resource.RouteType)))
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 0, len(snap.GetResources(resource.EndpointType)))
}
