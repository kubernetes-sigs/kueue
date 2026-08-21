package v1

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RayClusterSpec defines the desired state of RayCluster
type RayClusterSpec struct {
	// UpgradeStrategy defines the scaling policy used when upgrading the RayCluster
	// +optional
	UpgradeStrategy *RayClusterUpgradeStrategy `json:"upgradeStrategy,omitempty"`
	// AuthOptions specifies the authentication options for the RayCluster.
	// +optional
	AuthOptions *AuthOptions `json:"authOptions,omitempty"`
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
	// HistoryServerOptions used for history server related configuration
	// +optional
	HistoryServerOptions *HistoryServerOptions `json:"historyServerOptions,omitempty"`
	// NetworkPolicy specifies optional configuration for network isolation.
	// When set, separate NetworkPolicies are created for head and worker pods.
	// The reconciler always permits intra-cluster pod-to-pod traffic.
	// Note: under DenyAll/DenyAllEgress, DNS egress is not added
	// automatically; since Ray pods reach the head via its service FQDN, you must
	// allow DNS egress via Head/Worker EgressRules or the cluster will fail to start.
	// +optional
	NetworkPolicy *NetworkPolicyConfig `json:"networkPolicy,omitempty"`
	// TLSOptions specifies optional TLS encryption settings for the RayCluster.
	// If omitted or Enabled is false, TLS is disabled. When Enabled is true,
	// the operator enables mTLS using cert-manager to provision and manage certificates.
	// Requires the RayClusterMTLS feature gate on the operator.
	// +optional
	TLSOptions *TLSOptions `json:"tlsOptions,omitempty"`
	// HeadGroupSpec is the spec for the head pod
	HeadGroupSpec HeadGroupSpec `json:"headGroupSpec"`
	// RayVersion is used to determine the command for the Kubernetes Job managed by RayJob
	// +optional
	RayVersion string `json:"rayVersion,omitempty"`
	// WorkerGroupSpecs are the specs for the worker pods
	// +optional
	WorkerGroupSpecs []WorkerGroupSpec `json:"workerGroupSpecs,omitempty"`
}

// TLSOptions configures TLS encryption for the RayCluster.
// When TLSOptions is nil or Enabled is nil/false, TLS is disabled.
// When Enabled is true, the operator uses cert-manager to automatically
// provision a full PKI (self-signed CA, head and worker leaf certificates)
// and keeps certificates up to date as pod IPs change during autoscaling.
type TLSOptions struct {
	// Enabled controls whether mTLS is active for this RayCluster.
	// Defaults to false when omitted. Set to true to enable mTLS.
	// +optional
	Enabled *bool `json:"enabled,omitempty"`
}

// +kubebuilder:validation:Enum=Recreate;None
type RayClusterUpgradeType string

const (
	// During upgrade, Recreate strategy will delete all existing pods before creating new ones
	RayClusterRecreate RayClusterUpgradeType = "Recreate"
	// No new pod will be created while the strategy is set to None
	RayClusterUpgradeNone RayClusterUpgradeType = "None"
)

type RayClusterUpgradeStrategy struct {
	// Type represents the strategy used when upgrading the RayCluster Pods. Currently supports `Recreate` and `None`.
	// +optional
	Type *RayClusterUpgradeType `json:"type,omitempty"`
}

// AuthMode describes the authentication mode for the Ray cluster.
type AuthMode string

const (
	// AuthModeDisabled disables authentication.
	AuthModeDisabled AuthMode = "disabled"
	// AuthModeToken enables token-based authentication.
	AuthModeToken AuthMode = "token"
)

