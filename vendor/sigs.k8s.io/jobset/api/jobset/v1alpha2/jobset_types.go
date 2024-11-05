/*
Copyright 2023 The Kubernetes Authors.
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

// +k8s:openapi-gen=true
package v1alpha2

import (
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	JobSetNameKey         string = "jobset.sigs.k8s.io/jobset-name"
	ReplicatedJobReplicas string = "jobset.sigs.k8s.io/replicatedjob-replicas"
	// GlobalReplicasKey is a label/annotation set to the total number of replicatedJob replicas.
	// For each JobSet, this value will be equal to the sum of `replicas`, where `replicas`
	// is equal to jobset.spec.replicatedJobs[*].replicas.
	GlobalReplicasKey string = "jobset.sigs.k8s.io/global-replicas"
	// ReplicatedJobNameKey is used to index into a Jobs labels and retrieve the name of the parent ReplicatedJob
	ReplicatedJobNameKey string = "jobset.sigs.k8s.io/replicatedjob-name"
	// JobIndexKey is a label/annotation set to the index of the Job replica within its parent replicatedJob.
	// For each replicatedJob, this value will range from 0 to replicas-1, where `replicas`
	// is equal to jobset.spec.replicatedJobs[*].replicas.
	JobIndexKey string = "jobset.sigs.k8s.io/job-index"
	// JobGlobalIndexKey is a label/annotation set to an integer that is unique across the entire JobSet.
	// For each JobSet, this value will range from 0 to N-1, where N=total number of jobs in the jobset.
	JobGlobalIndexKey string = "jobset.sigs.k8s.io/job-global-index"
	// JobKey holds the SHA256 hash of the namespaced job name, which can be used to uniquely identify the job.
	JobKey string = "jobset.sigs.k8s.io/job-key"
	// ExclusiveKey is an annotation that can be set on the JobSet or on a ReplicatedJob template.
	// If set at the JobSet level, all child jobs from all ReplicatedJobs will be scheduled using exclusive
	// job placement per topology group (defined as the label value).
	// If set at the ReplicatedJob level, all child jobs from the target ReplicatedJobs will be scheduled
	// using exclusive job placement per topology group.
	// Exclusive placement is enforced within a priority level.
	ExclusiveKey string = "alpha.jobset.sigs.k8s.io/exclusive-topology"
	// NodeSelectorStrategyKey is an annotation that acts as a flag, the value does not matter.
	// If set, the JobSet controller will automatically inject nodeSelectors for the JobSetNameKey label to
	// ensure exclusive job placement per topology, instead of injecting pod affinity/anti-affinites for this.
	// The user must add the JobSet name node label to the desired topologies separately.
	NodeSelectorStrategyKey string = "alpha.jobset.sigs.k8s.io/node-selector"
	NamespacedJobKey        string = "alpha.jobset.sigs.k8s.io/namespaced-job"
	NoScheduleTaintKey      string = "alpha.jobset.sigs.k8s.io/no-schedule"

	// JobSetControllerName is the reserved value for the managedBy field for the built-in
	// JobSet controller.
	JobSetControllerName = "jobset.sigs.k8s.io/jobset-controller"

	// CoordinatorKey is used as an annotation and label on Jobs and Pods. If the JobSet spec
	// defines the .spec.coordinator field, this annotation/label will be added to store a stable
	// network endpoint where the coordinator pod can be reached.
	CoordinatorKey = "jobset.sigs.k8s.io/coordinator"
)

type JobSetConditionType string

// These are built-in conditions of a JobSet.
const (
	// JobSetCompleted means the job has completed its execution.
	JobSetCompleted JobSetConditionType = "Completed"
	// JobSetFailed means the job has failed its execution.
	JobSetFailed JobSetConditionType = "Failed"
	// JobSetSuspended means the job is suspended.
	JobSetSuspended JobSetConditionType = "Suspended"
	// JobSetStartupPolicyInProgress means the StartupPolicy is in progress.
	JobSetStartupPolicyInProgress JobSetConditionType = "StartupPolicyInProgress"
	// JobSetStartupPolicyCompleted means the StartupPolicy has completed.
	JobSetStartupPolicyCompleted JobSetConditionType = "StartupPolicyCompleted"
)

// JobSetSpec defines the desired state of JobSet
type JobSetSpec struct {
	// ReplicatedJobs is the group of jobs that will form the set.
	// +listType=map
	// +listMapKey=name
	ReplicatedJobs []ReplicatedJob `json:"replicatedJobs,omitempty"`

	// Network defines the networking options for the jobset.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	Network *Network `json:"network,omitempty"`

	// SuccessPolicy configures when to declare the JobSet as
	// succeeded.
	// The JobSet is always declared succeeded if all jobs in the set
	// finished with status complete.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	SuccessPolicy *SuccessPolicy `json:"successPolicy,omitempty"`

	// FailurePolicy, if set, configures when to declare the JobSet as
	// failed.
	// The JobSet is always declared failed if any job in the set
	// finished with status failed.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	FailurePolicy *FailurePolicy `json:"failurePolicy,omitempty"`

	// StartupPolicy, if set, configures in what order jobs must be started
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	StartupPolicy *StartupPolicy `json:"startupPolicy,omitempty"`

	// Suspend suspends all running child Jobs when set to true.
	Suspend *bool `json:"suspend,omitempty"`

	// Coordinator can be used to assign a specific pod as the coordinator for
	// the JobSet. If defined, an annotation will be added to all Jobs and pods with
	// coordinator pod, which contains the stable network endpoint where the
	// coordinator pod can be reached.
	// jobset.sigs.k8s.io/coordinator=<pod hostname>.<headless service>
	// +optional
	Coordinator *Coordinator `json:"coordinator,omitempty"`

	// ManagedBy is used to indicate the controller or entity that manages a JobSet.
	// The built-in JobSet controller reconciles JobSets which don't have this
	// field at all or the field value is the reserved string
	// `jobset.sigs.k8s.io/jobset-controller`, but skips reconciling JobSets
	// with a custom value for this field.
	//
	// The value must be a valid domain-prefixed path (e.g. acme.io/foo) -
	// all characters before the first "/" must be a valid subdomain as defined
	// by RFC 1123. All characters trailing the first "/" must be valid HTTP Path
	// characters as defined by RFC 3986. The value cannot exceed 63 characters.
	// The field is immutable.
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`

	// TTLSecondsAfterFinished limits the lifetime of a JobSet that has finished
	// execution (either Complete or Failed). If this field is set,
	// TTLSecondsAfterFinished after the JobSet finishes, it is eligible to be
	// automatically deleted. When the JobSet is being deleted, its lifecycle
	// guarantees (e.g. finalizers) will be honored. If this field is unset,
	// the JobSet won't be automatically deleted. If this field is set to zero,
	// the JobSet becomes eligible to be deleted immediately after it finishes.
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`
}

// JobSetStatus defines the observed state of JobSet
type JobSetStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// Restarts tracks the number of times the JobSet has restarted (i.e. recreated in case of RecreateAll policy).
	Restarts int32 `json:"restarts,omitempty"`

	// RestartsCountTowardsMax tracks the number of times the JobSet has restarted that counts towards the maximum allowed number of restarts.
	RestartsCountTowardsMax int32 `json:"restartsCountTowardsMax,omitempty"`

	// TerminalState the state of the JobSet when it finishes execution.
	// It can be either Complete or Failed. Otherwise, it is empty by default.
	TerminalState string `json:"terminalState,omitempty"`

	// ReplicatedJobsStatus track the number of JobsReady for each replicatedJob.
	// +optional
	// +listType=map
	// +listMapKey=name
	ReplicatedJobsStatus []ReplicatedJobStatus `json:"replicatedJobsStatus,omitempty"`
}

// ReplicatedJobStatus defines the observed ReplicatedJobs Readiness.
type ReplicatedJobStatus struct {
	// Name of the ReplicatedJob.
	Name string `json:"name"`

	// Ready is the number of child Jobs where the number of ready pods and completed pods
	// is greater than or equal to the total expected pod count for the Job (i.e., the minimum
	// of job.spec.parallelism and job.spec.completions).
	Ready int32 `json:"ready"`

	// Succeeded is the number of successfully completed child Jobs.
	Succeeded int32 `json:"succeeded"`

	// Failed is the number of failed child Jobs.
	Failed int32 `json:"failed"`

	// Active is the number of child Jobs with at least 1 pod in a running or pending state
	// which are not marked for deletion.
	Active int32 `json:"active"`

	// Suspended is the number of child Jobs which are in a suspended state.
	Suspended int32 `json:"suspended"`
}

// +genclient
// +k8s:openapi-gen=true
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="TerminalState",JSONPath=".status.terminalState",type=string,description="Final state of JobSet"
// +kubebuilder:printcolumn:name="Restarts",JSONPath=".status.restarts",type=string,description="Number of restarts"
// +kubebuilder:printcolumn:name="Completed",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"Completed\")].status"
// +kubebuilder:printcolumn:name="Suspended",type="string",JSONPath=".spec.suspend",description="JobSet suspended"
// +kubebuilder:printcolumn:name="Age",JSONPath=".metadata.creationTimestamp",type=date,description="Time this JobSet was created"

// JobSet is the Schema for the jobsets API
type JobSet struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              JobSetSpec   `json:"spec,omitempty"`
	Status            JobSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// JobSetList contains a list of JobSet
type JobSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []JobSet `json:"items"`
}

type ReplicatedJob struct {
	// Name is the name of the entry and will be used as a suffix
	// for the Job name.
	Name string `json:"name"`
	// Template defines the template of the Job that will be created.
	Template batchv1.JobTemplateSpec `json:"template"`

	// Replicas is the number of jobs that will be created from this ReplicatedJob's template.
	// Jobs names will be in the format: <jobSet.name>-<spec.replicatedJob.name>-<job-index>
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas,omitempty"`
}

type Network struct {
	// EnableDNSHostnames allows pods to be reached via their hostnames.
	// Pods will be reachable using the fully qualified pod hostname:
	// <jobSet.name>-<spec.replicatedJob.name>-<job-index>-<pod-index>.<subdomain>
	// +optional
	EnableDNSHostnames *bool `json:"enableDNSHostnames,omitempty"`

	// Subdomain is an explicit choice for a network subdomain name
	// When set, any replicated job in the set is added to this network.
	// Defaults to <jobSet.name> if not set.
	// +optional
	Subdomain string `json:"subdomain,omitempty"`

	// Indicates if DNS records of pods should be published before the pods are ready.
	// Defaults to True.
	// +optional
	PublishNotReadyAddresses *bool `json:"publishNotReadyAddresses,omitempty"`
}

// Operator defines the target of a SuccessPolicy or FailurePolicy.
type Operator string

const (
	// OperatorAll applies to all jobs matching the jobSelector.
	OperatorAll Operator = "All"

	// OperatorAny applies to any single job matching the jobSelector.
	OperatorAny Operator = "Any"
)

// FailurePolicyAction defines the action the JobSet controller will take for
// a given FailurePolicyRule.
type FailurePolicyAction string

const (
	// Fail the JobSet immediately, regardless of maxRestarts.
	FailJobSet FailurePolicyAction = "FailJobSet"

	// Restart the JobSet if the number of restart attempts is less than MaxRestarts.
	// Otherwise, fail the JobSet.
	RestartJobSet FailurePolicyAction = "RestartJobSet"

	// Do not count the failure against maxRestarts.
	RestartJobSetAndIgnoreMaxRestarts FailurePolicyAction = "RestartJobSetAndIgnoreMaxRestarts"
)

// FailurePolicyRule defines a FailurePolicyAction to be executed if a child job
// fails due to a reason listed in OnJobFailureReasons.
type FailurePolicyRule struct {
	// The name of the failure policy rule.
	// The name is defaulted to 'failurePolicyRuleN' where N is the index of the failure policy rule.
	// The name must match the regular expression "^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$".
	Name string `json:"name"`
	// The action to take if the rule is matched.
	// +kubebuilder:validation:Enum:=FailJobSet;RestartJobSet;RestartJobSetAndIgnoreMaxRestarts
	Action FailurePolicyAction `json:"action"`
	// The requirement on the job failure reasons. The requirement
	// is satisfied if at least one reason matches the list.
	// The rules are evaluated in order, and the first matching
	// rule is executed.
	// An empty list applies the rule to any job failure reason.
	// +kubebuilder:validation:UniqueItems:true
	OnJobFailureReasons []string `json:"onJobFailureReasons,omitempty"`
	// TargetReplicatedJobs are the names of the replicated jobs the operator applies to.
	// An empty list will apply to all replicatedJobs.
	// +optional
	// +listType=atomic
	TargetReplicatedJobs []string `json:"targetReplicatedJobs,omitempty"`
}

type FailurePolicy struct {
	// MaxRestarts defines the limit on the number of JobSet restarts.
	// A restart is achieved by recreating all active child jobs.
	MaxRestarts int32 `json:"maxRestarts,omitempty"`

	// RestartStrategy defines the strategy to use when restarting the JobSet.
	// Defaults to Recreate.
	// +optional
	// +kubebuilder:default=Recreate
	RestartStrategy JobSetRestartStrategy `json:"restartStrategy,omitempty"`

	// List of failure policy rules for this JobSet.
	// For a given Job failure, the rules will be evaluated in order,
	// and only the first matching rule will be executed.
	// If no matching rule is found, the RestartJobSet action is applied.
	Rules []FailurePolicyRule `json:"rules,omitempty"`
}

// +kubebuilder:validation:Enum=Recreate;BlockingRecreate
type JobSetRestartStrategy string

const (
	// Recreate Jobs on a Job-by-Job basis.
	Recreate JobSetRestartStrategy = "Recreate"

	// BlockingRecreate ensures that all Jobs (and Pods) from a previous iteration are deleted before
	// creating new Jobs.
	BlockingRecreate JobSetRestartStrategy = "BlockingRecreate"
)

type SuccessPolicy struct {
	// Operator determines either All or Any of the selected jobs should succeed to consider the JobSet successful
	// +kubebuilder:validation:Enum=All;Any
	Operator Operator `json:"operator"`

	// TargetReplicatedJobs are the names of the replicated jobs the operator will apply to.
	// A null or empty list will apply to all replicatedJobs.
	// +optional
	// +listType=atomic
	TargetReplicatedJobs []string `json:"targetReplicatedJobs,omitempty"`
}

type StartupPolicyOptions string

const (
	// This is the default setting
	// AnyOrder means that we will start the replicated jobs
	// without any specific order.
	AnyOrder StartupPolicyOptions = "AnyOrder"
	// InOrder starts the replicated jobs in order
	// that they are listed.
	InOrder StartupPolicyOptions = "InOrder"
)

type StartupPolicy struct {
	// StartupPolicyOrder determines the startup order of the ReplicatedJobs.
	// AnyOrder means to start replicated jobs in any order.
	// InOrder means to start them as they are listed in the JobSet. A ReplicatedJob is started only
	// when all the jobs of the previous one are ready.
	// +kubebuilder:validation:Enum=AnyOrder;InOrder
	StartupPolicyOrder StartupPolicyOptions `json:"startupPolicyOrder"`
}

// Coordinator defines which pod can be marked as the coordinator for the JobSet workload.
type Coordinator struct {
	// ReplicatedJob is the name of the ReplicatedJob which contains
	// the coordinator pod.
	ReplicatedJob string `json:"replicatedJob"`

	// JobIndex is the index of Job which contains the coordinator pod
	// (i.e., for a ReplicatedJob with N replicas, there are Job indexes 0 to N-1).
	JobIndex int `json:"jobIndex,omitempty"`

	// PodIndex is the Job completion index of the coordinator pod.
	PodIndex int `json:"podIndex,omitempty"`
}

func init() {
	SchemeBuilder.Register(&JobSet{}, &JobSetList{})
}
