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
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// +kubebuilder:validation:XValidation:rule="!(has(self.scheduling) && has(self.podGroupPolicy))", message="scheduling and podGroupPolicy are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.scheduling) && has(self.template) && has(self.template.spec) && has(self.template.spec.scheduling))", message="JobSet scheduling must not be set, it is owned by the TrainJob controller"
// +kubebuilder:validation:XValidation:rule="!(has(self.scheduling) && has(self.template) && has(self.template.spec) && has(self.template.spec.replicatedJobs) && self.template.spec.replicatedJobs.exists(r, has(r.template.spec) && has(r.template.spec.scheduling)))", message="Job scheduling must not be set, it is owned by the TrainJob controller"
// +kubebuilder:validation:XValidation:rule="!(has(self.scheduling) && has(self.template) && has(self.template.spec) && has(self.template.spec.replicatedJobs) && self.template.spec.replicatedJobs.exists(r, has(r.template.spec) && has(r.template.spec.template.spec) && has(r.template.spec.template.spec.schedulingGroup)))", message="Pod schedulingGroup must not be set, it is owned by the TrainJob controller"
// +kubebuilder:validation:XValidation:rule="!has(self.scheduling) || !(has(self.scheduling.schedulingPolicy) ? has(self.scheduling.schedulingPolicy.gang) : (!has(self.scheduling.replicatedJobs) || size(self.scheduling.replicatedJobs) == 0)) || !has(self.template) || !has(self.template.spec) || ((!has(self.template.spec.replicatedJobs) || self.template.spec.replicatedJobs.all(rj, !has(rj.dependsOn) || size(rj.dependsOn) == 0)) && (!has(self.template.spec.startupPolicy) || self.template.spec.startupPolicy.startupPolicyOrder != 'InOrder'))", message="TrainJob-level gang scheduling cannot be used with dependsOn or InOrder startupPolicy; target the ReplicatedJobs with separate scheduling.replicatedJobs entries instead"
type TrainingRuntimeSpec struct {
	// mlPolicy provides the ML-specific parameters for the model training.
	// +optional
	MLPolicy *MLPolicy `json:"mlPolicy,omitempty"`

	// podGroupPolicy defines the configuration for the PodGroup to enable gang-scheduling via supported plugins.
	// +optional
	PodGroupPolicy *PodGroupPolicy `json:"podGroupPolicy,omitempty"`

	// scheduling defines the Workload-Aware Scheduling configuration for TrainJobs which
	// reference this runtime. When set, the TrainJob controller creates a Workload and the
	// corresponding PodGroups for every TrainJob. When nil, no scheduling objects are created and
	// TrainJob behavior is unchanged.
	// This field is mutually exclusive with podGroupPolicy.
	// This is an alpha feature and requires enabling the TrainJobWorkloadAwareScheduling feature gate.
	// This field embeds upstream scheduling.k8s.io/v1alpha3 types and is excluded from the Trainer API
	// stability guarantees until the upstream scheduling APIs graduate.
	// More info: https://github.com/kubeflow/trainer/tree/master/proposals/3015-workload-aware-scheduling
	// +optional
	Scheduling *TrainingRuntimeScheduling `json:"scheduling,omitempty"`

	// template for the JobSet which will be used by TrainJob.
	// +optional
	Template JobSetTemplateSpec `json:"template,omitzero"`
}

// JobSetTemplateSpec represents a template of the desired JobSet.
// +kubebuilder:validation:MinProperties=1
// +kubebuilder:validation:XValidation:rule="!has(self.spec) || !has(self.spec.replicatedJobs) || self.spec.replicatedJobs.all(r, size(r.name) <= 20)", message="replicatedJob name must be no more than 20 characters"
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
// +kubebuilder:validation:AtMostOneOf=coscheduling;volcano
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
	// +kubebuilder:validation:Minimum=0
	// +optional
	ScheduleTimeoutSeconds *int32 `json:"scheduleTimeoutSeconds,omitempty"`
}

// VolcanoPodGroupPolicySource represents configuration for the Volcano gang-scheduler.
type VolcanoPodGroupPolicySource struct {
	// networkTopology defines the NetworkTopology config, this field works in conjunction with network topology feature and hyperNode CRD.
	// +optional
	NetworkTopology *volcanov1beta1.NetworkTopologySpec `json:"networkTopology,omitempty"`
}

