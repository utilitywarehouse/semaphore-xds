package xds

import (
	"testing"

	clusterv3 "github.com/envoyproxy/go-control-plane/envoy/config/cluster/v3"
	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestXdsServiceStore_Empty(t *testing.T) {
	serviceStore := NewXdsServiceStore()
	assert.Equal(t, 0, len(serviceStore.All()))
}

func TestXdsServiceStore_AddNew(t *testing.T) {
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
	assert.Equal(t, 1, len(serviceStore.All()))
}

func TestXdsServiceStore_AddMany(t *testing.T) {
	serviceStore := NewXdsServiceStore()
	svcF := &v1.Service{
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
	serviceStore.AddOrUpdate(svcF, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	svcO := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "other",
			Namespace: "bar",
		},
	}
	serviceStore.AddOrUpdate(svcO, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    true,
		PrioritizeLocalEndpoints: true,
	})
	assert.Equal(t, 2, len(serviceStore.All()))
	foo, err := serviceStore.Get("foo", "bar")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "foo", foo.Service.Name)
	assert.Equal(t, false, foo.EnableRemoteEndpoints)
	assert.Equal(t, false, foo.PrioritizeLocalEndpoints)
	other, err := serviceStore.Get("other", "bar")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, "other", other.Service.Name)
	assert.Equal(t, true, other.EnableRemoteEndpoints)
	assert.Equal(t, true, other.PrioritizeLocalEndpoints)
}

func TestXdsServiceStore_AddTheSame(t *testing.T) {
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
	assert.Equal(t, 1, len(serviceStore.All()))
	// Re-add the same service
	serviceStore.AddOrUpdate(svc, Service{
		Policy:                   clusterv3.Cluster_ROUND_ROBIN,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	assert.Equal(t, 1, len(serviceStore.All()))
}

func TestXdsServiceStore_Update(t *testing.T) {
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
	assert.Equal(t, 1, len(serviceStore.All()))
	s, err := serviceStore.Get("foo", "bar")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, int32(80), s.Service.Spec.Ports[0].Port)
	assert.Equal(t, clusterv3.Cluster_ROUND_ROBIN, s.Policy)
	// Change port value and lb policy and update
	svc.Spec.Ports[0].Port = int32(81)
	serviceStore.AddOrUpdate(svc, Service{
		Policy:                   clusterv3.Cluster_RING_HASH,
		EnableRemoteEndpoints:    false,
		PrioritizeLocalEndpoints: false,
	})
	assert.Equal(t, 1, len(serviceStore.All()))
	s, err = serviceStore.Get("foo", "bar")
	if err != nil {
		t.Fatal(err)
	}
	assert.Equal(t, int32(81), s.Service.Spec.Ports[0].Port)
	assert.Equal(t, clusterv3.Cluster_RING_HASH, s.Policy)
}
