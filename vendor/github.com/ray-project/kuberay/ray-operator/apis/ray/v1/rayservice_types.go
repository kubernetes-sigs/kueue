package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ServiceStatus string

const (
	// `Running` means the RayService is ready to serve requests. `NotRunning` means it is not ready.
	// The naming is a bit confusing, but to maintain backward compatibility, we use `Running` instead of `Ready`.
	// Since KubeRay v1.3.0, `ServiceStatus` is equivalent to the `RayServiceReady` condition.
	// `ServiceStatus` is deprecated - please use conditions instead.
	Running    ServiceStatus = "Running"
	NotRunning ServiceStatus = ""
)

type RayServiceUpgradeType string

const (
	// During upgrade, NewClusterWithIncrementalUpgrade strategy will create an upgraded cluster to gradually scale
	// and migrate traffic to using Gateway API.
	NewClusterWithIncrementalUpgrade RayServiceUpgradeType = "NewClusterWithIncrementalUpgrade"
	// During upgrade, NewCluster strategy will create new upgraded cluster and switch to it when it becomes ready
	NewCluster RayServiceUpgradeType = "NewCluster"
	// No new cluster will be created while the strategy is set to None
	None RayServiceUpgradeType = "None"
)

// These statuses should match Ray Serve's application statuses
// See `enum ApplicationStatus` in https://sourcegraph.com/github.com/ray-project/ray/-/blob/src/ray/protobuf/serve.proto for more details.
var ApplicationStatusEnum = struct {
	NOT_STARTED   string
	DEPLOYING     string
	RUNNING       string
	DEPLOY_FAILED string
	DELETING      string
	UNHEALTHY     string
}{
	NOT_STARTED:   "NOT_STARTED",
	DEPLOYING:     "DEPLOYING",
	RUNNING:       "RUNNING",
	DEPLOY_FAILED: "DEPLOY_FAILED",
	DELETING:      "DELETING",
	UNHEALTHY:     "UNHEALTHY",
}

// These statuses should match Ray Serve's deployment statuses
var DeploymentStatusEnum = struct {
	UPDATING  string
	HEALTHY   string
	UNHEALTHY string
}{
	UPDATING:  "UPDATING",
	HEALTHY:   "HEALTHY",
	UNHEALTHY: "UNHEALTHY",
}

// These options are currently only supported for the IncrementalUpgrade type.
type ClusterUpgradeOptions struct {
	// The capacity of serve requests the upgraded cluster should scale to handle each interval.
	// Defaults to 100%.
	// +kubebuilder:default:=100
	MaxSurgePercent *int32 `json:"maxSurgePercent,omitempty"`
	// The percentage of traffic to switch to the upgraded RayCluster at a set interval after scaling by MaxSurgePercent.
	StepSizePercent *int32 `json:"stepSizePercent"`
	// The interval in seconds between transferring StepSize traffic from the old to new RayCluster.
	IntervalSeconds *int32 `json:"intervalSeconds"`
	// The name of the Gateway Class installed by the Kubernetes Cluster admin.
	GatewayClassName string `json:"gatewayClassName"`
}

type RayServiceUpgradeStrategy struct {
	// Type represents the strategy used when upgrading the RayService. Currently supports `NewCluster` and `None`.
	// +optional
	Type *RayServiceUpgradeType `json:"type,omitempty"`
	// ClusterUpgradeOptions defines the behavior of a NewClusterWithIncrementalUpgrade type.
	// RayServiceIncrementalUpgrade feature gate must be enabled to set ClusterUpgradeOptions.
	ClusterUpgradeOptions *ClusterUpgradeOptions `json:"clusterUpgradeOptions,omitempty"`
}

// RayServiceSpec defines the desired state of RayService
type RayServiceSpec struct {
	// RayClusterDeletionDelaySeconds specifies the delay, in seconds, before deleting old RayClusters.
	// The default value is 60 seconds.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RayClusterDeletionDelaySeconds *int32 `json:"rayClusterDeletionDelaySeconds,omitempty"`
	// Deprecated: This field is not used anymore. ref: https://github.com/ray-project/kuberay/issues/1685
	// +optional
	ServiceUnhealthySecondThreshold *int32 `json:"serviceUnhealthySecondThreshold,omitempty"`
	// Deprecated: This field is not used anymore. ref: https://github.com/ray-project/kuberay/issues/1685
	// +optional
	DeploymentUnhealthySecondThreshold *int32 `json:"deploymentUnhealthySecondThreshold,omitempty"`
	// ServeService is the Kubernetes service for head node and worker nodes who have healthy http proxy to serve traffics.
	// +optional
	ServeService *corev1.Service `json:"serveService,omitempty"`
	// UpgradeStrategy defines the scaling policy used when upgrading the RayService.
	// +optional
	UpgradeStrategy *RayServiceUpgradeStrategy `json:"upgradeStrategy,omitempty"`
	// Important: Run "make" to regenerate code after modifying this file
	// Defines the applications and deployments to deploy, should be a YAML multi-line scalar string.
	// +optional
	ServeConfigV2  string         `json:"serveConfigV2,omitempty"`
	RayClusterSpec RayClusterSpec `json:"rayClusterConfig"`
	// If the field is set to true, the value of the label `ray.io/serve` on the head Pod should always be false.
	// Therefore, the head Pod's endpoint will not be added to the Kubernetes Serve service.
	// +optional
	ExcludeHeadPodFromServeSvc bool `json:"excludeHeadPodFromServeSvc,omitempty"`
}

