package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RayClusterSpec defines the desired state of RayCluster
type RayClusterSpec struct {
	// Suspend indicates whether a RayCluster should be suspended.
	// A suspended RayCluster will have head pods and worker pods deleted.
	// +optional
	Suspend *bool `json:"suspend,omitempty"`
	// ManagedBy is an optional configuration for the controller or entity that manages a RayCluster.
	// The value must be either 'ray.io/kuberay-operator' or 'kueue.x-k8s.io/multikueue'.
	// The kuberay-operator reconciles a RayCluster which doesn't have this field at all or
	// the field value is the reserved string 'ray.io/kuberay-operator',
	// but delegates reconciling the RayCluster with 'kueue.x-k8s.io/multikueue' to the Kueue.
	// The field is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the managedBy field is immutable"
	// +kubebuilder:validation:XValidation:rule="self in ['ray.io/kuberay-operator', 'kueue.x-k8s.io/multikueue']",message="the managedBy field value must be either 'ray.io/kuberay-operator' or 'kueue.x-k8s.io/multikueue'"
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`
	// AutoscalerOptions specifies optional configuration for the Ray autoscaler.
	// +optional
	AutoscalerOptions *AutoscalerOptions `json:"autoscalerOptions,omitempty"`
	// +optional
	HeadServiceAnnotations map[string]string `json:"headServiceAnnotations,omitempty"`
	// EnableInTreeAutoscaling indicates whether operator should create in tree autoscaling configs
	// +optional
	EnableInTreeAutoscaling *bool `json:"enableInTreeAutoscaling,omitempty"`
	// GcsFaultToleranceOptions for enabling GCS FT
	// +optional
	GcsFaultToleranceOptions *GcsFaultToleranceOptions `json:"gcsFaultToleranceOptions,omitempty"`
	// HeadGroupSpec is the spec for the head pod
	HeadGroupSpec HeadGroupSpec `json:"headGroupSpec"`
	// RayVersion is used to determine the command for the Kubernetes Job managed by RayJob
	// +optional
	RayVersion string `json:"rayVersion,omitempty"`
	// WorkerGroupSpecs are the specs for the worker pods
	// +optional
	WorkerGroupSpecs []WorkerGroupSpec `json:"workerGroupSpecs,omitempty"`
}

// GcsFaultToleranceOptions contains configs for GCS FT
type GcsFaultToleranceOptions struct {
	// +optional
	RedisUsername *RedisCredential `json:"redisUsername,omitempty"`
	// +optional
	RedisPassword *RedisCredential `json:"redisPassword,omitempty"`
	// +optional
	ExternalStorageNamespace string `json:"externalStorageNamespace,omitempty"`
	RedisAddress             string `json:"redisAddress"`
}

// RedisCredential is the redis username/password or a reference to the source containing the username/password
type RedisCredential struct {
	// +optional
	ValueFrom *corev1.EnvVarSource `json:"valueFrom,omitempty"`
	// +optional
	Value string `json:"value,omitempty"`
}

// HeadGroupSpec are the spec for the head pod
type HeadGroupSpec struct {
	// Template is the exact pod template used in K8s deployments, statefulsets, etc.
	Template corev1.PodTemplateSpec `json:"template"`
	// HeadService is the Kubernetes service of the head pod.
	// +optional
	HeadService *corev1.Service `json:"headService,omitempty"`
	// EnableIngress indicates whether operator should create ingress object for head service or not.
	// +optional
	EnableIngress *bool `json:"enableIngress,omitempty"`
	// RayStartParams are the params of the start command: node-manager-port, object-store-memory, ...
	// +optional
	RayStartParams map[string]string `json:"rayStartParams"`
	// ServiceType is Kubernetes service type of the head service. it will be used by the workers to connect to the head pod
	// +optional
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`
}

