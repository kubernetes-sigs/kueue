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
	"k8s.io/apimachinery/pkg/util/intstr"
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
	// The field is immutable.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="runtimeRef is immutable"
	// +required
	RuntimeRef RuntimeRef `json:"runtimeRef,omitzero"`

	// initializer defines the configuration of the initializer.
	// +optional
	Initializer *Initializer `json:"initializer,omitempty"`

	// trainer defines the configuration of the trainer.
	// +optional
	Trainer *Trainer `json:"trainer,omitempty"`

	// labels to apply for the derivative JobSet and Jobs.
	// They will be merged with the TrainingRuntime values.
	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// annotations to apply for the derivative JobSet and Jobs.
	// They will be merged with the TrainingRuntime values.
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// podTemplateOverrides define the PodTemplateOverrides for the training runtime.
	// When multiple overrides apply to the same targetJob, later entries in the slice override earlier field values.
	// +listType=atomic
	// +optional
	PodTemplateOverrides []PodTemplateOverride `json:"podTemplateOverrides,omitempty"`

	// suspend defines whether to suspend the running TrainJob.
	// Defaults to false.
	// +kubebuilder:default=false
	// +optional
	Suspend *bool `json:"suspend,omitempty"`

	// managedBy is used to indicate the controller or entity that manages a TrainJob.
	// The value must be either an empty, `trainer.kubeflow.org/trainjob-controller` or
	// `kueue.x-k8s.io/multikueue`. The built-in TrainJob controller reconciles TrainJob which
	// don't have this field at all or the field value is the reserved string
	// `trainer.kubeflow.org/trainjob-controller`, but delegates reconciling TrainJobs
	// with a 'kueue.x-k8s.io/multikueue' to the Kueue. The field is immutable.
	// Defaults to `trainer.kubeflow.org/trainjob-controller`
	// +kubebuilder:default="trainer.kubeflow.org/trainjob-controller"
	// +kubebuilder:validation:XValidation:rule="self in ['trainer.kubeflow.org/trainjob-controller', 'kueue.x-k8s.io/multikueue']", message="ManagedBy must be trainer.kubeflow.org/trainjob-controller or kueue.x-k8s.io/multikueue if set"
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="ManagedBy value is immutable"
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`
}

// RuntimeRef represents the reference to the existing training runtime.
type RuntimeRef struct {
	// name of the runtime being referenced.
	// When namespaced-scoped TrainingRuntime is used, the TrainJob must have
	// the same namespace as the deployed runtime.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name,omitempty"`

	// apiGroup of the runtime being referenced.
	// Defaults to `trainer.kubeflow.org`.
	// +kubebuilder:default="trainer.kubeflow.org"
	// +optional
	APIGroup *string `json:"apiGroup,omitempty"`

	// kind of the runtime being referenced.
	// Defaults to ClusterTrainingRuntime.
	// +kubebuilder:default="ClusterTrainingRuntime"
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
	// +optional
	StorageUri *string `json:"storageUri,omitempty"`

	// env is the list of environment variables to set in the dataset initializer container.
	// These values will be merged with the TrainingRuntime's dataset initializer environments.
	// +listType=map
	// +listMapKey=name
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
	// +optional
	StorageUri *string `json:"storageUri,omitempty"`

	// env is the list of environment variables to set in the model initializer container.
	// These values will be merged with the TrainingRuntime's model initializer environments.
	// +listType=map
	// +listMapKey=name
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
	// +optional
	Image *string `json:"image,omitempty"`

	// command for the entrypoint of the training container.
	// +listType=atomic
	// +optional
	Command []string `json:"command,omitempty"`

	// args for the entrypoint for the training container.
	// +listType=atomic
	// +optional
	Args []string `json:"args,omitempty"`

	// env is the list of environment variables to set in the training container.
	// These values will be merged with the TrainingRuntime's trainer environments.
	// +listType=map
	// +listMapKey=name
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
	// For the Torch runtime: `auto`, `cpu`, `gpu`, or int value can be set.
	// For the MPI runtime only int value can be set.
	// +optional
	NumProcPerNode *intstr.IntOrString `json:"numProcPerNode,omitempty"`
}

