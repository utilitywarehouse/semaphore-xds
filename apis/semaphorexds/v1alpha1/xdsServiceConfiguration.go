package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/utilitywarehouse/semaphore-xds/types"
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

// XdsServiceSpecRetryBackoffPolicy defines the exponential backoff policy for a service.
type XdsServiceSpecRetryBackoffPolicy struct {
	// Specifies the base interval between retries.
	// +optional
	// +kubebuilder:default="25ms"
	BaseInterval string `json:"baseInterval,omitempty"`
	// Specifies the maximum interval between retries.
	// +optional
	// +kubebuilder:default="250ms"
	MaxInterval string `json:"maxInterval,omitempty"`
}

// XdsServiceSpecRetry defines the retry policy for a service.
type XdsServiceSpecRetry struct {
	// Specifies the conditions under which retry takes place.
	// By default this is empty, which means retries are disabled.
	// +optional
	RetryOn []string `json:"retryOn,omitempty"`
	// Number of retries that will be attempted.
	// +optional
	// +kubebuilder:default=1
	NumRetries *uint32 `json:"numRetries,omitempty"`
	// Specifies parameters that control exponential retry back off.
	// +optional
	// +kubebuilder:default={baseInterval:"25ms",maxInterval:"250ms"}
	RetryBackOff XdsServiceSpecRetryBackoffPolicy `json:"backoff,omitempty"`
}

// XdsServiceSpec defines the desired config for a service served via xDS
type XdsServiceSpec struct {
	// EnableRemoteEndpoints determines whether this Service should look for
	// endpoints (EndpointSlices) in remote clusters.
	// +optional
	// +kubebuilder:default=false
	EnableRemoteEndpoints *bool `json:"enableRemoteEndpoints,omitempty"`
	// LoadBalancing specidies the load balancer configuration to be passed
	// to xDS clients.
	// +kubebuilder:default={policy:round_robin}
	// +optional
	LoadBalancing XdsServiceSpecLoadBalancing `json:"loadBalancing,omitempty"`
	// PriorityStrategy determines the strategy to follow when assigning
	// priorities to endpoints. Possible values are `none` and `local-first`
	// +optional
	// +kubebuilder:default=none
	// +kubebuilder:validation:Enum=none;local-first
	PriorityStrategy types.PolicyStrategy `json:"priorityStrategy,omitempty"`
	// Retry specifies the retry policy for the service.
	// +optional
	Retry *XdsServiceSpecRetry `json:"retry,omitempty"`
	// Service determines the Service resource to target
	Service XdsServiceSpecService `json:"service"`
}

// +genclient
// +kubebuilder:object:root=true

// XdsService is the Schema for the XdsService of semaphore-xds controller. An
// XdsService is defined as the configuration used to construct a service that
// is served via the xDS server implemented by semaphore-xds to load balance
// traffic to GRPC endpoints.
// +kubebuilder:resource:shortName=xdssvc
// +kubebuilder:printcolumn:name="Service",type=string,JSONPath=`.spec.service.name`
// +kubebuilder:printcolumn:name="Lb_Policy",type=string,JSONPath=`.spec.loadBalancing.policy`
// +kubebuilder:printcolumn:name="Remote_Endpoints",type=string,JSONPath=`.spec.enableRemoteEndpoints`
// +kubebuilder:printcolumn:name="Priority_Strategy",type=string,JSONPath=`.spec.priorityStrategy`
// +kubebuilder:printcolumn:name="Retry_On",type=string,JSONPath=`.spec.retry.on`
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
