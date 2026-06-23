// Copyright 2019 The Kubeflow Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package v2beta1

import (
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

type MPIJob struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              MPIJobSpec `json:"spec,omitempty"`
	Status            JobStatus  `json:"status,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:object:root=true

type MPIJobList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`
	Items           []MPIJob `json:"items"`
}

// CleanPodPolicy describes how to deal with pods when the job is finished.
type CleanPodPolicy string

const (
	CleanPodPolicyUndefined CleanPodPolicy = ""
	CleanPodPolicyAll       CleanPodPolicy = "All"
	CleanPodPolicyRunning   CleanPodPolicy = "Running"
	CleanPodPolicyNone      CleanPodPolicy = "None"
)

// SchedulingPolicy encapsulates various scheduling policies of the distributed training
// job, for example `minAvailable` for gang-scheduling.
// Now, it supports only for volcano and scheduler-plugins.
type SchedulingPolicy struct {
	// MinAvailable defines the minimal number of member to run the PodGroup.
	// If the gang-scheduling isn't empty, input is passed to `.spec.minMember` in PodGroup.
	// Note that, when using this field,
	// you need to make sure the application supports resizing (e.g., Elastic Horovod).
	//
	// If not set, it defaults to the number of workers.
	// +optional
	MinAvailable *int32 `json:"minAvailable,omitempty"`

	// Queue defines the queue name to allocate resource for PodGroup.
	// If the gang-scheduling is set to the volcano,
	// input is passed to `.spec.queue` in PodGroup for the volcano,
	// and if it is set to the scheduler-plugins,
	// input isn't passed to PodGroup.
	// +optional
	Queue string `json:"queue,omitempty"`

	// MinResources defines the minimal resources of members to run the PodGroup.
	// If the gang-scheduling isn't empty,
	// input is passed to `.spec.minResources` in PodGroup for scheduler-plugins.
	// +optional
	MinResources *v1.ResourceList `json:"minResources,omitempty"`

	// PriorityClass defines the PodGroup's PriorityClass.
	// If the gang-scheduling is set to the volcano,
	// input is passed to `.spec.priorityClassName` in PodGroup for volcano,
	// and if it is set to the scheduler-plugins,
	// input isn't passed to PodGroup for scheduler-plugins.
	// +optional
	PriorityClass string `json:"priorityClass,omitempty"`

	// SchedulerTimeoutSeconds defines the maximal time of members to wait before run the PodGroup.
	// If the gang-scheduling is set to the scheduler-plugins,
	// input is passed to `.spec.scheduleTimeoutSeconds` in PodGroup for the scheduler-plugins,
	// and if it is set to the volcano, input isn't passed to PodGroup.
	// +optional
	ScheduleTimeoutSeconds *int32 `json:"scheduleTimeoutSeconds,omitempty"`
}

const (
	// KubeflowJobController represents the value of the default job controller
	KubeflowJobController = "kubeflow.org/mpi-operator"

	// MultiKueueController represents the vaue of the MultiKueue controller
	MultiKueueController = "kueue.x-k8s.io/multikueue"
)