// AuthOptions defines the authentication options for a RayCluster.
type AuthOptions struct {
	// EnableK8sTokenAuth enables Kubernetes-delegated token authentication.
	// When true, the RAY_ENABLE_K8S_TOKEN_AUTH environment variable is set to "true"
	// across all Ray Pods, and Ray will delegate authentication to the K8s API server.
	//
	// NOTE: The Kubernetes ServiceAccount token mounted to Raylets must be granted
	// the `ray:write` custom verb via RBAC for this to function correctly.
	//
	// WARNING: This feature is intended for standalone RayCluster objects and is
	// currently unsupported for RayJob or RayService resources.
	// +optional
	EnableK8sTokenAuth *bool `json:"enableK8sTokenAuth,omitempty"`

	// SecretName is the name of the Secret that contains the authentication token.
	// If set, KubeRay will skip generating a Secret object per RayCluster containing a token.
	// The Secret must have a data key `auth_token` that contains the value of the token.
	// +optional
	SecretName *string `json:"secretName,omitempty"`

	// Mode specifies the authentication mode.
	// Supported values are "disabled" and "token".
	// Defaults to "token".
	// +kubebuilder:validation:Enum=disabled;token
	// +optional
	Mode AuthMode `json:"mode,omitempty"`
}

// GcsFaultToleranceBackend selects the GCS fault tolerance persistence backend.
// +kubebuilder:validation:Enum=redis;rocksdb
type GcsFaultToleranceBackend string

const (
	// GcsFTBackendRedis persists GCS metadata in an external Redis service.
	GcsFTBackendRedis GcsFaultToleranceBackend = "redis"
	// GcsFTBackendRocksDB persists GCS metadata in an embedded RocksDB store on a
	// persistent volume mounted on the head Pod.
	GcsFTBackendRocksDB GcsFaultToleranceBackend = "rocksdb"
)

// GcsFaultToleranceOptions contains configs for GCS FT
type GcsFaultToleranceOptions struct {
	// Backend selects the GCS FT persistence backend. Defaults to "redis" for
	// backward compatibility. Immutable: the backend cannot be switched on an
	// existing RayCluster (doing so would swap the entire GCS store and head-Pod
	// wiring, losing fault-tolerance state).
	// +optional
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="gcsFaultToleranceOptions.backend is immutable"
	Backend GcsFaultToleranceBackend `json:"backend,omitempty"`

	// ----- Redis backend fields -----

	// +optional
	RedisUsername *RedisCredential `json:"redisUsername,omitempty"`
	// +optional
	RedisPassword *RedisCredential `json:"redisPassword,omitempty"`
	// +optional
	ExternalStorageNamespace string `json:"externalStorageNamespace,omitempty"`
	// RedisAddress is the address of the external Redis service used when Backend
	// is "redis". It may alternatively be supplied via env vars/annotations.
	// +optional
	RedisAddress string `json:"redisAddress,omitempty"`

	// ----- RocksDB (embedded) backend fields -----

	// Storage configures the persistent volume backing the embedded RocksDB
	// store. Only used when Backend is "rocksdb".
	// +optional
	Storage *GcsEmbeddedStorage `json:"storage,omitempty"`
}

