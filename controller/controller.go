package controller

import (
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/utilitywarehouse/semaphore-xds/kube"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

type Controller struct {
	snapshotter          *xds.Snapshotter
	serviceQueue         *queue
	serviceWatcher       *kube.ServiceWatcher
	endpointSliceQueue   *queue
	endpointSliceWatcher *kube.EndpointSliceWatcher
}

func NewController(client kubernetes.Interface, namespace, labelselector string, s *xds.Snapshotter, resyncPeriod time.Duration) *Controller {
	controller := &Controller{
		snapshotter: s,
	}

	controller.serviceQueue = newQueue("service", controller.reconcileServices)
	controller.endpointSliceQueue = newQueue("endpointSlice", controller.reconcileEndpointSlices)

	controller.serviceWatcher = kube.NewServiceWatcher(client, resyncPeriod, controller.serviceEventHandler, labelselector, namespace)
	controller.serviceWatcher.Init()
	controller.endpointSliceWatcher = kube.NewEndpointSliceWatcher(client, resyncPeriod, controller.endpointSliceEventHandler, labelselector, namespace)
	controller.endpointSliceWatcher.Init()

	return controller
}

func (c *Controller) Run() error {
	go c.serviceWatcher.Run()
	go c.endpointSliceWatcher.Run()

	stopCh := make(chan struct{})
	if ok := cache.WaitForNamedCacheSync("serviceWatcher", stopCh, c.serviceWatcher.HasSynced); !ok {
		return fmt.Errorf("failed to wait for service caches to sync")
	}
	if ok := cache.WaitForNamedCacheSync("endpointSliceWatcher", stopCh, c.endpointSliceWatcher.HasSynced); !ok {
		return fmt.Errorf("failed to wait for endpointSlice caches to sync")
	}

	go c.serviceQueue.Run()
	go c.endpointSliceQueue.Run()

	return nil
}

func (c *Controller) Stop() {
	c.serviceWatcher.Stop()
	c.serviceQueue.Stop()
	c.endpointSliceWatcher.Stop()
	c.endpointSliceQueue.Stop()
}

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

func (c *Controller) endpointSliceEventHandler(eventType watch.EventType, old *discoveryv1.EndpointSlice, new *discoveryv1.EndpointSlice) {
	switch eventType {
	case watch.Added:
		log.Logger.Debug("endpoints added", "namespace", new.Namespace, "name", new.Name)
		c.endpointSliceQueue.Add(new)
	case watch.Modified:
		log.Logger.Debug("endpoints modified", "namespace", new.Namespace, "name", new.Name)
		c.endpointSliceQueue.Add(new)
	case watch.Deleted:
		log.Logger.Debug("endpoints deleted", "namespace", old.Namespace, "name", old.Name)
		c.endpointSliceQueue.Add(old)
	default:
		log.Logger.Info("Unknown endpoints event received: %v", eventType)
	}
}

func (c *Controller) reconcileServices() error {
	return c.snapshotter.SnapServices(c.serviceWatcher)
}

func (c *Controller) reconcileEndpointSlices() error {
	return c.snapshotter.SnapEndpoints(c.endpointSliceWatcher)
}
