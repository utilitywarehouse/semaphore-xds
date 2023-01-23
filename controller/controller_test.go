package controller

import (
	"fmt"
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	resource "github.com/envoyproxy/go-control-plane/pkg/resource/v3"
	"github.com/stretchr/testify/assert"
	kubeerror "k8s.io/apimachinery/pkg/api/errors"
	schema "k8s.io/apimachinery/pkg/runtime/schema"

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

func TestReconcileEndpointSlices_SnapOnUpdate(t *testing.T) {
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
	// Reconciling an existing EndpointSlice should make sure to refresh the
	// services and endpoints snaps
	controller.reconcileEndpointSlices("grpc-echo-server-628fr", "labs")
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.ListenerType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.ClusterType)))
	assert.Equal(t, 1, len(snap.GetResources(resource.RouteType)))
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.EndpointType)))
}

func TestReconcileEndpointSlices_NotFound(t *testing.T) {
	client := kube.NewClientMock(
		"./test-resources/xds_service.yaml",
		"./test-resources/endpointslice.yaml",
	)
	client.EndpointSliceApiError(kubeerror.NewNotFound(schema.GroupResource{Resource: "endpointslice"}, "foo"))
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
	// If the EndpointSlice is not found the controller should assume it is
	// deleted and refresh the Endpoints snapshot just in case.
	controller.reconcileEndpointSlices("foo", "bar")
	snap, err := snapshotter.ServicesSnapshot(testNodeID)
	assert.Equal(t, fmt.Errorf("no snapshot found for node %s", testNodeID), err)
	snap, err = snapshotter.EndpointsSnapshot(testNodeID)
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, 1, len(snap.GetResources(resource.EndpointType)))
}

func TestReconcileEndpointSlices_NonXdsService(t *testing.T) {
	client := kube.NewClientMock(
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
	// Reconciling an existing EndpointSlice not belonging to an xDS service
	// should not do anything
	controller.reconcileEndpointSlices("grpc-echo-server-628fr", "labs")
	_, err := snapshotter.ServicesSnapshot(testNodeID)
	assert.Equal(t, fmt.Errorf("no snapshot found for node %s", testNodeID), err)
	_, err = snapshotter.EndpointsSnapshot(testNodeID)
	assert.Equal(t, fmt.Errorf("no snapshot found for node %s", testNodeID), err)
}