// GcsEmbeddedStorage configures the PVC backing the embedded RocksDB store.
//
// RocksDB tolerates only a single writer at a time. The operator mounts the
// volume on the head Pod but does not itself enforce mutual exclusion, so when a
// volume can be attached to more than one Pod concurrently (see AccessModes) the
// caller is responsible for ensuring only one Ray head writes to it at a time.
type GcsEmbeddedStorage struct {
	// ClaimName is the name of an existing, user-provided PersistentVolumeClaim to
	// use as the RocksDB store ("bring your own" PVC). When set, the operator
	// consumes that PVC as-is: it does not create, delete, resize, or set
	// ownerReferences on it -- the user owns its entire lifecycle. Mutually
	// exclusive with Size/StorageClassName/AccessModes (those configure an
	// operator-managed PVC, which is used instead when ClaimName is empty).
	//
	// This is the supported path for persisting GCS state across a RayService
	// zero-downtime upgrade: point every RayService generation at the same claim.
	// (An operator-managed PVC is keyed by and owned by the RayCluster, so it is
	// not reused across upgrades.) Because the old and new head Pods overlap during
	// a zero-downtime upgrade, the claim must permit concurrent attach
	// (ReadWriteMany) with externally-coordinated single-writer semantics, or an
	// active-passive handoff where only one Pod attaches at once.
	// +optional
	ClaimName string `json:"claimName,omitempty"`

	// Size of the operator-managed PVC (e.g. "1Gi"). Ignored when ClaimName is set.
	// Defaults to 1Gi. The operator-managed PVC is created once and not
	// reconfigured in place; to change size/class/accessModes, delete the PVC (or
	// switch to ClaimName). A warning event is emitted if this diverges from the
	// live PVC.
	// +optional
	Size *resource.Quantity `json:"size,omitempty"`

	// StorageClassName for the operator-managed PVC. Uses the cluster default
	// StorageClass when omitted. Ignored when ClaimName is set.
	// +optional
	StorageClassName *string `json:"storageClassName,omitempty"`

	// AccessModes for the operator-managed PVC. Defaults to [ReadWriteOnce].
	// Ignored when ClaimName is set.
	//
	// ReadWriteOnce is the sane default for a standalone RayCluster (one head Pod
	// attaches at a time). ReadWriteMany is a valid choice when you need the volume
	// attached to multiple Pods concurrently (e.g. to overlap the old and new head
	// during a RayService upgrade); RocksDB still requires that only one of them
	// writes at a time, which you must coordinate externally.
	// +optional
	AccessModes []corev1.PersistentVolumeAccessMode `json:"accessModes,omitempty"`

	// SubPath mounts a subdirectory of the volume instead of its root.
	// +optional
	SubPath string `json:"subPath,omitempty"`

	// DeletionPolicy controls the lifecycle of the operator-managed PVC relative to
	// the owning RayCluster. Defaults to DeleteWithCluster. Ignored when ClaimName
	// is set (the operator never owns a bring-your-own PVC, so it is never deleted
	// or retained by the operator).
	//
	// Recovery after Retain: a PVC left behind by a Retain delete can be recovered
	// either by pointing a new cluster's ClaimName at it, or by recreating a
	// RayCluster with the same name on the operator-managed path -- the operator
	// adopts the existing {cluster}-gcs-pvc and reuses its RocksDB state. To start
	// from a fresh store instead, delete the leftover PVC first.
	// +optional
	DeletionPolicy *GCSStorageDeletionPolicy `json:"deletionPolicy,omitempty"`
}

// GCSStorageDeletionPolicy specifies what happens to the operator-managed GCS
// storage PVC when the owning RayCluster is deleted.
// +kubebuilder:validation:Enum=DeleteWithCluster;Retain
type GCSStorageDeletionPolicy string

const (
	// DeleteWithClusterGCSStorageDeletionPolicy (the default) makes the
	// operator-managed PVC a child of the RayCluster via an ownerReference, so it
	// (and its RocksDB data) is garbage-collected together with the cluster.
	DeleteWithClusterGCSStorageDeletionPolicy GCSStorageDeletionPolicy = "DeleteWithCluster"
	// RetainGCSStorageDeletionPolicy keeps the operator-managed PVC (and its data)
	// after the owning RayCluster is deleted: the operator omits the ownerReference
	// so the PVC outlives the cluster. Recover the GCS state by pointing a new
	// cluster's ClaimName at the retained PVC.
	RetainGCSStorageDeletionPolicy GCSStorageDeletionPolicy = "Retain"
)

// RedisCredential is the redis username/password or a reference to the source containing the username/password
type RedisCredential struct {
	// +optional
	ValueFrom *corev1.EnvVarSource `json:"valueFrom,omitempty"`
	// +optional
	Value string `json:"value,omitempty"`
}

// HistoryServerOptions used for history server related configuration
type HistoryServerOptions struct {
	// CollectorOptions used for collector sidecar configuration
	// +optional
	CollectorOptions *CollectorOptions `json:"collectorOptions,omitempty"`
}

