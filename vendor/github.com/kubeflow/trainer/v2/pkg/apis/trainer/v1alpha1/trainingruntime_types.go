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
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	jobsetv1alpha2 "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	volcanov1beta1 "volcano.sh/apis/pkg/apis/scheduling/v1beta1"
)

const (
	// TrainingRuntimeKind is the Kind name for the TrainingRuntime.
	TrainingRuntimeKind string = "TrainingRuntime"
	// ClusterTrainingRuntimeKind is the Kind name for the ClusterTrainingRuntime.
	ClusterTrainingRuntimeKind string = "ClusterTrainingRuntime"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=clustertrainingruntime
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster

// ClusterTrainingRuntime represents a training runtime which can be referenced as part of
// `runtimeRef` API in TrainJob. This resource is a cluster-scoped and can be referenced
// by TrainJob that created in *any* namespace.
type ClusterTrainingRuntime struct {
	metav1.TypeMeta `json:",inline"`

	// metadata of the ClusterTrainingRuntime.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec of the ClusterTrainingRuntime.
	// +optional
	Spec TrainingRuntimeSpec `json:"spec,omitzero"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=clustertrainingruntimes
// +kubebuilder:object:root=true

// ClusterTrainingRuntimeList is a collection of cluster training runtimes.
type ClusterTrainingRuntimeList struct {
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of ClusterTrainingRuntimes.
	Items []ClusterTrainingRuntime `json:"items"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=trainingruntime
// +kubebuilder:object:root=true
// +kubebuilder:storageversion

// TrainingRuntime represents a training runtime which can be referenced as part of
// `runtimeRef` API in TrainJob. This resource is a namespaced-scoped and can be referenced
// by TrainJob that created in the *same* namespace as the TrainingRuntime.
type TrainingRuntime struct {
	metav1.TypeMeta `json:",inline"`

	// metadata of the TrainingRuntime.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec of the TrainingRuntime.
	// +optional
	Spec TrainingRuntimeSpec `json:"spec,omitempty,omitzero"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +resource:path=trainingruntimes
// +kubebuilder:object:root=true

// TrainingRuntimeList is a collection of training runtimes.
type TrainingRuntimeList struct {
	metav1.TypeMeta `json:",inline"`

	// Standard list metadata.
	metav1.ListMeta `json:"metadata,omitempty"`

	// List of TrainingRuntimes.
	Items []TrainingRuntime `json:"items"`
}

// TrainingRuntimeSpec represents a specification of the desired training runtime.
// +kubebuilder:validation:MinProperties=1
type TrainingRuntimeSpec struct {
	// mlPolicy provides the ML-specific parameters for the model training.
	// +optional
	MLPolicy *MLPolicy `json:"mlPolicy,omitempty"`

	// podGroupPolicy defines the configuration for the PodGroup to enable gang-scheduling via supported plugins.
	// +optional
	PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`

	// template for the JobSet which will be used by TrainJob.
	// +optional
	Template JobSetTemplateSpec `json:"template,omitzero"`
}

// JobSetTemplateSpec represents a template of the desired JobSet.
// +kubebuilder:validation:MinProperties=1
type JobSetTemplateSpec struct {
	// metadata for custom JobSet's labels and annotations.
	// JobSet name and namespace is equal to the TrainJob's name and namespace.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec of the desired JobSet which will be created from TrainJob.
	// +optional
	Spec jobsetv1alpha2.JobSetSpec `json:"spec,omitempty"`
}

// PodGroupPolicy represents a PodGroup configuration for gang-scheduling.
type PodGroupPolicy struct {
	// Configuration for gang-scheduling using various plugins.
	PodGroupPolicySource `json:",inline"`
}

// PodGroupPolicySource represents supported plugins for gang-scheduling.
// Only one of its members may be specified.
type PodGroupPolicySource struct {
	// coscheduling plugin from the Kubernetes scheduler-plugins for gang-scheduling.
	// +optional
	Coscheduling *CoschedulingPodGroupPolicySource `json:"coscheduling,omitempty"`

	// volcano plugin for gang-scheduling.
	// +optional
	Volcano *VolcanoPodGroupPolicySource `json:"volcano,omitempty"`
}

// CoschedulingPodGroupPolicySource represents configuration for coscheduling plugin.
// The number of min members in the PodGroupSpec is always equal to the number of nodes.
type CoschedulingPodGroupPolicySource struct {
	// scheduleTimeoutSeconds is the maximum duration to schedule PodGroup for gang-scheduling.
	// If the scheduling timeout is equal to 0, the default value is used.
	// Defaults to 60 seconds.
	// +kubebuilder:default=60
	// +optional
	ScheduleTimeoutSeconds *int32 `json:"scheduleTimeoutSeconds,omitempty"`
}

// VolcanoPodGroupPolicySource represents configuration for the Volcano gang-scheduler.
type VolcanoPodGroupPolicySource struct {
	// networkTopology defines the NetworkTopology config, this field works in conjunction with network topology feature and hyperNode CRD.
	// +optional
	NetworkTopology *volcanov1beta1.NetworkTopologySpec `json:"networkTopology,omitempty"`
}

// MLPolicy represents configuration for the model training with ML-specific parameters.
// +kubebuilder:validation:XValidation:rule="!(has(self.numNodes) && (has(self.torch) && has(self.torch.elasticPolicy)))", message="numNodes should not be set if torch.elasticPolicy is configured"
// +kubebuilder:validation:XValidation:rule="!(has(self.torch) && has(self.mpi))", message="Only one of the policy can be configured"
type MLPolicy struct {
	// numNodes is the number of training nodes.
	// Defaults to 1.
	// +optional
	NumNodes *int32 `json:"numNodes,omitempty"`

	// Configuration for the runtime-specific parameters, such as Torch or MPI.
	// Only one of its members may be specified.
	MLPolicySource `json:",inline"`
}

// MLPolicySource represents the runtime-specific configuration for various technologies.
// One of the following specs can be set.
type MLPolicySource struct {
	// torch defines the configuration for the PyTorch runtime.
	// +optional
	Torch *TorchMLPolicySource `json:"torch,omitempty"`

	// mpi defines the configuration for the MPI Runtime.
	// +optional
	MPI *MPIMLPolicySource `json:"mpi,omitempty"`
}

// TorchMLPolicySource represents a PyTorch runtime configuration.
type TorchMLPolicySource struct {
	// numProcPerNode is the number of processes per node.
	// This value is inserted into the `--nproc-per-node` argument of the `torchrun` CLI.
	// Supported values: `auto`, `cpu`, `gpu`, or int value.
	// Defaults to `auto`.
	// +kubebuilder:default="auto"
	// +kubebuilder:validation:XValidation:rule="self > 0 || self in ['auto', 'cpu', 'gpu']", message="NumProcPerNode must be equal to auto, cpu, gpu, or int value"
	// +optional
	NumProcPerNode *intstr.IntOrString `json:"numProcPerNode,omitempty"`

	// elasticPolicy defines the Elastic policy for the PyTorch training.
	// +optional
	ElasticPolicy *TorchElasticPolicy `json:"elasticPolicy,omitempty"`
}

// TorchElasticPolicy represents a configuration for the PyTorch elastic training.
// If this policy is set, the `.spec.numNodes` parameter must be omitted, since min and max node
// is used to configure the `torchrun` CLI argument: `--nnodes=minNodes:maxNodes`.
// Only `c10d` backend is supported for the Rendezvous communication.
type TorchElasticPolicy struct {
	// maxRestarts defines how many times the training job can be restarted.
	// This value is inserted into the `--max-restarts` argument of the `torchrun` CLI and
	// the `.spec.failurePolicy.maxRestarts` parameter of the training Job.
	// +optional
	MaxRestarts *int32 `json:"maxRestarts,omitempty"`

	// minNodes is the lower limit for the number of nodes to which training job can scale down.
	// +optional
	MinNodes *int32 `json:"minNodes,omitempty"`

	// maxNodes is the upper limit for the number of nodes to which training job can scale up.
	// +optional
	MaxNodes *int32 `json:"maxNodes,omitempty"`

	// metrics which are used to calculate the desired number of nodes. See the individual
	// metric source types for more information about how each type of metric must respond.
	// The HPA will be created to perform auto-scaling.
	// +listType=atomic
	// +optional
	Metrics []autoscalingv2.MetricSpec `json:"metrics,omitempty"`
}

// MPIMLPolicySource represents a MPI runtime configuration.
type MPIMLPolicySource struct {
	// numProcPerNode is the number of processes per node.
	// This value is equal to the number of slots for each node in the hostfile.
	// Defaults to 1.
	// +kubebuilder:default=1
	// +optional
	NumProcPerNode *int32 `json:"numProcPerNode,omitempty"`

	// mpiImplementation is the name of the MPI implementation to create the appropriate hostfile.
	// Defaults to OpenMPI.
	// +kubebuilder:default=OpenMPI
	// +kubebuilder:validation:Enum=OpenMPI;""
	// +optional
	MPIImplementation *MPIImplementation `json:"mpiImplementation,omitempty"`

	// sshAuthMountPath is the directory where SSH keys are mounted.
	// Defaults to /root/.ssh.
	// +kubebuilder:default=/root/.ssh
	// +optional
	SSHAuthMountPath *string `json:"sshAuthMountPath,omitempty"`

	// runLauncherAsNode defines whether to run training process on the launcher Job.
	// Defaults to false.
	// +kubebuilder:default=false
	// +optional
	RunLauncherAsNode *bool `json:"runLauncherAsNode,omitempty"`
}

// MPIImplementation represents one of the supported MPI implementations.
type MPIImplementation string

const (
	MPIImplementationOpenMPI MPIImplementation = "OpenMPI"
	MPIImplementationIntel   MPIImplementation = "Intel"
	MPIImplementationMPICH   MPIImplementation = "MPICH"
)

func init() {
	SchemeBuilder.Register(&ClusterTrainingRuntime{}, &ClusterTrainingRuntimeList{}, &TrainingRuntime{}, &TrainingRuntimeList{})
}
