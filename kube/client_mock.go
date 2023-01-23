package kube

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"

	"github.com/utilitywarehouse/semaphore-xds/apis/semaphorexds/v1alpha1"
	"github.com/utilitywarehouse/semaphore-xds/log"
	"github.com/utilitywarehouse/semaphore-xds/queue"
)

type clientMock struct {
	fakeClientset kubernetes.Interface

	services       []*corev1.Service
	endpointSlices []*discoveryv1.EndpointSlice
	xdsServices    []*v1alpha1.XdsService

	apiServiceError        error
	apiEndpointsSliceError error
	apiXdsServiceError     error
}

// mustParseYaml parses a YAML to objects.
func mustParseYaml(content []byte) []runtime.Object {
	acceptedK8sTypes := regexp.MustCompile(`^(EndpointSlice|Service|XdsService)$`)

	files := strings.Split(string(content), "---\n")
	retVal := make([]runtime.Object, 0, len(files))
	for _, file := range files {
		if file == "\n" || file == "" {
			continue
		}

		decode := scheme.Codecs.UniversalDeserializer().Decode
		obj, groupVersionKind, err := decode([]byte(file), nil, nil)
		if err != nil {
			panic(fmt.Sprintf("Error while decoding YAML object. Err was: %s", err))
		}

		if !acceptedK8sTypes.MatchString(groupVersionKind.Kind) {
			log.Logger.Debug("The yaml contained K8s object types which are not supported! Skipping object", "type", groupVersionKind.Kind)
		} else {
			retVal = append(retVal, obj)
		}
	}
	return retVal
}

// NewClientMock creates a new mock client and loads Kubernetes resources from
// the passed yaml files
func NewClientMock(paths ...string) *clientMock {
	c := &clientMock{}

	c.fakeClientset = fake.NewSimpleClientset()
	for _, path := range paths {
		yamlContent, err := os.ReadFile(filepath.FromSlash(path))
		if err != nil {
			panic(err)
		}

		k8sObjects := mustParseYaml(yamlContent)
		for _, obj := range k8sObjects {
			switch o := obj.(type) {
			case *corev1.Service:
				c.services = append(c.services, o)
				c.fakeClientset.CoreV1().Services(o.Namespace).Create(context.Background(), o, metav1.CreateOptions{}) // Add Services to the legacy watcher client too
			case *discoveryv1.EndpointSlice:
				c.endpointSlices = append(c.endpointSlices, o)
			case *v1alpha1.XdsService:
				c.xdsServices = append(c.xdsServices, o)
			default:
				panic(fmt.Sprintf("Unknown runtime object %+v %T", o, o))
			}
		}
	}
	return c
}

func (c *clientMock) WatchAll(crdQ, endpointSliceQ *queue.Queue, stopCh <-chan struct{}) error {
	return nil
}

// KubeClient exports the kubernetes client interface
func (c *clientMock) KubeClient() kubernetes.Interface {
	return c.fakeClientset
}

func (c *clientMock) ServiceApiError(err error) {
	c.apiServiceError = err
}

func (c *clientMock) EndpointSliceApiError(err error) {
	c.apiEndpointsSliceError = err
}

func (c *clientMock) XdsServiceApiError(err error) {
	c.apiXdsServiceError = err
}

// Service returns the named service from the given namespace.
func (c *clientMock) Service(name, namespace string) (*corev1.Service, error) {
	if c.apiServiceError != nil {
		return nil, c.apiServiceError
	}
	for _, svc := range c.services {
		if svc.Namespace == namespace && svc.Name == name {
			return svc, nil
		}
	}
	return nil, fmt.Errorf("Service not found, name=%s, namespace=%s", name, namespace)
}

// EndpointSlice returns the named EndpointSlice from the given namespace.
func (c *clientMock) EndpointSlice(name, namespace string) (*discoveryv1.EndpointSlice, error) {
	if c.apiEndpointsSliceError != nil {
		return nil, c.apiEndpointsSliceError
	}
	for _, e := range c.endpointSlices {
		if e.Namespace == namespace && e.Name == name {
			return e, nil
		}
	}
	return nil, nil
}

// EndpointSliceList returns all the EndpointSlices selected by a label
func (c *clientMock) EndpointSliceList(labelSelector string) ([]*discoveryv1.EndpointSlice, error) {
	return c.endpointSlices, nil
}

// XdsServiceList lists the watched XdsServices resources
func (c *clientMock) XdsServiceList() ([]*v1alpha1.XdsService, error) {
	if c.apiXdsServiceError != nil {
		return nil, c.apiXdsServiceError
	}
	return c.xdsServices, nil
}

// Clear removes all the mocked resources from the client
func (c *clientMock) Clear() {
	c.services = []*corev1.Service{}
	c.endpointSlices = []*discoveryv1.EndpointSlice{}
	c.xdsServices = []*v1alpha1.XdsService{}
	c.fakeClientset = fake.NewSimpleClientset()
}