// RunPolicy encapsulates various runtime policies of the distributed training
// job, for example how to clean up resources and how long the job can stay
// active.
type RunPolicy struct {
	// CleanPodPolicy defines the policy to kill pods after the job completes.
	// Default to Running.
	CleanPodPolicy *CleanPodPolicy `json:"cleanPodPolicy,omitempty"`

	// TTLSecondsAfterFinished is the TTL to clean up jobs.
	// It may take extra ReconcilePeriod seconds for the cleanup, since
	// reconcile gets called periodically.
	// Default to infinite.
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// Specifies the duration in seconds relative to the startTime that the job may be active
	// before the system tries to terminate it; value must be positive integer.
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// Optional number of retries before marking this job failed.
	// +optional
	BackoffLimit *int32 `json:"backoffLimit,omitempty"`

	// SchedulingPolicy defines the policy related to scheduling, e.g. gang-scheduling
	// +optional
	SchedulingPolicy *SchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// suspend specifies whether the MPIJob controller should create Pods or not.
	// If a MPIJob is created with suspend set to true, no Pods are created by
	// the MPIJob controller. If a MPIJob is suspended after creation (i.e. the
	// flag goes from false to true), the MPIJob controller will delete all
	// active Pods and PodGroups associated with this MPIJob. Also, it will suspend the
	// Launcher Job. Users must design their workload to gracefully handle this.
	// Suspending a Job will reset the StartTime field of the MPIJob.
	//
	// Defaults to false.
	// +kubebuilder:default:=false
	Suspend *bool `json:"suspend,omitempty"`

	// ManagedBy is used to indicate the controller or entity that manages a MPIJob.
	// The value must be either empty, 'kubeflow.org/mpi-operator' or
	// 'kueue.x-k8s.io/multikueue'.
	// The mpi-operator reconciles a MPIJob which doesn't have this
	// field at all or the field value is the reserved string
	// 'kubeflow.org/mpi-operator', but delegates reconciling the MPIJob
	// with 'kueue.x-k8s.io/multikueue' to the Kueue.
	// The field is immutable.
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`
}

type LauncherCreationPolicy string

const (
	// LauncherCreationPolicyAtStartup is default behavior
	// when Launcher is started in parallel with workers
	LauncherCreationPolicyAtStartup LauncherCreationPolicy = "AtStartup"

	// LauncherCreationPolicyWaitForWorkersReady makes Launcher reation
	// postponed until all workers are in ready state so that the Launcher
	// does not fail trying to connect to worker.
	LauncherCreationPolicyWaitForWorkersReady LauncherCreationPolicy = "WaitForWorkersReady"
)

type MPIJobSpec struct {

	// Specifies the number of slots per worker used in hostfile.
	// Defaults to 1.
	// +optional
	// +kubebuilder:default:=1
	SlotsPerWorker *int32 `json:"slotsPerWorker,omitempty"`

	// RunLauncherAsWorker indicates whether to run worker process in launcher
	// Defaults to false.
	// +optional
	// +kubebuilder:default:=false
	RunLauncherAsWorker *bool `json:"runLauncherAsWorker,omitempty"`

	// RunPolicy encapsulates various runtime policies of the job.
	RunPolicy RunPolicy `json:"runPolicy,omitempty"`

	// MPIReplicaSpecs contains maps from `MPIReplicaType` to `ReplicaSpec` that
	// specify the MPI replicas to run.
	MPIReplicaSpecs map[MPIReplicaType]*ReplicaSpec `json:"mpiReplicaSpecs"`

	// SSHAuthMountPath is the directory where SSH keys are mounted.
	// Defaults to "/root/.ssh".
	// +kubebuilder:default:="/root/.ssh"
	SSHAuthMountPath string `json:"sshAuthMountPath,omitempty"`

	// launcherCreationPolicy if WaitForWorkersReady, the launcher is created only after all workers are in Ready state. Defaults to AtStartup.
	// +kubebuilder:validation:Enum:AtStartup;WaitForWorkersReady
	// +kubebuilder:default:=AtStartup
	LauncherCreationPolicy LauncherCreationPolicy `json:"launcherCreationPolicy,omitempty"`

	// MPIImplementation is the MPI implementation.
	// Options are "OpenMPI" (default), "Intel" and "MPICH".
	// +kubebuilder:validation:Enum:=OpenMPI;Intel;MPICH
	// +kubebuilder:default:=OpenMPI
	MPIImplementation MPIImplementation `json:"mpiImplementation,omitempty"`
}

// MPIReplicaType is the type for MPIReplica.
type MPIReplicaType string

const (
	// MPIReplicaTypeLauncher is the type for launcher replica.
	MPIReplicaTypeLauncher MPIReplicaType = "Launcher"

	// MPIReplicaTypeWorker is the type for worker replicas.
	MPIReplicaTypeWorker MPIReplicaType = "Worker"
)

type MPIImplementation string

const (
	MPIImplementationOpenMPI MPIImplementation = "OpenMPI"
	MPIImplementationIntel   MPIImplementation = "Intel"
	MPIImplementationMPICH   MPIImplementation = "MPICH"
)

// JobStatus represents the current observed state of the training Job.
type JobStatus struct {
	// conditions is a list of current observed job conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []JobCondition `json:"conditions,omitempty"`

	// replicaStatuses is map of ReplicaType and ReplicaStatus,
	// specifies the status of each replica.
	// +optional
	ReplicaStatuses map[MPIReplicaType]*ReplicaStatus `json:"replicaStatuses,omitempty"`

	// Represents time when the job was acknowledged by the job controller.
	// It is not guaranteed to be set in happens-before order across separate operations.
	// It is represented in RFC3339 form and is in UTC.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// Represents time when the job was completed. It is not guaranteed to
	// be set in happens-before order across separate operations.
	// It is represented in RFC3339 form and is in UTC.
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`

	// Represents last time when the job was reconciled. It is not guaranteed to
	// be set in happens-before order across separate operations.
	// It is represented in RFC3339 form and is in UTC.
	// +optional
	LastReconcileTime *metav1.Time `json:"lastReconcileTime,omitempty"`
}