// WorkerGroupSpec are the specs for the worker pods
type WorkerGroupSpec struct {
	// Suspend indicates whether a worker group should be suspended.
	// A suspended worker group will have all pods deleted.
	// This is not a user-facing API and is only used by RayJob DeletionPolicy.
	// +optional
	Suspend *bool `json:"suspend,omitempty"`
	// we can have multiple worker groups, we distinguish them by name
	GroupName string `json:"groupName"`
	// Replicas is the number of desired Pods for this worker group. See https://github.com/ray-project/kuberay/pull/1443 for more details about the reason for making this field optional.
	// +kubebuilder:default:=0
	// +optional
	Replicas *int32 `json:"replicas,omitempty"`
	// MinReplicas denotes the minimum number of desired Pods for this worker group.
	// +kubebuilder:default:=0
	MinReplicas *int32 `json:"minReplicas"`
	// MaxReplicas denotes the maximum number of desired Pods for this worker group, and the default value is maxInt32.
	// +kubebuilder:default:=2147483647
	MaxReplicas *int32 `json:"maxReplicas"`
	// IdleTimeoutSeconds denotes the number of seconds to wait before the v2 autoscaler terminates an idle worker pod of this type.
	// This value is only used with the Ray Autoscaler enabled and defaults to the value set by the AutoscalingConfig if not specified for this worker group.
	// +optional
	IdleTimeoutSeconds *int32 `json:"idleTimeoutSeconds,omitempty"`
	// RayStartParams are the params of the start command: address, object-store-memory, ...
	// +optional
	RayStartParams map[string]string `json:"rayStartParams"`
	// Template is a pod template for the worker
	Template corev1.PodTemplateSpec `json:"template"`
	// ScaleStrategy defines which pods to remove
	// +optional
	ScaleStrategy ScaleStrategy `json:"scaleStrategy,omitempty"`
	// NumOfHosts denotes the number of hosts to create per replica. The default value is 1.
	// +kubebuilder:default:=1
	// +optional
	NumOfHosts int32 `json:"numOfHosts,omitempty"`
}

// ScaleStrategy to remove workers
type ScaleStrategy struct {
	// WorkersToDelete workers to be deleted
	WorkersToDelete []string `json:"workersToDelete,omitempty"`
}