// PodTemplateOverride represents a custom PodTemplateSpec override that will be applied to the TrainJob's training runtime.
type PodTemplateOverride struct {
	// targetJobs is the list of replicated jobs in the training runtime template to apply the overrides.
	// +listType=atomic
	// +required
	TargetJobs []PodTemplateOverrideTargetJob `json:"targetJobs,omitempty"`

	// metadata overrides the Pod template metadata.
	// These values will be merged with the TrainingRuntime's Pod template metadata.
	// +optional
	Metadata *metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec overrides the Pod template spec.
	// These values will be merged with the TrainingRuntime's Pod template spec.
	// +optional
	Spec *PodTemplateSpecOverride `json:"spec,omitempty"`
}

type PodTemplateOverrideTargetJob struct {
	// name is the target training job name for which the PodTemplateSpec is overridden.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name,omitempty"`
}

// PodTemplateSpecOverride represents the spec overrides for Pod template.
type PodTemplateSpecOverride struct {
	// serviceAccountName overrides the service account.
	// +optional
	ServiceAccountName *string `json:"serviceAccountName,omitempty"`

	// nodeSelector overrides the node selector to place Pod on the specific node.
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// affinity overrides for the Pod's affinity.
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`

	// tolerations overrides the Pod's tolerations.
	// +listType=atomic
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// volumes overrides the Pod's volumes.
	// +listType=map
	// +listMapKey=name
	// +optional
	Volumes []corev1.Volume `json:"volumes,omitempty"`

	// initContainers overrides the init container in the target job templates.
	// +listType=map
	// +listMapKey=name
	// +optional
	InitContainers []ContainerOverride `json:"initContainers,omitempty"`

	// containers overrides for the containers in the target job templates.
	// +listType=map
	// +listMapKey=name
	// +optional
	Containers []ContainerOverride `json:"containers,omitempty"`

	// schedulingGates overrides the scheduling gates of the Pods in the target job templates.
	// More info: https://kubernetes.io/docs/concepts/scheduling-eviction/pod-scheduling-readiness/
	// +listType=map
	// +listMapKey=name
	// +optional
	SchedulingGates []corev1.PodSchedulingGate `json:"schedulingGates,omitempty"`

	// imagePullSecrets overrides the image pull secrets for the Pods in the target job templates.
	// +listType=map
	// +listMapKey=name
	// +optional
	ImagePullSecrets []corev1.LocalObjectReference `json:"imagePullSecrets,omitempty"`
}

// ContainerOverride represents parameters that can be overridden using PodSpecOverrides.
type ContainerOverride struct {
	// name for the container. TrainingRuntime must have this container.
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name,omitempty"`

	// env is the list of environment variables to set in the container.
	// These values will be merged with the TrainingRuntime's environments.
	// These values can't be set for container with the name: `node`, `dataset-initializer`, or
	// `model-initializer`. For those containers the envs can only be set via Trainer or Initializer APIs.
	// +listType=map
	// +listMapKey=name
	// +optional
	Env []corev1.EnvVar `json:"env,omitempty"`

	// volumeMounts are the volumes to mount into the container's filesystem.
	// +listType=map
	// +listMapKey=name
	// +optional
	VolumeMounts []corev1.VolumeMount `json:"volumeMounts,omitempty"`
}

// TrainJobStatus represents the current status of TrainJob.
// +kubebuilder:validation:MinProperties=1
type TrainJobStatus struct {
	// conditions for the TrainJob.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// jobsStatus tracks the child Jobs in TrainJob.
	// +listType=map
	// +listMapKey=name
	// +optional
	JobsStatus []JobStatus `json:"jobsStatus,omitempty"`
}

type JobStatus struct {
	// name of the child Job.
	// +kubebuilder:validation:MinLength=1
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

func init() {
	SchemeBuilder.Register(&TrainJob{}, &TrainJobList{})
}