// RayServiceStatuses defines the observed state of RayService
type RayServiceStatuses struct {
	// Represents the latest available observations of a RayService's current state.
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
	// LastUpdateTime represents the timestamp when the RayService status was last updated.
	// +optional
	LastUpdateTime *metav1.Time `json:"lastUpdateTime,omitempty"`
	// Deprecated: `ServiceStatus` is deprecated - use `Conditions` instead. `Running` means the RayService is ready to
	// serve requests. An empty `ServiceStatus` means the RayService is not ready to serve requests. The definition of
	// `ServiceStatus` is equivalent to the `RayServiceReady` condition.
	// +optional
	ServiceStatus ServiceStatus `json:"serviceStatus,omitempty"`
	// +optional
	ActiveServiceStatus RayServiceStatus `json:"activeServiceStatus,omitempty"`
	// Pending Service Status indicates a RayCluster will be created or is being created.
	// +optional
	PendingServiceStatus RayServiceStatus `json:"pendingServiceStatus,omitempty"`
	// NumServeEndpoints indicates the number of Ray Pods that are actively serving or have been selected by the serve service.
	// Ray Pods without a proxy actor or those that are unhealthy will not be counted.
	// +optional
	NumServeEndpoints int32 `json:"numServeEndpoints,omitempty"`
	// observedGeneration is the most recent generation observed for this RayService. It corresponds to the
	// RayService's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

type RayServiceStatus struct {
	// Important: Run "make" to regenerate code after modifying this file
	// +optional
	Applications map[string]AppStatus `json:"applicationStatuses,omitempty"`
	// TargetCapacity is the `target_capacity` percentage for all Serve replicas
	// across the cluster for this RayService. The `num_replicas`, `min_replicas`, `max_replicas`,
	// and `initial_replicas` for each deployment will be scaled by this percentage."
	// +optional
	TargetCapacity *int32 `json:"targetCapacity,omitempty"`
	// TrafficRoutedPercent is the percentage of traffic that is routed to the Serve service
	// for this RayService. TrafficRoutedPercent is updated to reflect the weight on the HTTPRoute
	// created for this RayService during incremental upgrades to a new cluster.
	// +optional
	TrafficRoutedPercent *int32 `json:"trafficRoutedPercent,omitempty"`
	// LastTrafficMigratedTime is the last time that TrafficRoutedPercent was updated to a new value
	// for this RayService.
	// +optional
	LastTrafficMigratedTime *metav1.Time `json:"lastTrafficMigratedTime,omitempty"`
	// +optional
	RayClusterName string `json:"rayClusterName,omitempty"`
	// +optional
	RayClusterStatus RayClusterStatus `json:"rayClusterStatus,omitempty"`
}

type AppStatus struct {
	// +optional
	Deployments map[string]ServeDeploymentStatus `json:"serveDeploymentStatuses,omitempty"`
	// +optional
	Status string `json:"status,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

// ServeDeploymentStatus defines the current state of a Serve deployment
type ServeDeploymentStatus struct {
	// +optional
	Status string `json:"status,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
}

type (
	RayServiceConditionType   string
	RayServiceConditionReason string
)

const (
	// RayServiceReady means users can send requests to the underlying cluster and the number of serve endpoints is greater than 0.
	RayServiceReady RayServiceConditionType = "Ready"
	// UpgradeInProgress means the RayService is currently performing a zero-downtime upgrade.
	UpgradeInProgress RayServiceConditionType = "UpgradeInProgress"
)

const (
	RayServiceInitializing         RayServiceConditionReason = "Initializing"
	RayServiceInitializingTimeout  RayServiceConditionReason = "InitializingTimeout"
	ZeroServeEndpoints             RayServiceConditionReason = "ZeroServeEndpoints"
	NonZeroServeEndpoints          RayServiceConditionReason = "NonZeroServeEndpoints"
	BothActivePendingClustersExist RayServiceConditionReason = "BothActivePendingClustersExist"
	NoPendingCluster               RayServiceConditionReason = "NoPendingCluster"
	NoActiveCluster                RayServiceConditionReason = "NoActiveCluster"
)

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=all
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="service status",type=string,JSONPath=".status.serviceStatus"
// +kubebuilder:printcolumn:name="num serve endpoints",type=string,JSONPath=".status.numServeEndpoints"
// +genclient
// RayService is the Schema for the rayservices API
type RayService struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              RayServiceSpec `json:"spec,omitempty"`
	// +optional
	Status RayServiceStatuses `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RayServiceList contains a list of RayService
type RayServiceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RayService `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RayService{}, &RayServiceList{})
}
