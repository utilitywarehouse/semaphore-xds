package kube

import (
	"fmt"
	"io"
	"time"

	"crypto/tls"
	"crypto/x509"
	"io/ioutil"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	kubeerror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	// in case of local kube config
	_ "k8s.io/client-go/plugin/pkg/client/auth/oidc"

	"github.com/utilitywarehouse/semaphore-xds/apis/generated/clientset/versioned"
	"github.com/utilitywarehouse/semaphore-xds/apis/generated/informers/externalversions"
	"github.com/utilitywarehouse/semaphore-xds/apis/semaphorexds/v1alpha1"
	"github.com/utilitywarehouse/semaphore-xds/queue"
)

const (
	resyncPeriod = 10 * time.Minute

	KubernetesIOServiceNameLabel = "kubernetes.io/service-name"
)

type certMan struct {
	caURL string
}

func (cm *certMan) verifyConn(cs tls.ConnectionState) error {
	resp, err := http.Get(cm.caURL)
	if err != nil {
		return fmt.Errorf("error getting remote CA from %s: %v", cm.caURL, err)
	}
	defer func() {
		io.Copy(ioutil.Discard, resp.Body)
		resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("expected %d response from %s, got %d", http.StatusOK, cm.caURL, resp.StatusCode)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("error reading response body from %s: %v", cm.caURL, err)
	}
	roots := x509.NewCertPool()
	ok := roots.AppendCertsFromPEM(body)
	if !ok {
		return fmt.Errorf("failed to parse root certificate from %s", cm.caURL)
	}
	opts := x509.VerifyOptions{
		DNSName: cs.ServerName,
		Roots:   roots,
	}
	_, err = cs.PeerCertificates[0].Verify(opts)
	return err
}

// ClientFromConfig returns a Kubernetes client (clientset) from the kubeconfig
// path or from the in-cluster service account environment.
func ClientFromConfig(path string) (*kubernetes.Clientset, error) {
	conf, err := getClientConfig(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes client config: %v", err)
	}
	return kubernetes.NewForConfig(conf)
}

// getClientConfig returns a Kubernetes client Config.
func getClientConfig(path string) (*rest.Config, error) {
	if path != "" {
		// build Config from a kubeconfig filepath
		return clientcmd.BuildConfigFromFlags("", path)
	}
	// uses pod's service account to get a Config
	return rest.InClusterConfig()
}

// Client interface for a Kubernetes config. Will be used to  watch all needed
// resources and update the stores.
type Client interface {
	WatchAll(crdQ, endpointSliceQ *queue.Queue, stopCh <-chan struct{}) error
	WatchEndpointSlices(endpointSliceQ *queue.Queue, stopCh <-chan struct{}) error
	KubeClient() kubernetes.Interface
	Service(namespace, name string) (*corev1.Service, error)
	EndpointSlice(name, namespace string) (*discoveryv1.EndpointSlice, error)
	EndpointSliceList(labelSelector, namespace string) ([]*discoveryv1.EndpointSlice, error)
	XdsServiceList() ([]*v1alpha1.XdsService, error)
}

type clientWrapper struct {
	clientsetCRD  versioned.Interface
	clientsetKube kubernetes.Interface
	factoryCRD    externalversions.SharedInformerFactory
	factoryKube   informers.SharedInformerFactory
}

// NewClientFromConfig returns a client from the given kubeconfig path or from
// the in-cluster service account environment.
func NewClientFromConfig(path string) (*clientWrapper, error) {
	conf, err := getClientConfig(path)
	if err != nil {
		return nil, fmt.Errorf("failed to get Kubernetes client config: %v", err)
	}
	csKube, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %v", err)
	}
	csCRD, err := versioned.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("failed to create CRD client: %v", err)
	}
	return &clientWrapper{
		clientsetCRD:  csCRD,
		clientsetKube: csKube,
	}, nil
}

func NewClient(token, apiURL, caURL string) (*clientWrapper, error) {
	cm := &certMan{caURL}
	conf := &rest.Config{
		Host: apiURL,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				VerifyConnection:   cm.verifyConn}},
		BearerToken: token,
	}
	csKube, err := kubernetes.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("failed to create kube client: %v", err)
	}
	csCRD, err := versioned.NewForConfig(conf)
	if err != nil {
		return nil, fmt.Errorf("failed to create CRD client: %v", err)
	}
	return &clientWrapper{
		clientsetCRD:  csCRD,
		clientsetKube: csKube,
	}, nil
}

