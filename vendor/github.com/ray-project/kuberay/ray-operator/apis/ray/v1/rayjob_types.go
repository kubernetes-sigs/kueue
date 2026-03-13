package v1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// JobStatus is the Ray Job Status.
type JobStatus string

// https://docs.ray.io/en/latest/cluster/running-applications/job-submission/jobs-package-ref.html#jobstatus
//
// NOTICE: [AllJobStatuses] should be kept in sync with all job statuses below.
const (
	JobStatusNew       JobStatus = ""
	JobStatusPending   JobStatus = "PENDING"
	JobStatusRunning   JobStatus = "RUNNING"
	JobStatusStopped   JobStatus = "STOPPED"
	JobStatusSucceeded JobStatus = "SUCCEEDED"
	JobStatusFailed    JobStatus = "FAILED"
)

var AllJobStatuses = []JobStatus{
	JobStatusNew,
	JobStatusPending,
	JobStatusRunning,
	JobStatusStopped,
	JobStatusSucceeded,
	JobStatusFailed,
}

// This function should be synchronized with the function `is_terminal()` in Ray Job.
func IsJobTerminal(status JobStatus) bool {
	terminalStatusSet := map[JobStatus]struct{}{
		JobStatusStopped: {}, JobStatusSucceeded: {}, JobStatusFailed: {},
	}
	_, ok := terminalStatusSet[status]
	return ok
}

// JobDeploymentStatus indicates RayJob status including RayCluster lifecycle management and Job submission
type JobDeploymentStatus string

const (
	JobDeploymentStatusNew              JobDeploymentStatus = ""
	JobDeploymentStatusInitializing     JobDeploymentStatus = "Initializing"
	JobDeploymentStatusRunning          JobDeploymentStatus = "Running"
	JobDeploymentStatusComplete         JobDeploymentStatus = "Complete"
	JobDeploymentStatusFailed           JobDeploymentStatus = "Failed"
	JobDeploymentStatusValidationFailed JobDeploymentStatus = "ValidationFailed"
	JobDeploymentStatusSuspending       JobDeploymentStatus = "Suspending"
	JobDeploymentStatusSuspended        JobDeploymentStatus = "Suspended"
	JobDeploymentStatusRetrying         JobDeploymentStatus = "Retrying"
	JobDeploymentStatusWaiting          JobDeploymentStatus = "Waiting"
)

// IsJobDeploymentTerminal returns true if the given JobDeploymentStatus
// is in a terminal state. Terminal states are either Complete or Failed.
func IsJobDeploymentTerminal(status JobDeploymentStatus) bool {
	terminalStatusSet := map[JobDeploymentStatus]struct{}{
		JobDeploymentStatusComplete: {}, JobDeploymentStatusFailed: {},
	}
	_, ok := terminalStatusSet[status]
	return ok
}

// JobFailedReason indicates the reason the RayJob changes its JobDeploymentStatus to 'Failed'
type JobFailedReason string

const (
	SubmissionFailed                                 JobFailedReason = "SubmissionFailed"
	DeadlineExceeded                                 JobFailedReason = "DeadlineExceeded"
	AppFailed                                        JobFailedReason = "AppFailed"
	JobDeploymentStatusTransitionGracePeriodExceeded JobFailedReason = "JobDeploymentStatusTransitionGracePeriodExceeded"
	ValidationFailed                                 JobFailedReason = "ValidationFailed"
)

type JobSubmissionMode string

const (
	K8sJobMode      JobSubmissionMode = "K8sJobMode"      // Submit job via Kubernetes Job
	HTTPMode        JobSubmissionMode = "HTTPMode"        // Submit job via HTTP request
	InteractiveMode JobSubmissionMode = "InteractiveMode" // Don't submit job in KubeRay. Instead, wait for user to submit job and provide the job submission ID.
	SidecarMode     JobSubmissionMode = "SidecarMode"     // Submit job via a sidecar container in the Ray head Pod
)

