package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// XdsServiceSpecService contains information regarding the Kubernetes Service
// we want to expose via xDS
type XdsServiceSpecService struct {
	// Name is the name of the Kubernetes Service to target
	Name string `json:"name"`
}

// XdsServiceSpecLoadBalancing contains information regarding the Load Balancing
// policy
type XdsServiceSpecLoadBalancing struct {
	// Policy is the name of the load balancing policy to be used
	// +optional
	// +kubebuilder:default=round_robin
	Policy string `json:"policy,omitempty"`
}

// XdsServiceSpec defines the desired config for a service served via xDS
type XdsServiceSpec struct {
	// AllowRemoteEndpoints determines whether this Service should look for
	// endpoints (EndpointSlices) in remote clusters.
	// +optional
	// +kubebuilder:default=false
	AllowRemoteEndpoints *bool `json:"allowRemoteEndpoints,omitempty"`
	// Service determines the Service resource to target
	Service XdsServiceSpecService `json:"service"`
	// LoadBalancing specidies the load balancer configuration to be passed
	// to xDS clients.
	// +kubebuilder:default={policy:round_robin}
	// +optional
	LoadBalancing XdsServiceSpecLoadBalancing `json:"loadBalancing,omitempty"`
	// PrioritizeLocalEndpoints determines whether to give higher priority
	// to endpoints that live in the local cluster
	// +optional
	//+kubebuilder:default=false
	PrioritizeLocalEndpoints *bool `json:"prioritizeLocalEndpoints,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true

// XdsService is the Schema for the XdsService of semaphore-xds controller. An
// XdsService is defined as the configuration used to construct a service that
// is served via the xDS server implemented by semaphore-xds to load balance
// traffic to GRPC endpoints.
// +kubebuilder:resource:shortName=xdssvc
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.service.name`
// +kubebuilder:printcolumn:name="LbPolicy",type=string,JSONPath=`.spec.loadBalancing.policy`
// +kubebuilder:printcolumn:name="AllowRemote",type=string,JSONPath=`.spec.allowRemoteEndpoints`
// +kubebuilder:printcolumn:name="PrioritizeLocal",type=string,JSONPath=`.spec.prioritizeLocalEndpoints`
type XdsService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec XdsServiceSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// XdsServiceList contains a list of XdsServices
type XdsServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []XdsService `json:"items"`
}