func (c *clientWrapper) WatchAll(crdQ, endpointSliceQ *queue.Queue, stopCh <-chan struct{}) error {
	factoryCRD := externalversions.NewSharedInformerFactoryWithOptions(c.clientsetCRD, resyncPeriod, externalversions.WithNamespace(metav1.NamespaceAll))
	factoryCRD.Semaphorexds().V1alpha1().XdsServices().Informer().AddEventHandler(newEventHandlerFunc(crdQ))
	c.factoryCRD = factoryCRD

	factoryKube := informers.NewSharedInformerFactoryWithOptions(c.clientsetKube, resyncPeriod, informers.WithNamespace(metav1.NamespaceAll))
	factoryKube.Discovery().V1().EndpointSlices().Informer().AddEventHandler(newEventHandlerFunc(endpointSliceQ))
	factoryKube.Core().V1().Services().Informer() // Create the Services informer without any event handling to update the stores
	c.factoryKube = factoryKube

	c.factoryCRD.Start(stopCh)
	c.factoryKube.Start(stopCh)
	for typ, ok := range c.factoryCRD.WaitForCacheSync(stopCh) {
		if !ok {
			return fmt.Errorf("timed out waiting for controller caches to sync %s", typ)
		}
	}
	for typ, ok := range c.factoryKube.WaitForCacheSync(stopCh) {
		if !ok {
			return fmt.Errorf("timed out waiting for controller caches to sync %s", typ)
		}
	}
	return nil
}

func (c *clientWrapper) WatchEndpointSlices(endpointSliceQ *queue.Queue, stopCh <-chan struct{}) error {
	factoryKube := informers.NewSharedInformerFactoryWithOptions(c.clientsetKube, resyncPeriod, informers.WithNamespace(metav1.NamespaceAll))
	factoryKube.Discovery().V1().EndpointSlices().Informer().AddEventHandler(newEventHandlerFunc(endpointSliceQ))
	c.factoryKube = factoryKube

	c.factoryKube.Start(stopCh)
	for typ, ok := range c.factoryKube.WaitForCacheSync(stopCh) {
		if !ok {
			return fmt.Errorf("timed out waiting for controller caches to sync %s", typ)
		}
	}
	return nil
}

// KubeClient exports the kubernetes client interface
func (c *clientWrapper) KubeClient() kubernetes.Interface {
	return c.clientsetKube
}

// Service returns the named service from the given namespace.
func (c *clientWrapper) Service(name, namespace string) (*corev1.Service, error) {
	return c.factoryKube.Core().V1().Services().Lister().Services(namespace).Get(name)
}

// EndpointSlice returns the named EndpointSlice from the given namespace.
func (c *clientWrapper) EndpointSlice(name, namespace string) (*discoveryv1.EndpointSlice, error) {
	// Return a not found error if watcher is not initialised yet (for remote watchers)
	if c.factoryKube == nil {
		return &discoveryv1.EndpointSlice{}, kubeerror.NewNotFound(schema.GroupResource{Resource: "endpointslice"}, name)
	}
	return c.factoryKube.Discovery().V1().EndpointSlices().Lister().EndpointSlices(namespace).Get(name)
}

// EndpointSliceList returns all the EndpointSlices selected by a label
func (c *clientWrapper) EndpointSliceList(labelSelector, namespace string) ([]*discoveryv1.EndpointSlice, error) {
	// Return an empty slice if watcher is not initialised yet (for remote watchers)
	if c.factoryKube == nil {
		return []*discoveryv1.EndpointSlice{}, nil
	}
	selector, err := labels.Parse(labelSelector)
	if err != nil {
		return []*discoveryv1.EndpointSlice{}, err
	}
	return filterKubeNotFound(c.factoryKube.Discovery().V1().EndpointSlices().Lister().EndpointSlices(namespace).List(selector))
}

// XdsServiceList lists the watched XdsServices resources
func (c *clientWrapper) XdsServiceList() ([]*v1alpha1.XdsService, error) {
	return c.factoryCRD.Semaphorexds().V1alpha1().XdsServices().Lister().XdsServices(metav1.NamespaceAll).List(labels.Everything())
}

func newEventHandlerFunc(q *queue.Queue) cache.ResourceEventHandlerFuncs {
	return cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			q.Add(obj)
		},
		UpdateFunc: func(oldObj, newObj interface{}) {
			if objChanged(oldObj, newObj) {
				q.Add(newObj)
			}
		},
		DeleteFunc: func(obj interface{}) {
			q.Add(obj)
		},
	}
}

func objChanged(oldObj, newObj interface{}) bool {
	if oldObj == nil || newObj == nil {
		return true
	}
	if oldObj.(metav1.Object).GetResourceVersion() == newObj.(metav1.Object).GetResourceVersion() {
		return false
	}
	return true
}

// filterKubeNotFound will swallow not found errors and make sure to return an
// empty list
func filterKubeNotFound(es []*discoveryv1.EndpointSlice, err error) ([]*discoveryv1.EndpointSlice, error) {
	if kubeerror.IsNotFound(err) {
		return []*discoveryv1.EndpointSlice{}, nil
	}
	return es, err
}