// DeletionStrategy configures automated cleanup after the RayJob reaches a terminal state.
// Two mutually exclusive styles are supported:
//
//	Legacy: provide both onSuccess and onFailure (deprecated; removal planned for 1.6.0). May be combined with shutdownAfterJobFinishes and (optionally) global TTLSecondsAfterFinished.
//	Rules: provide deletionRules (non-empty list). Rules mode is incompatible with shutdownAfterJobFinishes, legacy fields, and the global TTLSecondsAfterFinished (use per‑rule condition.ttlSeconds instead).
//
// Semantics:
//   - A non-empty deletionRules selects rules mode; empty lists are treated as unset.
//   - Legacy requires both onSuccess and onFailure; specifying only one is invalid.
//   - Global TTLSecondsAfterFinished > 0 requires shutdownAfterJobFinishes=true; therefore it cannot be used with rules mode or with legacy alone (no shutdown).
//   - Feature gate RayJobDeletionPolicy must be enabled when this block is present.
//
// Validation:
//   - CRD XValidations prevent mixing legacy fields with deletionRules and enforce legacy completeness.
//   - Controller logic enforces rules vs shutdown exclusivity and TTL constraints.
//   - onSuccess/onFailure are deprecated; migration to deletionRules is encouraged.
//
// +kubebuilder:validation:XValidation:rule="!((has(self.onSuccess) || has(self.onFailure)) && has(self.deletionRules))",message="legacy policies (onSuccess/onFailure) and deletionRules cannot be used together within the same deletionStrategy"
// +kubebuilder:validation:XValidation:rule="((has(self.onSuccess) && has(self.onFailure)) || has(self.deletionRules))",message="deletionStrategy requires either BOTH onSuccess and onFailure, OR the deletionRules field (cannot be empty)"
type DeletionStrategy struct {
	// OnSuccess is the deletion policy for a successful RayJob.
	// Deprecated: Use `deletionRules` instead for more flexible, multi-stage deletion strategies.
	// This field will be removed in release 1.6.0.
	// +optional
	OnSuccess *DeletionPolicy `json:"onSuccess,omitempty"`

	// OnFailure is the deletion policy for a failed RayJob.
	// Deprecated: Use `deletionRules` instead for more flexible, multi-stage deletion strategies.
	// This field will be removed in release 1.6.0.
	// +optional
	OnFailure *DeletionPolicy `json:"onFailure,omitempty"`

	// DeletionRules is a list of deletion rules, processed based on their trigger conditions.
	// While the rules can be used to define a sequence, if multiple rules are overdue (e.g., due to controller downtime),
	// the most impactful rule (e.g., DeleteSelf) will be executed first to prioritize resource cleanup.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	DeletionRules []DeletionRule `json:"deletionRules,omitempty"`
}

// DeletionRule defines a single deletion action and its trigger condition.
// This is the new, recommended way to define deletion behavior.
type DeletionRule struct {
	// Policy is the action to take when the condition is met. This field is required.
	// +kubebuilder:validation:Enum=DeleteCluster;DeleteWorkers;DeleteSelf;DeleteNone
	Policy DeletionPolicyType `json:"policy"`

	// The condition under which this deletion rule is triggered. This field is required.
	Condition DeletionCondition `json:"condition"`
}

// DeletionCondition specifies the trigger conditions for a deletion action.
// Exactly one of JobStatus or JobDeploymentStatus must be specified:
//   - JobStatus (application-level): Match the Ray job execution status.
//   - JobDeploymentStatus (infrastructure-level): Match the RayJob deployment lifecycle status. This is particularly useful for cleaning up resources when Ray jobs fail to be submitted.
//
// +kubebuilder:validation:XValidation:rule="!(has(self.jobStatus) && has(self.jobDeploymentStatus))",message="JobStatus and JobDeploymentStatus cannot be used together within the same deletion condition."
// +kubebuilder:validation:XValidation:rule="has(self.jobStatus) || has(self.jobDeploymentStatus)",message="the deletion condition requires either the JobStatus or the JobDeploymentStatus field."
type DeletionCondition struct {
	// JobStatus is the terminal status of the RayJob that triggers this condition.
	// For the initial implementation, only "SUCCEEDED" and "FAILED" are supported.
	// +kubebuilder:validation:Enum=SUCCEEDED;FAILED
	// +optional
	JobStatus *JobStatus `json:"jobStatus,omitempty"`

	// JobDeploymentStatus is the terminal status of the RayJob deployment that triggers this condition.
	// For the initial implementation, only "Failed" is supported.
	// +kubebuilder:validation:Enum=Failed
	// +optional
	JobDeploymentStatus *JobDeploymentStatus `json:"jobDeploymentStatus,omitempty"`

	// TTLSeconds is the time in seconds from when the JobStatus or JobDeploymentStatus
	// reaches the specified terminal state to when this deletion action should be triggered.
	// The value must be a non-negative integer.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTLSeconds int32 `json:"ttlSeconds,omitempty"`
}

// DeletionPolicy is the legacy single-stage deletion policy.
// Deprecated: This struct is part of the legacy API. Use DeletionRule for new configurations.
type DeletionPolicy struct {
	// Policy is the action to take when the condition is met.
	// This field is logically required when using the legacy OnSuccess/OnFailure policies.
	// It is marked as '+optional' at the API level to allow the 'deletionRules' field to be used instead.
	// +kubebuilder:validation:Enum=DeleteCluster;DeleteWorkers;DeleteSelf;DeleteNone
	// +optional
	Policy *DeletionPolicyType `json:"policy,omitempty"`
}