// TrainingRuntimeScheduling defines the Workload-Aware Scheduling configuration for a TrainJob.
// Unless the TrainJobCompositePodGroup feature gate is enabled, a runtime must configure
// scheduling using one of the following models, and each ReplicatedJob can be configured
// by exactly one of them, since composite Gang-of-Gangs PodGroup hierarchies linking a
// parent PodGroup to leaf PodGroups are not created without that gate:
//   - the TrainJob-level (level 1) model: set schedulingPolicy, schedulingConstraints,
//     and/or disruptionMode to configure a single PodGroup covering the whole
//     TrainJob, and leave replicatedJobs unset.
//   - the per-ReplicatedJob (level 2) model: set replicatedJobs and leave schedulingPolicy,
//     schedulingConstraints, and disruptionMode unset at the top level.
//     Every ReplicatedJob in the runtime template must then be targeted by exactly one
//     replicatedJobs entry, since there is no top-level policy for an untargeted
//     ReplicatedJob to fall back to.
//   - the per-Job (level 3) model: set job on a replicatedJobs entry, so every replica
//     (Job) of the targeted ReplicatedJob gets its own PodGroup. The entry must target
//     exactly one ReplicatedJob and leave its schedulingPolicy, schedulingConstraints, and
//     disruptionMode unset. Level 2 and level 3 entries can be combined for different
//     ReplicatedJobs of the same runtime.
//
// JobSet creates the ReplicatedJobs that set dependsOn only after their dependencies reach the
// required status, so a single TrainJob-level gang could never reach its minCount. A runtime
// using dependsOn must therefore use the per-ReplicatedJob model.
// +kubebuilder:validation:XValidation:rule="!has(self.replicatedJobs) || size(self.replicatedJobs) == 0 || (!has(self.schedulingPolicy) && !has(self.schedulingConstraints) && !has(self.disruptionMode))", message="schedulingPolicy, schedulingConstraints, and disruptionMode cannot be set together with replicatedJobs"
// +kubebuilder:validation:XValidation:rule="!has(self.replicatedJobs) || self.replicatedJobs.all(e, self.replicatedJobs.filter(o, o.targetReplicatedJobs.exists(n, n in e.targetReplicatedJobs)).size() == 1)", message="a ReplicatedJob must not be targeted by more than one replicatedJobs entry"
type TrainingRuntimeScheduling struct {
	// schedulingPolicy defines the TrainJob-level (level 1) scheduling policy for every Pod
	// created by the TrainJob. Defaults to Gang when scheduling is set but schedulingPolicy is
	// nil and replicatedJobs is not set.
	// TrainJob-level gang scheduling, including the default, cannot be used with dependsOn.
	// Mutually exclusive with replicatedJobs: see the type-level comment.
	// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.gang)", message="exactly one of basic or gang must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.gang) || !has(self.gang.minGroupCount)", message="gang.minGroupCount must not be set until CompositePodGroup is supported"
	// +optional
	SchedulingPolicy *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// schedulingConstraints defines TrainJob-level (level 1) topology constraints for every
	// Pod created by the TrainJob.
	// Mutually exclusive with replicatedJobs: see the type-level comment.
	// +optional
	SchedulingConstraints *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`

	// disruptionMode defines how the Pods of the entire TrainJob (level 1) can be disrupted.
	// Mutually exclusive with replicatedJobs: see the type-level comment.
	// +kubebuilder:validation:XValidation:rule="has(self.single) != has(self.all)", message="exactly one of single or all must be set"
	// +optional
	DisruptionMode *schedulingv1alpha3.WorkloadCompositePodGroupDisruptionMode `json:"disruptionMode,omitempty"`

	// replicatedJobs specifies per-ReplicatedJob (level 2) scheduling configuration.
	// Each entry targets one or more ReplicatedJobs of the runtime template.
	// Without the TrainJobCompositePodGroup feature gate, this field is mutually exclusive with
	// the TrainJob-level schedulingPolicy, schedulingConstraints, and disruptionMode fields.
	// The item limit matches the upstream limit of PodGroupTemplates in a Workload.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	ReplicatedJobs []ReplicatedJobScheduling `json:"replicatedJobs,omitempty"`
}

// ReplicatedJobScheduling targets one or more ReplicatedJobs of the runtime template with
// level 2 scheduling configuration.
// +kubebuilder:validation:XValidation:rule="!has(self.job) || (size(self.targetReplicatedJobs) == 1 && !has(self.schedulingPolicy) && !has(self.schedulingConstraints) && !has(self.disruptionMode))", message="when job is set, targetReplicatedJobs must contain exactly one name and schedulingPolicy, schedulingConstraints, and disruptionMode must not be set"
type ReplicatedJobScheduling struct {
	// targetReplicatedJobs is the list of ReplicatedJob names this policy applies to.
	// When more than one name is listed, the targeted ReplicatedJobs share a single PodGroup.
	// Every name must exist in the runtime template and must be unique across all
	// replicatedJobs entries.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=253
	// +required
	TargetReplicatedJobs []string `json:"targetReplicatedJobs,omitempty"`

	// schedulingPolicy defines the level 2 scheduling policy for the Pods created by the
	// targeted ReplicatedJobs. Defaults to Gang when not specified.
	// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.gang)", message="exactly one of basic or gang must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.gang) || !has(self.gang.minGroupCount)", message="gang.minGroupCount must not be set until CompositePodGroup is supported"
	// +optional
	SchedulingPolicy *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// schedulingConstraints defines level 2 topology constraints for the Pods created by the
	// targeted ReplicatedJobs.
	// +optional
	SchedulingConstraints *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`

	// disruptionMode defines how the Pods created by the targeted ReplicatedJobs can be
	// disrupted.
	// +kubebuilder:validation:XValidation:rule="has(self.single) != has(self.all)", message="exactly one of single or all must be set"
	// +optional
	DisruptionMode *schedulingv1alpha3.WorkloadCompositePodGroupDisruptionMode `json:"disruptionMode,omitempty"`

	// job defines Job-level (level 3) scheduling configuration, where every replica of the
	// targeted ReplicatedJob forms its own independent gang, so one PodGroup is created per
	// Job instead of a single PodGroup shared across every replica.
	// Without the TrainJobCompositePodGroup feature gate, targetReplicatedJobs must contain exactly
	// one ReplicatedJob name and the level 2 schedulingPolicy, schedulingConstraints, and
	// disruptionMode fields must not be set, since they configure a shared PodGroup that the
	// job field replaces with one PodGroup per Job.
	// Currently, the targeted ReplicatedJob must have at most 1 replica, since a distinct PodGroup
	// can't be assigned to the Pods of each replica before k8s v1.38.
	// +optional
	Job *JobScheduling `json:"job,omitempty"`
}