// ReplicaStatus represents the current observed state of the replica.
type ReplicaStatus struct {
	// The number of actively running pods.
	// +optional
	Active int32 `json:"active,omitempty"`

	// The number of pods which reached phase succeeded.
	// +optional
	Succeeded int32 `json:"succeeded,omitempty"`

	// The number of pods which reached phase failed.
	// +optional
	Failed int32 `json:"failed,omitempty"`

	// Deprecated: Use selector instead
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// A selector is a label query over a set of resources. The result of matchLabels and
	// matchExpressions are ANDed. An empty selector matches all objects. A null
	// selector matches no objects.
	// +optional
	Selector string `json:"selector,omitempty"`
}

// JobCondition describes the state of the job at a certain point.
type JobCondition struct {
	// type of job condition.
	Type JobConditionType `json:"type"`

	// status of the condition, one of True, False, Unknown.
	// +kubebuilder:validation:Enum:=True;False;Unknown
	Status v1.ConditionStatus `json:"status"`

	// The reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`

	// A human-readable message indicating details about the transition.
	// +optional
	Message string `json:"message,omitempty"`

	// The last time this condition was updated.
	// +optional
	LastUpdateTime metav1.Time `json:"lastUpdateTime,omitempty"`

	// Last time the condition transitioned from one status to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// JobConditionType defines all kinds of types of JobStatus.
type JobConditionType string

const (
	// JobCreated means the job has been accepted by the system,
	// but one or more of the pods/services has not been started.
	// This includes time before pods being scheduled and launched.
	JobCreated JobConditionType = "Created"

	// JobRunning means all sub-resources (e.g. services/pods) of this job
	// have been successfully scheduled and launched.
	// The training is running without error.
	JobRunning JobConditionType = "Running"

	// JobRestarting means one or more sub-resources (e.g. services/pods) of this job
	// reached phase failed but maybe restarted according to it's restart policy
	// which specified by user in v1.PodTemplateSpec.
	// The training is freezing/pending.
	JobRestarting JobConditionType = "Restarting"

	// JobSucceeded means all sub-resources (e.g. services/pods) of this job
	// reached phase have terminated in success.
	// The training is complete without error.
	JobSucceeded JobConditionType = "Succeeded"

	// JobSuspended means the job has been suspended.
	JobSuspended JobConditionType = "Suspended"

	// JobFailed means one or more sub-resources (e.g. services/pods) of this job
	// reached phase failed with no restarting.
	// The training has failed its execution.
	JobFailed JobConditionType = "Failed"
)

// Following is merge from common.v1
// reference https://github.com/kubeflow/training-operator/blob/master/pkg/apis/kubeflow.org/v1/common_types.go

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true
// ReplicaSpec is a description of the replica
type ReplicaSpec struct {
	// Replicas is the desired number of replicas of the given template.
	// If unspecified, defaults to 1.
	Replicas *int32 `json:"replicas,omitempty"`

	// Template is the object that describes the pod that
	// will be created for this replica. RestartPolicy in PodTemplateSpec
	// will be overide by RestartPolicy in ReplicaSpec
	Template v1.PodTemplateSpec `json:"template,omitempty"`

	// Restart policy for all replicas within the job.
	// One of Always, OnFailure, Never and ExitCode.
	// Default to Never.
	RestartPolicy RestartPolicy `json:"restartPolicy,omitempty"`
}

// +k8s:openapi-gen=true
// RestartPolicy describes how the replicas should be restarted.
// Only one of the following restart policies may be specified.
// If none of the following policies is specified, the default one
// is RestartPolicyAlways.
type RestartPolicy string

const (
	RestartPolicyAlways    RestartPolicy = "Always"
	RestartPolicyOnFailure RestartPolicy = "OnFailure"
	RestartPolicyNever     RestartPolicy = "Never"

	// RestartPolicyExitCode policy means that user should add exit code by themselves,
	// The job operator will check these exit codes to
	// determine the behavior when an error occurs:
	// - 1-127: permanent error, do not restart.
	// - 128-255: retryable error, will restart the pod.
	RestartPolicyExitCode RestartPolicy = "ExitCode"
)