// CollectorOptions defines settings for the history server collector sidecar.
type CollectorOptions struct {
	// Image is the collector container image to be used (e.g. quay.io/kuberay/collector:latest).
	// +optional
	Image *string `json:"image,omitempty"`
	// ImagePullPolicy is the pull policy for the collector image.
	// +optional
	ImagePullPolicy *corev1.PullPolicy `json:"imagePullPolicy,omitempty"`
	// Resources specifies computing resource requirements.
	// +optional
	Resources *corev1.ResourceRequirements `json:"resources,omitempty"`
	// Env allows injecting custom environment variables into the collector container.
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`
}

// NetworkPolicyMode is the type for network isolation mode constants.
// +kubebuilder:validation:Enum=DenyAll;DenyAllIngress;DenyAllEgress
type NetworkPolicyMode string

// Network isolation mode constants for NetworkPolicyConfig.Mode.
const (
	// NetworkPolicyDenyAll denies all ingress and egress traffic.
	NetworkPolicyDenyAll NetworkPolicyMode = "DenyAll"
	// NetworkPolicyDenyAllIngress denies all ingress traffic.
	NetworkPolicyDenyAllIngress NetworkPolicyMode = "DenyAllIngress"
	// NetworkPolicyDenyAllEgress denies all egress traffic.
	NetworkPolicyDenyAllEgress NetworkPolicyMode = "DenyAllEgress"
)

// NetworkPolicyConfig defines network isolation settings for Ray cluster.
// All modes permit intra-cluster pod-to-pod traffic.
// DNS egress is not included automatically; see NetworkPolicyRules.EgressRules
// for why it must be added under DenyAll/DenyAllEgress.
type NetworkPolicyConfig struct {
	// Mode controls the security level. All modes permit intra-cluster pod-to-pod
	// traffic (DNS egress excluded, see EgressRules).
	// - "DenyAll": Denies all Ingress and Egress.
	// - "DenyAllIngress": Denies all Ingress.
	// - "DenyAllEgress": Denies all Egress.
	// +optional
	// +kubebuilder:default=DenyAll
	Mode *NetworkPolicyMode `json:"mode,omitempty"`

	// Head specifies custom NetworkPolicy rules applied only to the head pod's policy.
	// The base head policy always allows intra-cluster traffic and (for K8sJobMode
	// RayJob-owned clusters) the submitter pod. Rules here are appended to those
	// base rules. Platforms that need operator dashboard access should add it here
	// (e.g. via a mutating webhook).
	// +optional
	Head *NetworkPolicyRules `json:"head,omitempty"`

	// Worker specifies custom NetworkPolicy rules applied only to worker pods' policy.
	// The base worker policy always allows intra-cluster traffic.
	// Rules here are appended to that base rule.
	// Acts as the default for all worker groups; see WorkerGroups for per-group overrides.
	// +optional
	Worker *NetworkPolicyRules `json:"worker,omitempty"`

	// WorkerGroups specifies per-worker-group NetworkPolicy rules, keyed by group name.
	// If an entry exists for a worker group, it replaces (not merges with) Worker for
	// that group. Worker groups without an entry fall back to Worker.
	// +optional
	// +listType=map
	// +listMapKey=groupName
	WorkerGroups []WorkerGroupNetworkPolicyRules `json:"workerGroups,omitempty"`
}

// WorkerGroupNetworkPolicyRules is NetworkPolicyRules bound to one worker group.
type WorkerGroupNetworkPolicyRules struct {
	// GroupName matches WorkerGroupSpec.GroupName.
	// +kubebuilder:validation:Required
	GroupName string `json:"groupName"`

	NetworkPolicyRules `json:",inline"`
}

// NetworkPolicyRules defines custom ingress and egress rules for a NetworkPolicy.
type NetworkPolicyRules struct {
	// IngressRules specifies custom ingress rules appended to the base policy.
	// Only meaningful when the mode includes ingress denial (DenyAll or DenyAllIngress).
	// +optional
	IngressRules []networkingv1.NetworkPolicyIngressRule `json:"ingressRules,omitempty"`

	// EgressRules specifies custom egress rules appended to the base policy.
	// Only meaningful when the mode includes egress denial (DenyAll or DenyAllEgress).
	// DNS egress is NOT added automatically: under DenyAll/DenyAllEgress you MUST
	// add a DNS rule here (e.g. to kube-system pods labeled k8s-app=kube-dns on
	// port 53), because Ray workers reach the head via its service FQDN and cannot
	// resolve it without DNS. See the network-policy-deny-all sample.
	// +optional
	EgressRules []networkingv1.NetworkPolicyEgressRule `json:"egressRules,omitempty"`
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
	// IngressOptions specifies optional ingress configuration for the head service.
	// +optional
	IngressOptions *IngressOptions `json:"ingressOptions,omitempty"`
	// Resources specifies the resource quantities for the head group.
	// These values override the resources passed to `rayStartParams` for the group, but
	// have no effect on the resources set at the K8s Pod container level.
	// +optional
	Resources map[string]string `json:"resources,omitempty"`
	// Labels specifies the Ray node labels for the head group.
	// These labels will also be added to the Pods of this head group and override the `--labels`
	// argument passed to `rayStartParams`.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
	// RayStartParams are the params of the start command: node-manager-port, object-store-memory, ...
	// +optional
	RayStartParams map[string]string `json:"rayStartParams"`
	// ServiceType is Kubernetes service type of the head service. it will be used by the workers to connect to the head pod
	// +optional
	ServiceType corev1.ServiceType `json:"serviceType,omitempty"`
}

// +kubebuilder:validation:Enum=Exact;Prefix;ImplementationSpecific
type IngressPathType string

const (
	IngressPathTypeExact                  IngressPathType = "Exact"
	IngressPathTypePrefix                 IngressPathType = "Prefix"
	IngressPathTypeImplementationSpecific IngressPathType = "ImplementationSpecific"
)

// IngressOptions defines the host, path, and TLS configuration for the ingress generated for the head group.
type IngressOptions struct {
	// Host is the fully-qualified domain name used to route external traffic to the
	// Ray head dashboard. When unset, the generated ingress rule matches any host.
	// +optional
	Host *string `json:"host,omitempty"`
	// Path is the HTTP path that routes to the Ray head dashboard.
	// When unset, the operator defaults it to "/", which routes all traffic on the
	// host to the dashboard.
	// +optional
	Path *string `json:"path,omitempty"`
	// PathType is the path matching mode applied to Path.
	// When unset, the operator defaults it to "Prefix", which works out of the box
	// without a rewrite-target annotation or controller-specific regex support.
	// +optional
	PathType *IngressPathType `json:"pathType,omitempty"`
	// TLS configures TLS termination for the generated ingress.
	// +optional
	TLS []networkingv1.IngressTLS `json:"tls,omitempty"`
}

// WorkerGroupSpec are the specs for the worker pods
type WorkerGroupSpec struct {
	// Suspend indicates whether a worker group should be suspended.
	// A suspended worker group will have all pods deleted.
	// This is not a user-facing API and is only used by RayJob DeletionStrategy.
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
	// Priority influences which worker group the autoscaler prefers when multiple
	// groups can satisfy the same resource demand. Higher priority groups are
	// preferred for scale-up. Only honored by Ray Autoscaler v2 (Ray >= 2.56).
	// +kubebuilder:default:=0
	// +optional
	Priority *int32 `json:"priority,omitempty"`
	// Resources specifies the resource quantities for this worker group.
	// These values override the resources passed to `rayStartParams` for the group, but
	// have no effect on the resources set at the K8s Pod container level.
	// +optional
	Resources map[string]string `json:"resources,omitempty"`
	// Labels specifies the Ray node labels for this worker group.
	// These labels will also be added to the Pods of this worker group and override the `--labels`
	// argument passed to `rayStartParams`.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`
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
	// Optional list overwrite the default command of the autoscaler container.
	// +optional
	Command []string `json:"command,omitempty"`
	// Optional to overwrite the default args of the autoscaler container.
	// +optional
	Args []string `json:"args,omitempty"`
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

type EventReason string

const (
	RayConfigError         EventReason = "RayConfigError"
	PodReconciliationError EventReason = "PodReconciliationError"
)
