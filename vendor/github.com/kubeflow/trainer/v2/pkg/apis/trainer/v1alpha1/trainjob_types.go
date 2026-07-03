/*
Copyright 2024 The Kubeflow Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// TrainJobKind is the Kind name for the TrainJob.
	TrainJobKind string = "TrainJob"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.conditions[-1:].type`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
// +kubebuilder:validation:XValidation:rule="self.metadata.name.matches('^[a-z]([-a-z0-9]*[a-z0-9])?$')", message="metadata.name must match RFC 1035 DNS label format"
// +kubebuilder:validation:XValidation:rule="size(self.metadata.name) <= 63", message="metadata.name must be no more than 63 characters"

// TrainJob represents configuration of a training job.
type TrainJob struct {
	metav1.TypeMeta `json:",inline"`

	// metadata of the TrainJob.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec of the TrainJob.
	// +optional
	Spec TrainJobSpec `json:"spec,omitzero"`

	// status of TrainJob.
	// +optional
	Status TrainJobStatus `json:"status,omitzero"`
}

const (
	// TrainJobSuspended means that TrainJob is suspended.
	TrainJobSuspended string = "Suspended"

	// TrainJobComplete means that the TrainJob has completed its execution.
	TrainJobComplete string = "Complete"

	// TrainJobFailed means that the actual jobs have failed its execution.
	TrainJobFailed string = "Failed"
)

const (
	// TrainJobSuspendedReason is the "Suspended" condition reason
	// when the TrainJob is suspended.
	TrainJobSuspendedReason string = "Suspended"

	// TrainJobResumedReason is the "Suspended" condition reason
	// when the TrainJob suspension is changed from True to False.
	TrainJobResumedReason string = "Resumed"

	// TrainJobRuntimeNotSupportedReason is the "Failed" condition reason
	// when the referenced TrainingRuntime is not supported.
	TrainJobRuntimeNotSupportedReason string = "TrainingRuntimeNotSupported"

	// TrainJobDeadlineExceededReason is the "Failed" condition reason
	// when the TrainJob exceeds its ActiveDeadlineSeconds.
	// Matches the Kubernetes Job behavior.
	TrainJobDeadlineExceededReason string = "DeadlineExceeded"
)

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=trainjobs
// +kubebuilder:object:root=true

// TrainJobList is a collection of training jobs.
type TrainJobList struct {
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of TrainJobs.
	Items []TrainJob `json:"items"`
}

// TrainJobSpec represents specification of the desired TrainJob.
type TrainJobSpec struct {
	// runtimeRef is the reference to the training runtime.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +required
	RuntimeRef RuntimeRef `json:"runtimeRef,omitzero"`

	// initializer defines the configuration of the initializer.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	Initializer *Initializer `json:"initializer,omitempty"`

	// trainer defines the configuration of the trainer.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	Trainer *Trainer `json:"trainer,omitempty"`

	// runtimePatches defines custom patches applied to the TrainJob's Runtime.
	// Patches are keyed by manager to provide clear ownership and avoid conflicts between controllers.
	// +listType=map
	// +listMapKey=manager
	// +kubebuilder:validation:MaxItems=16
	// +optional
	RuntimePatches []RuntimePatch `json:"runtimePatches,omitempty"`

	// suspend defines whether to suspend the running TrainJob.
	// +kubebuilder:default=false
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// activeDeadlineSeconds specifies the duration in seconds relative to the TrainJob
	// start time (which resets on resume from suspension) that the TrainJob may be active
	// before the system tries to terminate it. Value must be a positive integer.
	// Once reached, all running Pods are terminated and the TrainJob status becomes
	// Failed with reason: DeadlineExceeded.
	// +optional
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="field is immutable"
	ActiveDeadlineSeconds int64 `json:"activeDeadlineSeconds,omitempty"`

	// managedBy is used to indicate the controller or entity that manages a TrainJob.
	// The value must be either an empty, `trainer.kubeflow.org/trainjob-controller` or
	// `kueue.x-k8s.io/multikueue`. The built-in TrainJob controller reconciles TrainJob which
	// don't have this field at all or the field value is the reserved string
	// `trainer.kubeflow.org/trainjob-controller`, but delegates reconciling TrainJobs
	// with a 'kueue.x-k8s.io/multikueue' to the Kueue. The field is immutable.
	// +kubebuilder:default="trainer.kubeflow.org/trainjob-controller"
	// +kubebuilder:validation:XValidation:rule="self in ['trainer.kubeflow.org/trainjob-controller', 'kueue.x-k8s.io/multikueue']", message="ManagedBy must be trainer.kubeflow.org/trainjob-controller or kueue.x-k8s.io/multikueue if set"
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`
}

// RuntimeRef represents the reference to the existing training runtime.
type RuntimeRef struct {
	// name of the runtime being referenced.
	// When namespaced-scoped TrainingRuntime is used, the TrainJob must have
	// the same namespace as the deployed runtime.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name,omitempty"`

	// apiGroup of the runtime being referenced.
	// Defaults to `trainer.kubeflow.org`.
	// +kubebuilder:default="trainer.kubeflow.org"
	// +kubebuilder:validation:MaxLength=253
	// +optional
	APIGroup *string `json:"apiGroup,omitempty"`

	// kind of the runtime being referenced.
	// Defaults to ClusterTrainingRuntime.
	// +kubebuilder:default="ClusterTrainingRuntime"
	// +kubebuilder:validation:MaxLength=253
	// +optional
	Kind *string `json:"kind,omitempty"`
}

// Initializer represents the desired configuration for the dataset and model initialization.
// It is used to initialize the assets (dataset and pre-trained model) and pre-process data.
type Initializer struct {
	// dataset defines the configuration for the dataset initialization and pre-processing.
	// +optional
	Dataset *DatasetInitializer `json:"dataset,omitempty"`

	// model defines the configuration for the pre-trained model initialization
	// +optional
	Model *ModelInitializer `json:"model,omitempty"`
}

// DatasetInitializer represents the desired configuration to initialize and pre-process dataset.
// The DatasetInitializer spec will override the runtime Job template
// which contains this label: `trainer.kubeflow.org/trainjob-ancestor-step: dataset-initializer`
type DatasetInitializer struct {
	// storageUri is the URI for the dataset provider.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	StorageUri *string `json:"storageUri,omitempty"`

	// env is the list of environment variables to set in the dataset initializer container.
	// These values will be merged with the TrainingRuntime's dataset initializer environments.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// secretRef is the reference to the secret with credentials to download dataset.
	// Secret must be created in the TrainJob's namespace.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// ModelInitializer represents the desired configuration to initialize pre-trained model.
// The ModelInitializer spec will override the runtime Job template
// which contains this label: `trainer.kubeflow.org/trainjob-ancestor-step: dataset-initializer`
type ModelInitializer struct {
	// storageUri is the URI for the model provider.
	// +kubebuilder:validation:MaxLength=2048
	// +optional
	StorageUri *string `json:"storageUri,omitempty"`

	// env is the list of environment variables to set in the model initializer container.
	// These values will be merged with the TrainingRuntime's model initializer environments.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// secretRef is the reference to the secret with credentials to download model.
	// Secret must be created in the TrainJob's namespace.
	// +optional
	SecretRef *corev1.LocalObjectReference `json:"secretRef,omitempty"`
}

// Trainer represents the desired configuration for the training job.
// The Trainer spec will override the runtime template
// which contains this label: `trainer.kubeflow.org/trainjob-ancestor-step: trainer`
type Trainer struct {
	// image is the container image for the training container.
	// +kubebuilder:validation:MaxLength=500
	// +optional
	Image *string `json:"image,omitempty"`

	// command for the entrypoint of the training container.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:items:MaxLength=65536
	// +optional
	Command []string `json:"command,omitempty"`

	// args for the entrypoint for the training container.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:items:MaxLength=65536
	// +optional
	Args []string `json:"args,omitempty"`

	// env is the list of environment variables to set in the training container.
	// These values will be merged with the TrainingRuntime's trainer environments.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// numNodes is the number of training nodes.
	// TODO (andreyvelich): Do we want to support dynamic num of nodes in TrainJob for PyTorch elastic: `--nnodes=1:4` ?
	// +optional
	NumNodes *int32 `json:"numNodes,omitempty"`

	// resourcesPerNode defines the compute resources for each training node.
	// +optional
	ResourcesPerNode *corev1.ResourceRequirements `json:"resourcesPerNode,omitempty"`

	// numProcPerNode is the number of processes/workers/slots on every training node.
	// For the MPI runtime only int value can be set to represent number of slots per node.
	// For the Torch runtime the value defaults to `auto` and can be overridden with an int.
	// +optional
	NumProcPerNode *int32 `json:"numProcPerNode,omitempty"`
}

// RuntimePatch represents a custom patch applied to the TrainJob's training runtime template.
// Patches are keyed by manager to provide clear ownership and avoid conflicts between controllers.
type RuntimePatch struct {
	// manager indicates who owns this patch entry. It can be set by the user, external
	// controllers, or admission webhooks to track ownership and avoid conflicts.
	// For example, Kueue sets this field to "kueue.x-k8s.io/manager".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +required
	Manager string `json:"manager,omitempty"`

	// time is the timestamp of when this patch was last written.
	// Set by the Trainer admission webhook on each create or update. Used for observability only,
	// not used as a list map key.
	// +optional
	Time *metav1.Time `json:"time,omitempty"`

	// trainingRuntimeSpec defines allowed patches for ClusterTrainingRuntime or TrainingRuntime-based jobs.
	// +optional
	TrainingRuntimeSpec *TrainingRuntimeSpecPatch `json:"trainingRuntimeSpec,omitempty"`
}

// TrainingRuntimeSpecPatch mirrors TrainingRuntimeSpec but only exposes
// the fields managers are permitted to patch.
type TrainingRuntimeSpecPatch struct {
	// template patches the JobSet template.
	// +optional
	Template *JobSetTemplatePatch `json:"template,omitempty"`
}

// JobSetTemplatePatch defines patches for the JobSet template.
// It mirrors JobSetTemplateSpec but only exposes metadata and restricted spec fields.
type JobSetTemplatePatch struct {
	// metadata patches the JobSet object metadata.
	// Only labels and annotations are allowed.
	// +optional
	Metadata *metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec patches the JobSet spec with restricted fields.
	// +optional
	Spec *JobSetSpecPatch `json:"spec,omitempty"`
}

// JobSetSpecPatch defines allowed patches for the JobSet spec.
type JobSetSpecPatch struct {
	// replicatedJobs defines per-job patches, keyed by job name.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	// +optional
	ReplicatedJobs []ReplicatedJobPatch `json:"replicatedJobs,omitempty"`
}

// ReplicatedJobPatch defines patches for a specific replicated job within the JobSet.
type ReplicatedJobPatch struct {
	// name is the name of the replicated job to patch.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name,omitempty"`

	// template patches the Job template for this replicated job.
	// +optional
	Template *JobTemplatePatch `json:"template,omitempty"`
}

// JobTemplatePatch defines patches for a Job template within a replicated job.
type JobTemplatePatch struct {
	// metadata patches the Job template metadata.
	// Only labels and annotations are allowed.
	// +optional
	Metadata *metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec patches the Job spec with restricted fields.
	// +optional
	Spec *JobSpecPatch `json:"spec,omitempty"`
}

// JobSpecPatch defines allowed patches for the Job spec.
type JobSpecPatch struct {
	// template patches the Pod template for this Job.
	// +optional
	Template *PodTemplatePatch `json:"template,omitempty"`
}

// PodTemplatePatch defines patches for a Pod template within a Job.
type PodTemplatePatch struct {
	// metadata patches the Pod template metadata.
	// Only labels and annotations are allowed.
	// +optional
	Metadata *metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec patches the Pod spec with the fields managers are permitted to set.
	// +optional
	Spec *PodSpecPatch `json:"spec,omitempty"`
}

// PodSpecPatch contains the Pod spec fields that managers are permitted to patch.
type PodSpecPatch struct {
	// serviceAccountName patches the service account for the Pods in the target job templates.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	ServiceAccountName *string `json:"serviceAccountName,omitempty"`

	// volumes patches the Pod's volumes.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// initContainers patches the init containers in the target job templates.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	InitContainers []ContainerPatch `json:"initContainers,omitempty"`

	// containers patches specific containers in the target job templates.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	Containers []ContainerPatch `json:"containers,omitempty"`

	// imagePullSecrets patches the image pull secrets for the Pods in the target job templates.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`

	// securityContext patches the Pod's security context.
	// More info: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
	SecurityContext *corev1.PodSecurityContext `json:"securityContext,omitempty"`

	// nodeSelector patches the node selector to place Pods on specific nodes.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// affinity patches the Pod's scheduling affinity.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// tolerations patches the Pod's tolerations.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// schedulingGates patches the scheduling gates for the Pods in the target job templates.
	// More info: https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	SchedulingGates []corev1.PodSchedulingGate `json:"schedulingGates,omitempty"`
}

// ContainerPatch represents parameters that can be patched using PodSpecPatch.
type ContainerPatch struct {
	// name for the container. Runtime must have this container.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name,omitempty"`

	// env is the list of environment variables to set in the container.
	// These values will be merged with the Runtime's environments.
	// These values can't be set for container with the name: `node`, `dataset-initializer`, or
	// `model-initializer`. For those containers the envs can only be set via Trainer or Initializer APIs.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// volumeMounts are the volumes to mount into the container's filesystem.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`

	// securityContext patches the container's security context.
	// More info: https://kubernetes.io/docs/tasks/configure-pod-container/security-context/
	// +optional
	SecurityContext *corev1.SecurityContext `json:"securityContext,omitempty"`
}

// TrainJobStatus represents the current status of TrainJob.
// +kubebuilder:validation:MinProperties=1
type TrainJobStatus struct {
	// conditions for the TrainJob.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=8
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// jobsStatus tracks the child Jobs in TrainJob.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=100
	// +optional
	JobsStatus []JobStatus `json:"jobsStatus,omitempty"`

	// trainerStatus contains the latest observed runtime status of the
	// Trainer step of the TrainJob. It reflects progress, remaining time,
	// metrics, and the last update timestamp.
	//
	// This field is nil if the TrainJob does not report trainer-level
	// status, or if no status has been observed yet (for example,
	// immediately after the TrainJob is created).
	//
	// This is an alpha feature and requires enabling the TrainJobStatus feature gate.
	// +optional
	TrainerStatus *TrainerStatus `json:"trainerStatus,omitempty"`
}

type JobStatus struct {
	// name of the child Job.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name,omitempty"`

	// ready is the number of child Jobs where the number of ready pods and completed pods
	// is greater than or equal to the total expected pod count for the child Job.
	// +kubebuilder:validation:Minimum=0
	// +required
	Ready *int32 `json:"ready,omitempty"`

	// succeeded is the number of successfully completed child Jobs.
	// +kubebuilder:validation:Minimum=0
	// +required
	Succeeded *int32 `json:"succeeded,omitempty"`

	// failed is the number of failed child Jobs.
	// +kubebuilder:validation:Minimum=0
	// +required
	Failed *int32 `json:"failed,omitempty"`

	// active is the number of child Jobs with at least 1 pod in a running or pending state
	// which are not marked for deletion.
	// +kubebuilder:validation:Minimum=0
	// +required
	Active *int32 `json:"active,omitempty"`

	// suspended is the number of child Jobs which are in a suspended state.
	// +kubebuilder:validation:Minimum=0
	// +required
	Suspended *int32 `json:"suspended,omitempty"`
}

// TrainerStatus represents the latest known runtime status of the Trainer step of the TrainJob.
// +kubebuilder:validation:XValidation:rule="has(self.lastUpdatedTime)",message="lastUpdatedTime is required when trainerStatus is present"
type TrainerStatus struct {

	// progressPercentage gives an estimate of how complete the TrainJob is as a percentage.
	// The value will be between 0 and 100, or empty if unknown.
	//
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	ProgressPercentage *int32 `json:"progressPercentage,omitempty"`

	// estimatedRemainingSeconds gives the estimated remaining training time in seconds
	// before the train job is completed.
	// The value will be empty if it is unknown.
	//
	// +kubebuilder:validation:Minimum=0
	// +optional
	EstimatedRemainingSeconds *int32 `json:"estimatedRemainingSeconds,omitempty"`

	// metrics contains the current metrics for the model.
	//
	// +kubebuilder:validation:MaxItems=256
	// +listType=atomic
	// +optional
	Metrics []Metric `json:"metrics,omitempty"`

	// lastUpdatedTime is the timestamp when the runtime status was observed.
	// +optional
	LastUpdatedTime metav1.Time `json:"lastUpdatedTime,omitempty"`
}

type Metric struct {
	// name is a user-defined label for the metric, e.g. "loss", "eval_accuracy".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Name string `json:"name,omitempty"`

	// value of the metric. Values must be serialized as a string.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +required
	Value string `json:"value,omitempty"`
}

// UpdateTrainJobStatusRequest contains the current runtime status (e.g. progress and metrics) for the different stages of the
// TrainJob.
type UpdateTrainJobStatusRequest struct {
	// trainerStatus contains the latest observed runtime status of the
	// Trainer step of the TrainJob. It reflects progress, remaining time,
	// metrics, and the last update timestamp.
	//
	// This field is nil if the TrainJob does not report trainer-level
	// status, or if no status has been observed yet (for example,
	// immediately after the TrainJob is created).
	//
	// This is an alpha feature and requires enabling the TrainJobStatus feature gate.
	// +optional
	TrainerStatus *TrainerStatus `json:"trainerStatus,omitempty"`
}