// JobScheduling defines scheduling configuration applied to individual Jobs (ReplicatedJob
// replicas), enabling each replica to be scheduled as its own independent gang. The
// controller compiles one PodGroupTemplate and PodGroup per Job, sized to that Job's own
// parallelism, rather than one PodGroup shared across all of the ReplicatedJob's replicas.
type JobScheduling struct {
	// schedulingPolicy defines the scheduling policy applied to each replica of the targeted
	// ReplicatedJob. Defaults to Gang when not specified.
	// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.gang)", message="exactly one of basic or gang must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.gang) || !has(self.gang.minCount)", message="gang.minCount is computed from numNodes and must not be set"
	// +optional
	SchedulingPolicy *schedulingv1alpha3.WorkloadPodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// schedulingConstraints defines topology constraints applied to each replica of the
	// targeted ReplicatedJob.
	// +optional
	SchedulingConstraints *schedulingv1alpha3.WorkloadPodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`

	// disruptionMode defines how the Pods within a single replica can be disrupted.
	// +kubebuilder:validation:XValidation:rule="has(self.single) != has(self.all)", message="exactly one of single or all must be set"
	// +optional
	DisruptionMode *schedulingv1alpha3.WorkloadPodGroupDisruptionMode `json:"disruptionMode,omitempty"`

	// resourceClaims defines the DRA ResourceClaims shared by all Pods in each Job's PodGroup.
	// Requires the DRAWorkloadResourceClaims Kubernetes feature gate.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=4
	// +kubebuilder:validation:XValidation:rule="self.all(c, has(c.resourceClaimName) != has(c.resourceClaimTemplateName))", message="exactly one of resourceClaimName or resourceClaimTemplateName must be set"
	// +optional
	ResourceClaims []schedulingv1alpha3.WorkloadPodGroupResourceClaim `json:"resourceClaims,omitempty"`
}

// MLPolicy represents configuration for the model training with ML-specific parameters.
// +kubebuilder:validation:XValidation:rule="[has(self.torch), has(self.mpi), has(self.jax), has(self.xgboost),has(self.flux)].filter(x, x).size() <= 1", message="Only one of the policy can be configured"
type MLPolicy struct {
	// numNodes is the number of training nodes.
	// Defaults to 1.
	// +optional
	NumNodes *int32 `json:"numNodes,omitempty"`

	// Configuration for the runtime-specific parameters, such as Torch, Flux, or MPI.
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

	// flux defines the configuration for the Flux runtime.
	// +optional
	Flux *FluxMLPolicySource `json:"flux,omitempty"`

	// jax defines the configuration for the JAX Runtime
	// +optional
	JAX *JAXMLPolicySource `json:"jax,omitempty"`

	// xgboost defines the configuration for the XGBoost Runtime.
	// +optional
	XGBoost *XGBoostMLPolicySource `json:"xgboost,omitempty"`
}

// TorchMLPolicySource represents a PyTorch runtime configuration.
type TorchMLPolicySource struct {
	// envInjection configures which additional containers should receive the
	// PET_* environment variables. By default, the PET_* variables are injected
	// only into the main "node" container. Use this field to also inject them
	// into selected sidecar or init containers. For torchtune, envInjection
	// targets still receive PET_MASTER_ADDR and PET_MASTER_PORT even though the
	// main trainer container uses command-line rendezvous instead.
	// Defaults to empty (main container only).
	// +optional
	EnvInjection *EnvInjection `json:"envInjection,omitempty"`
}

// EnvInjection specifies which containers in which jobs receive framework env injection.
// Defined as a standalone type so it can be embedded by other MLPolicySource
// variants in the future.
type EnvInjection struct {
	// targets defines which replicated job containers receive PET_* env injection.
	// The item limit keeps runtime validation bounded while allowing common
	// multi-job runtimes to target their auxiliary containers.
	// +listType=map
	// +listMapKey=jobName
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Targets []EnvInjectionTarget `json:"targets,omitempty"`
}

// EnvInjectionTarget specifies a replicated job and the containers within it
// that should receive PET_* env injection.
type EnvInjectionTarget struct {
	// jobName is the name of the target replicated job (e.g. "node").
	// Using "jobName" rather than "replicatedJobName" keeps the API
	// future-proof for other CRD types (LWS, Grove, Slurm, etc.).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	JobName string `json:"jobName,omitempty"`

	// containerNames lists the container names within the target job
	// that should receive PET_* envs.
	// The item limit keeps admission checks small while covering typical
	// preflight and sidecar use cases.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:items:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="!self.exists(c, c == 'node')", message="containerNames must not include the main node container because PET_* envs are always injected into it"
	// +required
	ContainerNames []string `json:"containerNames,omitempty"`
}

// JAXMLPolicySource represents a jax runtime configuration.
type JAXMLPolicySource struct{}

// XGBoostMLPolicySource represents an XGBoost runtime configuration.
// The number of workers per node is automatically derived from container GPU resources:
//   - GPU training: 1 worker per GPU (from resourcesPerNode)
//   - CPU training: 1 worker per node (each worker utilizes all available CPU cores
//     via XGBoost's multi-threaded execution, controlled by the nthread parameter)
//
// DMLC_NUM_WORKER = numNodes × workersPerNode (where workersPerNode = GPU count or 1)
type XGBoostMLPolicySource struct{}

// MPIMLPolicySource represents a MPI runtime configuration.
type MPIMLPolicySource struct {
	// numProcPerNode is the number of processes per node.
	// This value is equal to the number of slots for each node in the hostfile.
	// Defaults to 1.
	// +kubebuilder:default=1
	// +kubebuilder:validation:XValidation:rule="self >= 1",message="NumProcPerNode in mpiPolicy must be >= 1"
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
	// +kubebuilder:validation:MaxLength=4096
	// +optional
	SSHAuthMountPath *string `json:"sshAuthMountPath,omitempty"`

	// runLauncherAsNode defines whether to run training process on the launcher Job.
	// Defaults to false.
	// +kubebuilder:default=false
	// +optional
	RunLauncherAsNode *bool `json:"runLauncherAsNode,omitempty"`
}

// FluxMLPolicySource represents a Flux HPC runtime configuration.
type FluxMLPolicySource struct {
	// numProcPerNode is the number of processes per node.
	// +kubebuilder:default=1
	// +kubebuilder:validation:XValidation:rule="self >= 1",message="NumProcPerNode in fluxPolicy must be >= 1"
	// +optional
	NumProcPerNode *int32 `json:"numProcPerNode,omitempty"`
}

// MPIImplementation represents one of the supported MPI implementations.
type MPIImplementation string

const (
	MPIImplementationOpenMPI MPIImplementation = "OpenMPI"
	MPIImplementationIntel   MPIImplementation = "Intel"
	MPIImplementationMPICH   MPIImplementation = "MPICH"
)