// AutoscalerOptions specifies optional configuration for the Ray autoscaler.
type AutoscalerOptions struct {
	// Resources specifies optional resource request and limit overrides for the autoscaler container.
	// Default values: 500m CPU request and limit. 512Mi memory request and limit.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Image optionally overrides the autoscaler's container image. This override is provided for autoscaler testing and development.
	// +optional
	Image *string `json:"image,omitempty"`
	// ImagePullPolicy optionally overrides the autoscaler container's image pull policy. This override is provided for autoscaler testing and development.
	// +optional
	ImagePullPolicy *corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	// SecurityContext defines the security options the container should be run with.
	// If set, the fields of SecurityContext override the equivalent fields of PodSecurityContext.
	// More info: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
	// IdleTimeoutSeconds is the number of seconds to wait before scaling down a worker pod which is not using Ray resources.
	// Defaults to 60 (one minute). It is not read by the KubeRay operator but by the Ray autoscaler.
	// +optional
	IdleTimeoutSeconds *int32 `json:"idleTimeoutSeconds,omitempty"`
	// UpscalingMode is "Conservative", "Default", or "Aggressive."
	// Conservative: Upscaling is rate-limited; the number of pending worker pods is at most the size of the Ray cluster.
	// Default: Upscaling is not rate-limited.
	// Aggressive: An alias for Default; upscaling is not rate-limited.
	// It is not read by the KubeRay operator but by the Ray autoscaler.
	// +optional
	UpscalingMode *UpscalingMode `json:"upscalingMode,omitempty"`
	// Version is the version of the Ray autoscaler.
	// Setting this to v1 will explicitly use autoscaler v1.
	// Setting this to v2 will explicitly use autoscaler v2.
	// If this isn't set, the Ray version determines the autoscaler version.
	// In Ray 2.47.0 and later, the default autoscaler version is v2. It's v1 before that.
	// +optional
	Version *AutoscalerVersion `json:"version,omitempty"`
	// Optional list of environment variables to set in the autoscaler container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
	// Optional list of sources to populate environment variables in the autoscaler container.
	// +optional
	EnvFrom []corev1.EnvFromSource `json:"envFrom,omitempty"`
	// Optional list of volumeMounts.  This is needed for enabling TLS for the autoscaler container.
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// +kubebuilder:validation:Enum=Default;Aggressive;Conservative
type UpscalingMode string

// +kubebuilder:validation:Enum=v1;v2
type AutoscalerVersion string

const (
	AutoscalerVersionV1 AutoscalerVersion = "v1"
	AutoscalerVersionV2 AutoscalerVersion = "v2"
)

// The overall state of the Ray cluster.
type ClusterState string

const (
	Ready ClusterState = "ready"
	// Failed is deprecated, but we keep it to avoid compilation errors in projects that import the KubeRay Golang module.
	Failed    ClusterState = "failed"
	Suspended ClusterState = "suspended"
)

// RayClusterStatus defines the observed state of RayCluster
type RayClusterStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// Status reflects the status of the cluster
	//
	// Deprecated: the State field is replaced by the Conditions field.
	// +optional
	State ClusterState `json:"state,omitempty"`
	// DesiredCPU indicates total desired CPUs for the cluster
	// +optional
	DesiredCPU resource.Quantity `json:"desiredCPU,omitempty"`
	// DesiredMemory indicates total desired memory for the cluster
	// +optional
	DesiredMemory resource.Quantity `json:"desiredMemory,omitempty"`
	// DesiredGPU indicates total desired GPUs for the cluster
	// +optional
	DesiredGPU resource.Quantity `json:"desiredGPU,omitempty"`
	// DesiredTPU indicates total desired TPUs for the cluster
	// +optional
	DesiredTPU resource.Quantity `json:"desiredTPU,omitempty"`
	// LastUpdateTime indicates last update timestamp for this cluster status.
	// +nullable
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
	// StateTransitionTimes indicates the time of the last state transition for each state.
	// +optional
	StateTransitionTimes map[ClusterState]*metav1.Time `json:"stateTransitionTimes,omitempty"`
	// Service Endpoints
	// +optional
	Endpoints map[string]string `json:"endpoints,omitempty"`
	// Head info
	// +optional
	Head HeadInfo `json:"head,omitempty"`
	// Reason provides more information about current State
	// +optional
	Reason string `json:"reason,omitempty"`

	// Represents the latest available observations of a RayCluster's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ReadyWorkerReplicas indicates the number of worker pods currently in the Ready state in the cluster.
	// It actually reflects the number of Ready pods, although it is named "replicas" to maintain backward compatibility.
	// +optional
	ReadyWorkerReplicas int32 `json:"readyWorkerReplicas,omitempty"`
	// AvailableWorkerReplicas indicates how many worker pods are currently available (i.e., running).
	// It is named "replicas" to maintain backward compatibility.
	// +optional
	AvailableWorkerReplicas int32 `json:"availableWorkerReplicas,omitempty"`
	// DesiredWorkerReplicas indicates the desired total number of worker Pods at the cluster level,
	// calculated as the sum of `replicas * numOfHosts` for each worker group.
	// It is named "replicas" to maintain backward compatibility.
	// +optional
	DesiredWorkerReplicas int32 `json:"desiredWorkerReplicas,omitempty"`
	// MinWorkerReplicas indicates the minimum number of worker pods across all worker groups,
	// calculated as the sum of `minReplicas * numOfHosts` for each worker group.
	// It is named "replicas" to maintain backward compatibility.
	// +optional
	MinWorkerReplicas int32 `json:"minWorkerReplicas,omitempty"`
	// MaxWorkerReplicas indicates the maximum number of worker pods across all worker groups,
	// calculated as the sum of `maxReplicas * numOfHosts` for each worker group.
	// It is named "replicas" to maintain backward compatibility.
	// +optional
	MaxWorkerReplicas int32 `json:"maxWorkerReplicas,omitempty"`
	// observedGeneration is the most recent generation observed for this RayCluster. It corresponds to the
	// RayCluster's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

type RayClusterConditionType string

// Custom Reason for RayClusterCondition
const (
	AllPodRunningAndReadyFirstTime = "AllPodRunningAndReadyFirstTime"
	RayClusterPodsProvisioning     = "RayClusterPodsProvisioning"
	HeadPodNotFound                = "HeadPodNotFound"
	HeadPodRunningAndReady         = "HeadPodRunningAndReady"
	// UnknownReason says that the reason for the condition is unknown.
	UnknownReason = "Unknown"
)

const (
	// RayClusterProvisioned indicates whether all Ray Pods are ready for the first time.
	// After RayClusterProvisioned is set to true for the first time, it will not change anymore.
	RayClusterProvisioned RayClusterConditionType = "RayClusterProvisioned"
	// HeadPodReady indicates whether RayCluster's head Pod is ready for requests.
	HeadPodReady RayClusterConditionType = "HeadPodReady"
	// RayClusterReplicaFailure is added in a RayCluster when one of its pods fails to be created or deleted.
	RayClusterReplicaFailure RayClusterConditionType = "ReplicaFailure"
	// RayClusterSuspending is set to true when a user sets .Spec.Suspend to true, ensuring the atomicity of the suspend operation.
	RayClusterSuspending RayClusterConditionType = "RayClusterSuspending"
	// RayClusterSuspended is set to true when all Pods belonging to a suspending RayCluster are deleted. Note that RayClusterSuspending and RayClusterSuspended cannot both be true at the same time.
	RayClusterSuspended RayClusterConditionType = "RayClusterSuspended"
)

// HeadInfo gives info about head
type HeadInfo struct {
	// +optional
	PodIP string `json:"podIP,omitempty"`
	// +optional
	ServiceIP string `json:"serviceIP,omitempty"`
	// +optional
	PodName string `json:"podName,omitempty"`
	// +optional
	ServiceName string `json:"serviceName,omitempty"`
}

// RayNodeType  the type of a ray node: head/worker
type RayNodeType string

const (
	HeadNode   RayNodeType = "head"
	WorkerNode RayNodeType = "worker"
	// RedisCleanupNode is a Pod managed by a Kubernetes Job that cleans up Redis data after
	// a RayCluster with GCS fault tolerance enabled is deleted.
	RedisCleanupNode RayNodeType = "redis-cleanup"
)

// RayCluster is the Schema for the RayClusters API
// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=all
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="desired workers",type=integer,JSONPath=".status.desiredWorkerReplicas",priority=0
// +kubebuilder:printcolumn:name="available workers",type=integer,JSONPath=".status.availableWorkerReplicas",priority=0
// +kubebuilder:printcolumn:name="cpus",type=string,JSONPath=".status.desiredCPU",priority=0
// +kubebuilder:printcolumn:name="memory",type=string,JSONPath=".status.desiredMemory",priority=0
// +kubebuilder:printcolumn:name="gpus",type=string,JSONPath=".status.desiredGPU",priority=0
// +kubebuilder:printcolumn:name="tpus",type=string,JSONPath=".status.desiredTPU",priority=1
// +kubebuilder:printcolumn:name="status",type="string",JSONPath=".status.state",priority=0
// +kubebuilder:printcolumn:name="age",type="date",JSONPath=".metadata.creationTimestamp",priority=0
// +kubebuilder:printcolumn:name="head pod IP",type="string",JSONPath=".status.head.podIP",priority=1
// +kubebuilder:printcolumn:name="head service IP",type="string",JSONPath=".status.head.serviceIP",priority=1
// +genclient
type RayCluster struct {
	// Standard object metadata.
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired behavior of the RayCluster.
	Spec RayClusterSpec `json:"spec,omitempty"`
	// +optional
	Status RayClusterStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RayClusterList contains a list of RayCluster
type RayClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RayCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RayCluster{}, &RayClusterList{})
}

type EventReason string

const (
	RayConfigError         EventReason = "RayConfigError"
	PodReconciliationError EventReason = "PodReconciliationError"
)
