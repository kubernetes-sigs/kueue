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
const (
	JobStatusNew       JobStatus = ""
	JobStatusPending   JobStatus = "PENDING"
	JobStatusRunning   JobStatus = "RUNNING"
	JobStatusStopped   JobStatus = "STOPPED"
	JobStatusSucceeded JobStatus = "SUCCEEDED"
	JobStatusFailed    JobStatus = "FAILED"
)

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
	JobDeploymentStatusNew          JobDeploymentStatus = ""
	JobDeploymentStatusInitializing JobDeploymentStatus = "Initializing"
	JobDeploymentStatusRunning      JobDeploymentStatus = "Running"
	JobDeploymentStatusComplete     JobDeploymentStatus = "Complete"
	JobDeploymentStatusFailed       JobDeploymentStatus = "Failed"
	JobDeploymentStatusSuspending   JobDeploymentStatus = "Suspending"
	JobDeploymentStatusSuspended    JobDeploymentStatus = "Suspended"
	JobDeploymentStatusRetrying     JobDeploymentStatus = "Retrying"
	JobDeploymentStatusWaiting      JobDeploymentStatus = "Waiting"
)

// JobFailedReason indicates the reason the RayJob changes its JobDeploymentStatus to 'Failed'
type JobFailedReason string

const (
	SubmissionFailed JobFailedReason = "SubmissionFailed"
	DeadlineExceeded JobFailedReason = "DeadlineExceeded"
	AppFailed        JobFailedReason = "AppFailed"
)

type JobSubmissionMode string

const (
	K8sJobMode JobSubmissionMode = "K8sJobMode" // Submit job via Kubernetes Job
	HTTPMode   JobSubmissionMode = "HTTPMode"   // Submit job via HTTP request
	UserMode   JobSubmissionMode = "UserMode"   // Don't submit job in KubeRay. Instead, wait for user to submit job and provide the job submission ID
)

type SubmitterConfig struct {
	// BackoffLimit of the submitter k8s job.
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
}

// RayJobSpec defines the desired state of RayJob
type RayJobSpec struct {
	// ActiveDeadlineSeconds is the duration in seconds that the RayJob may be active before
	// KubeRay actively tries to terminate the RayJob; value must be positive integer.
	ActiveDeadlineSeconds *int32 `json:"activeDeadlineSeconds,omitempty"`
	// Specifies the number of retries before marking this job failed.
	// Each retry creates a new RayCluster.
	// +kubebuilder:default:=0
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`
	// RayClusterSpec is the cluster template to run the job
	RayClusterSpec *RayClusterSpec `json:"rayClusterSpec,omitempty"`
	// SubmitterPodTemplate is the template for the pod that will run `ray job submit`.
	SubmitterPodTemplate *corev1.PodTemplateSpec `json:"submitterPodTemplate,omitempty"`
	// Metadata is data to store along with this job.
	Metadata map[string]string `json:"metadata,omitempty"`
	// clusterSelector is used to select running rayclusters by labels
	ClusterSelector map[string]string `json:"clusterSelector,omitempty"`
	// Configurations of submitter k8s job.
	SubmitterConfig *SubmitterConfig `json:"submitterConfig,omitempty"`
	// INSERT ADDITIONAL SPEC FIELDS - desired state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	Entrypoint string `json:"entrypoint,omitempty"`
	// RuntimeEnvYAML represents the runtime environment configuration
	// provided as a multi-line YAML string.
	RuntimeEnvYAML string `json:"runtimeEnvYAML,omitempty"`
	// If jobId is not set, a new jobId will be auto-generated.
	JobId string `json:"jobId,omitempty"`
	// SubmissionMode specifies how RayJob submits the Ray job to the RayCluster.
	// In "K8sJobMode", the KubeRay operator creates a submitter Kubernetes Job to submit the Ray job.
	// In "HTTPMode", the KubeRay operator sends a request to the RayCluster to create a Ray job.
	// +kubebuilder:default:=K8sJobMode
	SubmissionMode JobSubmissionMode `json:"submissionMode,omitempty"`
	// EntrypointResources specifies the custom resources and quantities to reserve for the
	// entrypoint command.
	EntrypointResources string `json:"entrypointResources,omitempty"`
	// EntrypointNumCpus specifies the number of cpus to reserve for the entrypoint command.
	EntrypointNumCpus float32 `json:"entrypointNumCpus,omitempty"`
	// EntrypointNumGpus specifies the number of gpus to reserve for the entrypoint command.
	EntrypointNumGpus float32 `json:"entrypointNumGpus,omitempty"`
	// TTLSecondsAfterFinished is the TTL to clean up RayCluster.
	// It's only working when ShutdownAfterJobFinishes set to true.
	// +kubebuilder:default:=0
	TTLSecondsAfterFinished int32 `json:"ttlSecondsAfterFinished,omitempty"`
	// ShutdownAfterJobFinishes will determine whether to delete the ray cluster once rayJob succeed or failed.
	ShutdownAfterJobFinishes bool `json:"shutdownAfterJobFinishes,omitempty"`
	// suspend specifies whether the RayJob controller should create a RayCluster instance
	// If a job is applied with the suspend field set to true,
	// the RayCluster will not be created and will wait for the transition to false.
	// If the RayCluster is already created, it will be deleted.
	// In case of transition to false a new RayCluster will be created.
	Suspend bool `json:"suspend,omitempty"`
}

// RayJobStatus defines the observed state of RayJob
type RayJobStatus struct {
	// INSERT ADDITIONAL STATUS FIELD - define observed state of cluster
	// Important: Run "make" to regenerate code after modifying this file
	JobId               string              `json:"jobId,omitempty"`
	RayClusterName      string              `json:"rayClusterName,omitempty"`
	DashboardURL        string              `json:"dashboardURL,omitempty"`
	JobStatus           JobStatus           `json:"jobStatus,omitempty"`
	JobDeploymentStatus JobDeploymentStatus `json:"jobDeploymentStatus,omitempty"`
	Reason              JobFailedReason     `json:"reason,omitempty"`
	Message             string              `json:"message,omitempty"`
	// StartTime is the time when JobDeploymentStatus transitioned from 'New' to 'Initializing'.
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// EndTime is the time when JobDeploymentStatus transitioned to 'Complete' status.
	// This occurs when the Ray job reaches a terminal state (SUCCEEDED, FAILED, STOPPED)
	// or the submitter Job has failed.
	EndTime *metav1.Time `json:"endTime,omitempty"`
	// Succeeded is the number of times this job succeeded.
	// +kubebuilder:default:=0
	Succeeded *int32 `json:"succeeded,omitempty"`
	// Failed is the number of times this job failed.
	// +kubebuilder:default:=0
	Failed *int32 `json:"failed,omitempty"`
	// RayClusterStatus is the status of the RayCluster running the job.
	RayClusterStatus RayClusterStatus `json:"rayClusterStatus,omitempty"`

	// observedGeneration is the most recent generation observed for this RayJob. It corresponds to the
	// RayJob's generation, which is updated on mutation by the API Server.
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

	Spec   RayJobSpec   `json:"spec,omitempty"`
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