type DeletionPolicyType string

const (
	DeleteCluster DeletionPolicyType = "DeleteCluster" // To delete the entire RayCluster custom resource on job completion.
	DeleteWorkers DeletionPolicyType = "DeleteWorkers" // To delete only the workers on job completion.
	DeleteSelf    DeletionPolicyType = "DeleteSelf"    // To delete the RayJob custom resource (and all associated resources) on job completion.
	DeleteNone    DeletionPolicyType = "DeleteNone"    // To delete no resources on job completion.
)

type SubmitterConfig struct {
	// BackoffLimit of the submitter k8s job.
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// `RayJobStatusInfo` is a subset of `RayJobInfo` from `dashboard_httpclient.py`.
// This subset is used to store information in the CR status.
//
// TODO(kevin85421): We can consider exposing the whole `RayJobInfo` in the CR status
// after careful consideration. In that case, we can remove `RayJobStatusInfo`.
type RayJobStatusInfo struct {
	StartTime *metav1.Time `json:"startTime,omitempty"`
	EndTime   *metav1.Time `json:"endTime,omitempty"`
}

// RayJobSpec defines the desired state of RayJob
type RayJobSpec struct {
	// ActiveDeadlineSeconds is the duration in seconds that the RayJob may be active before
	// KubeRay actively tries to terminate the RayJob; value must be positive integer.
	// +optional
	ActiveDeadlineSeconds *int32 `json:"activeDeadlineSeconds,omitempty"`
	// Specifies the number of retries before marking this job failed.
	// Each retry creates a new RayCluster.
	// +kubebuilder:default:=0
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
	// RayClusterSpec is the cluster template to run the job
	RayClusterSpec *RayClusterSpec `json:"rayClusterSpec,omitempty"`
	// SubmitterPodTemplate is the template for the pod that will run `ray job submit`.
	// +optional
	SubmitterPodTemplate *corev1.PodTemplateSpec `json:"submitterPodTemplate,omitempty"`
	// Metadata is data to store along with this job.
	// +optional
	Metadata map[string]string `json:"metadata,omitempty"`
	// clusterSelector is used to select running rayclusters by labels
	// +optional
	ClusterSelector map[string]string `json:"clusterSelector,omitempty"`
	// Configurations of submitter k8s job.
	// +optional
	SubmitterConfig *SubmitterConfig `json:"submitterConfig,omitempty"`
	// ManagedBy is an optional configuration for the controller or entity that manages a RayJob.
	// The value must be either 'ray.io/kuberay-operator' or 'kueue.x-k8s.io/multikueue'.
	// The kuberay-operator reconciles a RayJob which doesn't have this field at all or
	// the field value is the reserved string 'ray.io/kuberay-operator',
	// but delegates reconciling the RayJob with 'kueue.x-k8s.io/multikueue' to the Kueue.
	// The field is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="the managedBy field is immutable"
	// +kubebuilder:validation:XValidation:rule="self in ['ray.io/kuberay-operator', 'kueue.x-k8s.io/multikueue']",message="the managedBy field value must be either 'ray.io/kuberay-operator' or 'kueue.x-k8s.io/multikueue'"
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`
	// DeletionStrategy automates post-completion cleanup.
	// Choose one style or omit:
	//   - Legacy: both onSuccess & onFailure (deprecated; may combine with shutdownAfterJobFinishes and TTLSecondsAfterFinished).
	//   - Rules: deletionRules (non-empty) — incompatible with shutdownAfterJobFinishes, legacy fields, and global TTLSecondsAfterFinished (use per-rule condition.ttlSeconds).
	// Global TTLSecondsAfterFinished > 0 requires shutdownAfterJobFinishes=true.
	// Feature gate RayJobDeletionPolicy must be enabled when this field is set.
	// +optional
	DeletionStrategy *DeletionStrategy `json:"deletionStrategy,omitempty"`
	// Entrypoint represents the command to start execution.
	// +optional
	Entrypoint string `json:"entrypoint,omitempty"`
	// RuntimeEnvYAML represents the runtime environment configuration
	// provided as a multi-line YAML string.
	// +optional
	RuntimeEnvYAML string `json:"runtimeEnvYAML,omitempty"`
	// If jobId is not set, a new jobId will be auto-generated.
	// +optional
	JobId string `json:"jobId,omitempty"`
	// SubmissionMode specifies how RayJob submits the Ray job to the RayCluster.
	// In "K8sJobMode", the KubeRay operator creates a submitter Kubernetes Job to submit the Ray job.
	// In "HTTPMode", the KubeRay operator sends a request to the RayCluster to create a Ray job.
	// In "InteractiveMode", the KubeRay operator waits for a user to submit a job to the Ray cluster.
	// In "SidecarMode", the KubeRay operator injects a container into the Ray head Pod that acts as the job submitter to submit the Ray job.
	// +kubebuilder:default:=K8sJobMode
	// +optional
	SubmissionMode JobSubmissionMode `json:"submissionMode,omitempty"`
	// EntrypointResources specifies the custom resources and quantities to reserve for the
	// entrypoint command.
	// +optional
	EntrypointResources string `json:"entrypointResources,omitempty"`
	// EntrypointNumCpus specifies the number of cpus to reserve for the entrypoint command.
	// +optional
	EntrypointNumCpus float32 `json:"entrypointNumCpus,omitempty"`
	// EntrypointNumGpus specifies the number of gpus to reserve for the entrypoint command.
	// +optional
	EntrypointNumGpus float32 `json:"entrypointNumGpus,omitempty"`
	// TTLSecondsAfterFinished is the TTL to clean up RayCluster.
	// It's only working when ShutdownAfterJobFinishes set to true.
	// +kubebuilder:default:=0
	// +optional
	TTLSecondsAfterFinished int32 `json:"ttlSecondsAfterFinished,omitempty"`
	// ShutdownAfterJobFinishes will determine whether to delete the ray cluster once rayJob succeed or failed.
	// +optional
	ShutdownAfterJobFinishes bool `json:"shutdownAfterJobFinishes,omitempty"`
	// suspend specifies whether the RayJob controller should create a RayCluster instance
	// If a job is applied with the suspend field set to true,
	// the RayCluster will not be created and will wait for the transition to false.
	// If the RayCluster is already created, it will be deleted.
	// In case of transition to false a new RayCluster will be created.
	// +optional
	Suspend bool `json:"suspend,omitempty"`
}

// RayJobStatus defines the observed state of RayJob
type RayJobStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	// RayJobStatusInfo contains information about the Ray job retrieved from the Ray dashboard.
	// +optional
	RayJobStatusInfo RayJobStatusInfo `json:"rayJobInfo,omitempty"`
	// +optional
	JobId string `json:"jobId,omitempty"`
	// +optional
	RayClusterName string `json:"rayClusterName,omitempty"`
	// +optional
	DashboardURL string `json:"dashboardURL,omitempty"`
	// +optional
	JobStatus JobStatus `json:"jobStatus,omitempty"`
	// +optional
	JobDeploymentStatus JobDeploymentStatus `json:"jobDeploymentStatus,omitempty"`
	// +optional
	Reason JobFailedReason `json:"reason,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// StartTime is the time when JobDeploymentStatus transitioned from 'New' to 'Initializing'.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// EndTime is the time when JobDeploymentStatus transitioned to 'Complete' status.
	// This occurs when the Ray job reaches a terminal state (SUCCEEDED, FAILED, STOPPED)
	// or the submitter Job has failed.
	// +optional
	EndTime *metav1.Time `json:"endTime,omitempty"`
	// Succeeded is the number of times this job succeeded.
	// +kubebuilder:default:=0
	// +optional
	Succeeded *int32 `json:"succeeded,omitempty"`
	// Failed is the number of times this job failed.
	// +kubebuilder:default:=0
	// +optional
	Failed *int32 `json:"failed,omitempty"`
	// RayClusterStatus is the status of the RayCluster running the job.
	// +optional
	RayClusterStatus RayClusterStatus `json:"rayClusterStatus,omitempty"`

	// observedGeneration is the most recent generation observed for this RayJob. It corresponds to the
	// RayJob's generation, which is updated on mutation by the API Server.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=all
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="job status",type=string,JSONPath=".status.jobStatus",priority=0
// +kubebuilder:printcolumn:name="deployment status",type=string,JSONPath=".status.jobDeploymentStatus",priority=0
// +kubebuilder:printcolumn:name="ray cluster name",type="string",JSONPath=".status.rayClusterName",priority=0
// +kubebuilder:printcolumn:name="start time",type=string,JSONPath=".status.startTime",priority=0
// +kubebuilder:printcolumn:name="end time",type=string,JSONPath=".status.endTime",priority=0
// +kubebuilder:printcolumn:name="age",type="date",JSONPath=".metadata.creationTimestamp",priority=0
// +genclient
// RayJob is the Schema for the rayjobs API
type RayJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec RayJobSpec `json:"spec,omitempty"`
	// +optional
	Status RayJobStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// RayJobList contains a list of RayJob
type RayJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RayJob `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RayJob{}, &RayJobList{})
}
