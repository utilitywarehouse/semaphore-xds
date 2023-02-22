package controller

import (
	"fmt"
	"time"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	kubeerror "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/tools/cache"
	"k8s.io/utils/pointer"

	"github.com/utilitywarehouse/semaphore-xds/backoff"
	"github.com/utilitywarehouse/semaphore-xds/kube"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/queue"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

// LbPolicyLabel is the label used to specify load balancing policy for the
// generated clusters per Kubernetes Service
var LbPolicyLabel string

type Controller struct {
	localClient              kube.Client
	remoteClients            []kube.Client
	snapshotter              *xds.Snapshotter
	serviceQueue             *queue.Queue
	serviceWatcher           *kube.ServiceWatcher
	localEndpointSliceQueue  *queue.Queue
	remoteEndpointSliceQueue *queue.Queue
	crdQueue                 *queue.Queue
}

func NewController(localClient kube.Client, remoteClients []kube.Client, namespace, labelselector string, s *xds.Snapshotter, resyncPeriod time.Duration) *Controller {
	controller := &Controller{
		localClient:   localClient,
		remoteClients: remoteClients,
		snapshotter:   s,
	}

	controller.serviceQueue = queue.NewQueue("service", controller.reconcileServices)
	controller.localEndpointSliceQueue = queue.NewQueue("localEndpointSlice", controller.reconcileLocalEndpointSlice)
	controller.remoteEndpointSliceQueue = queue.NewQueue("remoteEndpointSlice", controller.reconcileRemoteEndpointSlice)
	controller.crdQueue = queue.NewQueue("crd", controller.reconcileServices)

	// Legacy service watcher on label selector
	controller.serviceWatcher = kube.NewServiceWatcher(localClient.KubeClient(), resyncPeriod, controller.serviceEventHandler, labelselector, namespace)
	controller.serviceWatcher.Init()

	return controller
}

func (c *Controller) Run() error {
	// Legacy service watcher on label selector
	go c.serviceWatcher.Run()
	stopCh := make(chan struct{})
	if ok := cache.WaitForNamedCacheSync("serviceWatcher", stopCh, c.serviceWatcher.HasSynced); !ok {
		return fmt.Errorf("failed to wait for service caches to sync")
	}
	c.localClient.WatchAll(c.crdQueue, c.localEndpointSliceQueue, stopCh)
	for _, client := range c.remoteClients {
		// Do not block if a remote client is not able to start watching EndpointSlices
		go backoff.RetryWatch(client.WatchEndpointSlices, c.remoteEndpointSliceQueue, stopCh, "watching remote EndpointSlices")
	}

	go c.serviceQueue.Run()
	go c.localEndpointSliceQueue.Run()
	go c.remoteEndpointSliceQueue.Run()
	go c.crdQueue.Run()

	return nil
}

func (c *Controller) Stop() {
	c.serviceWatcher.Stop()
	c.serviceQueue.Stop()
	c.crdQueue.Stop()
	c.localEndpointSliceQueue.Stop()
	c.remoteEndpointSliceQueue.Stop()
}

// serviceEventHandler is the handler for the "legacy" service watcher
func (c *Controller) serviceEventHandler(eventType watch.EventType, old *v1.Service, new *v1.Service) {
	switch eventType {
	case watch.Added:
		log.Logger.Debug("service added", "namespace", new.Namespace, "name", new.Name)
		c.serviceQueue.Add(new)
	case watch.Modified:
		log.Logger.Debug("service modified", "namespace", new.Namespace, "name", new.Name)
		c.serviceQueue.Add(new)
	case watch.Deleted:
		log.Logger.Debug("service deleted", "namespace", old.Namespace, "name", old.Name)
		c.serviceQueue.Add(old)
	default:
		log.Logger.Info("Unknown service event received: %v", eventType)
	}
}

func (c *Controller) reconcileServices(name, namespace string) error {
	svcs, err := c.servicesToXdsServiceStore()
	if err != nil {
		return err
	}
	// We need to snap both Services and EndpointSlices to get all xds
	// server config changes
	return c.snapAll(svcs)
}

func (c *Controller) reconcileLocalEndpointSlice(name, namespace string) error {
	endpointSlice, err := c.localClient.EndpointSlice(name, namespace)
	// If the EndpointSlice is not found assume it is deleted and refresh
	// Endpoints snapshot to make sure it's up to date
	if kubeerror.IsNotFound(err) {
		log.Logger.Info("Endpoint slice not found (probably deleted), refreshing snapshot")
		return c.snapEndpoints()
	}
	if err != nil {
		return fmt.Errorf("Failed to get EndpointSlice: %s in namespace %s: %v", name, namespace, err)
	}
	return c.reconcileEndpointSlice(endpointSlice)
}

func (c *Controller) reconcileRemoteEndpointSlice(name, namespace string) error {
	for _, client := range c.remoteClients {
		endpointSlice, err := client.EndpointSlice(name, namespace)
		if kubeerror.IsNotFound(err) {
			log.Logger.Debug("EndpointSlice not found in remote cluster", "name", name, "namespace", namespace)
			continue
		}
		if err != nil { // If we error getting from a remote continue to the rest instead of requeuing
			log.Logger.Error("Failed to get remote EndpointSlice", "name", name, "namespace", namespace, "error", err)
			continue
		}
		return c.reconcileEndpointSlice(endpointSlice)
	}
	// EndpointSlice not found in remote clients - deleted
	return c.snapEndpoints()
}

func (c *Controller) reconcileEndpointSlice(endpointSlice *discoveryv1.EndpointSlice) error {
	svcs, err := c.servicesToXdsServiceStore()
	if err != nil {
		return err
	}
	if needToReconcileEndpointSlice(endpointSlice, svcs) {
		return c.snapAll(svcs)
	}
	return nil
}

// snapAll Snapshots all resources related to the passed Service map
func (c *Controller) snapAll(store xds.XdsServiceStore) error {
	if err := c.snapshotter.SnapServices(store); err != nil {
		log.Logger.Warn("Failed to snap Services", "error", err)
		return err
	}
	endpointStore, err := c.endpointsStoreForXdsServiceStore(store)
	if err != nil {
		return fmt.Errorf("Cannot list EndpointSlices for snapshotting: %v", err)
	}
	return c.snapshotter.SnapEndpoints(endpointStore)
}

// snapEndpoints will attempt to snap all endpointSlices from the passed
// serices store
func (c *Controller) snapEndpoints() error {
	svcs, err := c.servicesToXdsServiceStore()
	if err != nil {
		return err
	}
	endpointStore, err := c.endpointsStoreForXdsServiceStore(svcs)
	if err != nil {
		return fmt.Errorf("Cannot list EndpointSlices for snapshotting: %v", err)
	}
	return c.snapshotter.SnapEndpoints(endpointStore)
}

// servicesToXdsServiceStore iterates through the (legacy) labelled Services and
// XdsServices to populate and return a new XdsServiceStore
func (c *Controller) servicesToXdsServiceStore() (xds.XdsServiceStore, error) {
	store := xds.NewXdsServiceStore()
	labelledServices, err := c.serviceWatcher.List()
	if err != nil {
		return store, fmt.Errorf("Failed to list Services from watcher: %v", err)
	}
	for _, svc := range labelledServices {
		store.AddOrUpdate(svc, extractClusterLbPolicyFromServiceLabel(svc), false, false)
	}
	xdsSvcs, err := c.localClient.XdsServiceList()
	if err != nil {
		return store, fmt.Errorf("Failed to list XdsServices from watcher: %v", err)
	}
	for _, xdsSvc := range xdsSvcs {
		svc, err := c.localClient.Service(xdsSvc.Spec.Service.Name, xdsSvc.Namespace)
		if err != nil {
			log.Logger.Warn("Service not found", "service", xdsSvc.Spec.Service.Name, "namespace", xdsSvc.Namespace, "error", err)
			continue
		}
		policy := xds.ParseToClusterLbPolicy(xdsSvc.Spec.LoadBalancing.Policy)
		store.AddOrUpdate(svc, policy, pointer.BoolPtrDerefOr(xdsSvc.Spec.AllowRemoteEndpoints, false), pointer.BoolPtrDerefOr(xdsSvc.Spec.PrioritizeLocalEndpoints, false))
	}
	return store, nil
}

// endpointSlicesForServiceMap calculates a ServiceEndpointStore from the
// objects in the passed XdsServiceStore. It also calculates priorities if
// PrioritizeLocalEndpoints is set. Priorities should range from 0 (highest)
// to N (lowest) without skipping, so we should always use 0 for the local
// endpoints and 0 or 1 for remote endoints depending whether we want them as
// equals or fallback targets. Under usual circumstances only endpoints with the
// highest priority will be selected (unless all the highest targets are deemed
// unreachable by the clients)
// https://pkg.go.dev/github.com/envoyproxy/go-control-plane/envoy/config/endpoint/v3#LocalityLbEndpoints
func (c *Controller) endpointsStoreForXdsServiceStore(svcs xds.XdsServiceStore) (xds.XdsEndpointStore, error) {
	store := xds.NewXdsEnpointStore()
	for _, s := range svcs.All() {
		// Add EndpointSlices from the local cluster
		es, err := c.localClient.EndpointSliceList(fmt.Sprintf("%s=%s", kube.KubernetesIOServiceNameLabel, s.Service.Name))
		if err != nil {
			return nil, err
		}
		foundLocalEndpoints := false
		for _, e := range es {
			if e.Endpoints != nil && len(e.Endpoints) > 0 {
				store.Add(s.Service.Name, s.Service.Namespace, e, uint32(0)) // default 0 priority for all local endpoints
				foundLocalEndpoints = true
			}
		}
		// Add EndpointSlices from remote clusters if allowed
		if s.AllowRemoteEndpoints {
			priority := uint32(0)
			if s.PrioritizeLocalEndpoints && foundLocalEndpoints {
				priority = uint32(1)
			}
			for _, client := range c.remoteClients {
				es, err := client.EndpointSliceList(fmt.Sprintf("%s=%s", kube.KubernetesIOServiceNameLabel, s.Service.Name))
				if err != nil {
					return nil, err
				}
				for _, e := range es {
					store.Add(s.Service.Name, s.Service.Namespace, e, priority)
				}
			}
		}

	}
	return store, nil
}

// serviceKey concatenates a name and a namespace into a single string
func serviceKey(name, namespace string) string {
	return fmt.Sprintf("%s.%s", name, namespace)
}

// needToReconcileEndpointSlice returns true if we need to reconcile an
// EndpointSlice
func needToReconcileEndpointSlice(endpointSlice *discoveryv1.EndpointSlice, store xds.XdsServiceStore) bool {
	var parentSvcName string
	var ok bool
	if parentSvcName, ok = endpointSlice.Labels[kube.KubernetesIOServiceNameLabel]; !ok {
		log.Logger.Warn("Did not find parent service for EndpointSlice %s in namespace %s, skipping", endpointSlice.Name, endpointSlice.Namespace)
		return false
	}
	return isServiceInXdsServiceStore(parentSvcName, endpointSlice.Namespace, store)
}

func isServiceInXdsServiceStore(name, namespace string, store xds.XdsServiceStore) bool {
	_, err := store.Get(name, namespace)
	if err != nil {
		return false
	}
	return true
}

func extractClusterLbPolicyFromServiceLabel(service *v1.Service) clusterv3.Cluster_LbPolicy {
	lbPolicyRaw, ok := service.Labels[LbPolicyLabel]
	if !ok {
		log.Logger.Debug("No load balancing policy defined for service, defaulting to round robin", "service", service.Name)
		return clusterv3.Cluster_ROUND_ROBIN
	}
	return xds.ParseToClusterLbPolicy(lbPolicyRaw)
}
