/*
Copyright The Kubernetes Authors.
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
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	JobSetNameKey         string = "jobset.sigs.k8s.io/jobset-name"
	JobSetUIDKey          string = "jobset.sigs.k8s.io/jobset-uid"
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

	// GroupNameKey is a label/annotation set to the group name of the ReplicatedJob.
	// If a ReplicatedJob is part of a group, then its child jobs and pods have this
	// label/annotation equal to the group name
	GroupNameKey string = "jobset.sigs.k8s.io/group-name"
	// GroupReplicasKey is a label/annotation set to the total number of replicas in the group.
	// If a ReplicatedJob is part of a group, then its child jobs and pods have this
	// label/annotation equal to the total number of replicas in the group
	GroupReplicasKey string = "jobset.sigs.k8s.io/group-replicas"
	// JobGroupIndexKey is a label/annotation set to the index of the Job replica within its parent group.
	// If a ReplicatedJob is part of a group, then its child jobs and pods have this
	// label/annotation ranging from 0 to annotations[GroupReplicasKey] - 1
	JobGroupIndexKey string = "jobset.sigs.k8s.io/job-group-index"

	// InPlaceRestartAttemptKey is an annotation set to the in-place restart
	// attempt of the Pod. It is written by the agent. It should be
	// treated as *int32 (nil if missing) and the minimum value is 0. If the
	// in-place restart attempt of any Pod exceeds jobSet.spec.failurePolicy.maxRestarts,
	// the JobSet controller should fail the JobSet. If the in-place restart
	// attempt of the Pod is smaller than or equal to jobSet.status.previousInPlaceRestartAttempt,
	// the agent should restart its Pod in-place. If the in-place restart
	// attempt of the Pod is equal to jobSet.status.currentInPlaceRestartAttempt,
	// the agent should lift its barrier to allow the worker
	// container to start running.
	InPlaceRestartAttemptKey string = "jobset.sigs.k8s.io/in-place-restart-attempt"

	// DisableInPlaceRestartKey is an annotation that can be set in the JobSet to
	// disable the in-place restart logic in the agent. If this annotation
	// is set, the in-place restart agent will bypass the synchronization barrier
	// and run the worker immediately when the agent is started.
	// This is useful for cases that the agent is used but the in-place restart
	// strategy is not used (or the feature gate is not enabled). One example is
	// downgrading the JobSet CRD to a version that does not support in-place restart
	DisableInPlaceRestartKey string = "jobset.sigs.k8s.io/disable-in-place-restart"
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
	// JobSetRestarting means the JobSet is restarting.
	JobSetRestarting JobSetConditionType = "RestartingJobSet"
	// JobSetStartupPolicyInProgress means the StartupPolicy is in progress.
	JobSetStartupPolicyInProgress JobSetConditionType = "StartupPolicyInProgress"
	// JobSetStartupPolicyCompleted means the StartupPolicy has completed.
	JobSetStartupPolicyCompleted JobSetConditionType = "StartupPolicyCompleted"
)

// JobSetSpec defines the desired state of JobSet
// +kubebuilder:validation:XValidation:rule="!(has(self.startupPolicy) && self.startupPolicy.startupPolicyOrder == 'InOrder' && self.replicatedJobs.exists(x, has(x.dependsOn)))",message="StartupPolicy and DependsOn APIs are mutually exclusive"
// +kubebuilder:validation:XValidation:rule="!(has(self.scheduling) && (!has(self.scheduling.replicatedJobs) || size(self.scheduling.replicatedJobs) == 0)) || ((!has(self.startupPolicy) || self.startupPolicy.startupPolicyOrder != 'InOrder') && (!has(self.replicatedJobs) || self.replicatedJobs.all(rj, !has(rj.dependsOn) || size(rj.dependsOn) == 0)))",message="top-level scheduling (spec.scheduling without per-ReplicatedJob replicatedJobs entries, including the defaulted top-level gang) cannot be set with DependsOn or InOrder startup; configure per-ReplicatedJob scheduling under spec.scheduling.replicatedJobs instead",fieldPath=".scheduling"
type JobSetSpec struct {
	// replicatedJobs is the group of jobs that will form the set.
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	ReplicatedJobs []ReplicatedJob `json:"replicatedJobs,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// network defines the networking options for the jobset.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	Network *Network `json:"network,omitempty"`

	// successPolicy configures when to declare the JobSet as
	// succeeded.
	// The JobSet is always declared succeeded if all jobs in the set
	// finished with status complete.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	SuccessPolicy *SuccessPolicy `json:"successPolicy,omitempty"`

	// failurePolicy configures when to declare the JobSet as
	// failed.
	// The JobSet is always declared failed if any job in the set
	// finished with status failed.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	FailurePolicy *FailurePolicy `json:"failurePolicy,omitempty"`

	// startupPolicy configures in what order jobs must be started
	// Deprecated: StartupPolicy is deprecated, please use the DependsOn API.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	StartupPolicy *StartupPolicy `json:"startupPolicy,omitempty"`

	// suspend suspends all running child Jobs when set to true.
	Suspend *bool `json:"suspend,omitempty"` //nolint

	// coordinator can be used to assign a specific pod as the coordinator for
	// the JobSet. If defined, an annotation will be added to all Jobs and pods with
	// coordinator pod, which contains the stable network endpoint where the
	// coordinator pod can be reached.
	// jobset.sigs.k8s.io/coordinator=<pod hostname>.<headless service>
	// +optional
	Coordinator *Coordinator `json:"coordinator,omitempty"`

	// managedBy is used to indicate the controller or entity that manages a JobSet.
	// The built-in JobSet controller reconciles JobSets which don't have this
	// field at all or the field value is the reserved string
	// `jobset.sigs.k8s.io/jobset-controller`, but skips reconciling JobSets
	// with a custom value for this field. Note that ttlSecondsAfterFinished
	// cleanup still applies to externally managed JobSets once the managing
	// controller has recorded a terminal condition.
	//
	// The value must be a valid domain-prefixed path (e.g. acme.io/foo) -
	// all characters before the first "/" must be a valid subdomain as defined
	// by RFC 1123. All characters trailing the first "/" must be valid HTTP Path
	// characters as defined by RFC 3986. The value cannot exceed 63 characters.
	// The field is immutable.
	// +optional
	ManagedBy *string `json:"managedBy,omitempty"`

	// ttlSecondsAfterFinished limits the lifetime of a JobSet that has finished
	// execution (either Complete or Failed). If this field is set,
	// TTLSecondsAfterFinished after the JobSet finishes, it is eligible to be
	// automatically deleted. When the JobSet is being deleted, its lifecycle
	// guarantees (e.g. finalizers) will be honored. If this field is unset,
	// the JobSet won't be automatically deleted. If this field is set to zero,
	// the JobSet becomes eligible to be deleted immediately after it finishes.
	// For JobSets that set managedBy to an external controller, TTL cleanup
	// becomes eligible only after that controller records a terminal
	// (Completed or Failed) condition.
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// activeDeadlineSeconds is the maximum duration in seconds, relative to
	// status.startTime, that the JobSet may be continuously active before the
	// JobSet controller marks it Failed and deletes its active Jobs.
	//
	// The timer does not accrue while the JobSet is suspended: startTime is
	// cleared on suspend and reset when the JobSet resumes. The value must be a
	// positive integer. The field is mutable: operators may raise or lower the
	// deadline on a running JobSet, matching batch/v1.Job. The controller
	// recomputes the remaining time from status.startTime on the next reconcile,
	// so no timer state is reset on update.
	// +kubebuilder:validation:Minimum=1
	// +optional
	ActiveDeadlineSeconds *int64 `json:"activeDeadlineSeconds,omitempty"`

	// volumeClaimPolicies is a list of policies for persistent volume claims that pods are allowed
	// to reference. JobSet controller automatically adds the required volume claims to the
	// pod template. Every claim in this list must have at least one matching (by name)
	// volumeMount in one container in the template.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=50
	VolumeClaimPolicies []VolumeClaimPolicy `json:"volumeClaimPolicies,omitempty"`

	// scheduling defines the Workload-Aware Scheduling configuration for this JobSet.
	// When nil, no scheduling objects are created and behavior is unchanged.
	// When set (even to {}), the controller compiles a Workload resource
	// (containing PodGroupTemplates) and materializes the corresponding
	// PodGroup objects for the scheduler.
	// Requires the JobSetWorkloadAwareSchedulingAPI feature gate.
	// +optional
	Scheduling *JobSetScheduling `json:"scheduling,omitempty"`
}

// JobSetStatus defines the observed state of JobSet
type JobSetStatus struct {
	// conditions track status
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// startTime is the timestamp from which activeDeadlineSeconds is measured, and
	// also serves as the JobSet's active-start time. It is set when the JobSet
	// first becomes active (unsuspended), cleared when the JobSet is suspended, and
	// reset to the current time when the JobSet resumes, matching
	// batch/v1.Job.status.startTime. It is maintained only while the
	// JobSetActiveDeadlineSeconds feature gate is enabled (and cleared while the
	// gate is off), independent of whether activeDeadlineSeconds is set.
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`

	// restarts tracks the number of times the JobSet has been globally restarted.
	// That is, restarts is the number of times the restart action RestartJobSet or RestartJobSetAndIgnoreMaxRestarts have been executed and led to the recreation of all Jobs.
	// +optional
	Restarts int32 `json:"restarts"`

	// restartsCountTowardsMax tracks the number of times the JobSet has been globally restarted that counts towards the maximum allowed number of restarts.
	// That is, restartsCountTowardsMax is the number of times the restart action RestartJobSet has been executed and led to the recreation of all Jobs.
	// +optional
	RestartsCountTowardsMax int32 `json:"restartsCountTowardsMax,omitempty"`

	// executionAttempts tracks the number of execution lifecycles of the JobSet.
	// A value of nil indicates the JobSet has not yet executed (e.g. created suspended).
	// A value of 0 indicates the initial execution attempt, and values > 0 indicate
	// subsequent execution attempts triggered by failure policy restarts or suspend/resume cycles.
	// +optional
	// +kubebuilder:validation:Minimum=0
	ExecutionAttempts *int32 `json:"executionAttempts,omitempty"`

	// terminalState tracks the state of the JobSet when it finishes execution.
	// It can be either Completed or Failed. Otherwise, it is empty by default.
	// +optional
	TerminalState string `json:"terminalState,omitempty"`

	// replicatedJobsStatus tracks the number of JobsReady for each replicatedJob.
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	ReplicatedJobsStatus []ReplicatedJobStatus `json:"replicatedJobsStatus,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// previousInPlaceRestartAttempt tracks the previous in-place restart attempt
	// of the JobSet. It is read by the agent. If the in-place restart
	// attempt of the Pod is smaller than or equal to previousInPlaceRestartAttempt,
	// the agent should restart its Pod in-place.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +featureGate=InPlaceRestart
	PreviousInPlaceRestartAttempt *int32 `json:"previousInPlaceRestartAttempt,omitempty"`

	// currentInPlaceRestartAttempt tracks the current in-place restart attempt
	// of the JobSet. It is read by the agent. If the in-place restart
	// attempt of the Pod is equal to currentInPlaceRestartAttempt, the agent
	// should lift its barrier to allow the worker container to
	// start running.
	// +optional
	// +kubebuilder:validation:Minimum=0
	// +featureGate=InPlaceRestart
	CurrentInPlaceRestartAttempt *int32 `json:"currentInPlaceRestartAttempt,omitempty"`
}

// ReplicatedJobStatus defines the observed ReplicatedJobs Readiness.
type ReplicatedJobStatus struct {
	// name of the ReplicatedJob.
	Name string `json:"name"`

	// ready is the number of child Jobs where the number of ready pods and completed pods
	// is greater than or equal to the total expected pod count for the Job (i.e., the minimum
	// of job.spec.parallelism and job.spec.completions).
	Ready int32 `json:"ready"`

	// succeeded is the number of successfully completed child Jobs.
	Succeeded int32 `json:"succeeded"`

	// failed is the number of failed child Jobs.
	Failed int32 `json:"failed"`

	// active is the number of child Jobs with at least 1 pod in a running or pending state
	// which are not marked for deletion.
	Active int32 `json:"active"`

	// suspended is the number of child Jobs which are in a suspended state.
	Suspended int32 `json:"suspended"`

	// jobRestarts tracks the number of times the Jobs have been individually restarted.
	// That is, jobRestarts[jobIndex] is the number of times the restart action RestartJob or RestartJobAndIgnoreMaxRestarts have been executed for the Job with index jobIndex and led to its recreation without affecting the other Jobs.
	// +kubebuilder:validation:MaxItems=1024
	// +listType=atomic
	// +optional
	JobRestarts []int32 `json:"jobRestarts,omitempty"`

	// jobRestartsCountTowardsMax tracks the number of times the Jobs have been individually restarted that count towards the maximum allowed number of restarts.
	// That is, jobRestartsCountTowardsMax[jobIndex] is the number of times the restart action RestartJob has been executed for the Job with index jobIndex and led to its recreation without affecting the other Jobs.
	// +kubebuilder:validation:MaxItems=1024
	// +listType=atomic
	// +optional
	JobRestartsCountTowardsMax []int32 `json:"jobRestartsCountTowardsMax,omitempty"`
}

// +genclient
// +k8s:openapi-gen=true
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="TerminalState",JSONPath=".status.terminalState",type=string,description="Final state of JobSet"
// +kubebuilder:printcolumn:name="Restarts",JSONPath=".status.restarts",type=string,description="Number of restarts"
// +kubebuilder:printcolumn:name="Completed",type="string",priority=0,JSONPath=".status.conditions[?(@.type==\"Completed\")].status"
// +kubebuilder:printcolumn:name="Suspended",type="string",JSONPath=".spec.suspend",description="JobSet suspended"
// +kubebuilder:printcolumn:name="StartTime",type=date,JSONPath=`.status.startTime`,description="Time the JobSet became active"
// +kubebuilder:printcolumn:name="Age",JSONPath=".metadata.creationTimestamp",type=date,description="Time this JobSet was created"

// JobSet is the Schema for the jobsets API
type JobSet struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the object metadata for JobSet
	metav1.ObjectMeta `json:"metadata,omitempty"`
	// spec is the specification for jobset
	Spec JobSetSpec `json:"spec,omitempty"`
	// status is the status of the jobset
	Status JobSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true
// JobSetList contains a list of JobSet
type JobSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []JobSet `json:"items"`
}

type ReplicatedJob struct {
	// name is the name of the entry and will be used as a suffix
	// for the Job name.
	Name string `json:"name"`

	// groupName defines the name of the group this ReplicatedJob belongs to. Defaults to "default"
	// +kubebuilder:default=default
	GroupName string `json:"groupName,omitempty"`

	// template defines the template of the Job that will be created.
	Template batchv1.JobTemplateSpec `json:"template"`

	// replicas is the number of jobs that will be created from this ReplicatedJob's template.
	// Jobs names will be in the format: <jobSet.name>-<spec.replicatedJob.name>-<job-index>
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas,omitempty"`

	// dependsOn is an optional list that specifies the preceding ReplicatedJobs upon which
	// the current ReplicatedJob depends. If specified, the ReplicatedJob will be created
	// only after the referenced ReplicatedJobs reach their desired state.
	// The Order of ReplicatedJobs is defined by their enumeration in the slice.
	// Note, that the first ReplicatedJob in the slice cannot use the DependsOn API.
	// Currently, only a single item is supported in the DependsOn list.
	// If JobSet is suspended the all active ReplicatedJobs will be suspended. When JobSet is
	// resumed the Job sequence starts again.
	// This API is mutually exclusive with the StartupPolicy API.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +kubebuilder:validation:MaxItems=5
	// +optional
	// +patchMergeKey=name
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=name
	DependsOn []DependsOn `json:"dependsOn,omitempty" patchStrategy:"merge" patchMergeKey:"name"`
}

// DependsOn defines the dependency on the previous ReplicatedJob status.
type DependsOn struct {
	// name of the previous ReplicatedJob.
	Name string `json:"name"`

	// status defines the condition for the ReplicatedJob. Only Ready or Complete status can be set.
	// +kubebuilder:validation:Enum=Ready;Complete
	Status DependsOnStatus `json:"status"`
}

type DependsOnStatus string

const (
	// DependencyReady means the Ready + Succeeded + Failed counter
	// equals the number of child Jobs of the dependant ReplicatedJob.
	DependencyReady DependsOnStatus = "Ready"

	// DependencyComplete means the Succeeded counter
	// equals the number of child Jobs of the dependant ReplicatedJob.
	DependencyComplete DependsOnStatus = "Complete"
)

type Network struct {
	// enableDNSHostnames allows pods to be reached via their hostnames.
	// Pods will be reachable using the fully qualified pod hostname:
	// <jobSet.name>-<spec.replicatedJob.name>-<job-index>-<pod-index>.<subdomain>
	// +optional
	EnableDNSHostnames *bool `json:"enableDNSHostnames,omitempty"` //nolint

	// subdomain is an explicit choice for a network subdomain name
	// When set, any replicated job in the set is added to this network.
	// Defaults to <jobSet.name> if not set.
	// +optional
	Subdomain string `json:"subdomain,omitempty"`

	// publishNotReadyAddresses indicates if DNS records of pods should be published before the pods are ready.
	// Defaults to True.
	// +optional
	PublishNotReadyAddresses *bool `json:"publishNotReadyAddresses,omitempty"` //nolint
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
	// Fail the JobSet immediately.
	FailJobSet FailurePolicyAction = "FailJobSet"

	// Restart the JobSet globally (i.e., recreate all Jobs) if `jobSet.status.restartsCountTowardsMax + sum(jobSet.status.replicatedJobsStatus[].jobRestartsCountTowardsMax[]) < jobSet.spec.maxRestarts`.
	// Otherwise, fail the JobSet.
	// Bumps `jobSet.status.restarts += 1` and `jobSet.status.restartsCountTowardsMax += 1`
	RestartJobSet FailurePolicyAction = "RestartJobSet"

	// Same as RestartJobSet, but does not check `jobSet.spec.maxRestarts`.
	// Bumps `jobSet.status.restarts += 1`.
	RestartJobSetAndIgnoreMaxRestarts FailurePolicyAction = "RestartJobSetAndIgnoreMaxRestarts"

	// Restart the Job individually (i.e., recreate only the failed Job without touching the other Jobs) if `jobSet.status.restartsCountTowardsMax + sum(jobSet.status.replicatedJobsStatus[].jobRestartsCountTowardsMax[]) < jobSet.spec.maxRestarts`.
	// Otherwise, fail the JobSet.
	// Bumps `jobSet.status.replicatedJobsStatus[replicatedJobName].restarts[jobIndex] += 1` and `jobSet.status.replicatedJobsStatus[replicatedJobName].jobRestartsCountTowardsMax[jobIndex] += 1`.
	// Importantly, if this action is used, all `jobSet.spec.replicatedJobs[].replicas` must be less than or equal to 1024.
	RestartJob FailurePolicyAction = "RestartJob"

	// Same as RestartJob, but does not check `jobSet.spec.maxRestarts`.
	// Bumps `jobSet.status.replicatedJobsStatus[replicatedJobName].restarts[jobIndex] += 1`
	// Importantly, if this action is used, all `jobSet.spec.replicatedJobs[].replicas` must be less than or equal to 1024.
	RestartJobAndIgnoreMaxRestarts FailurePolicyAction = "RestartJobAndIgnoreMaxRestarts"
)

// FailurePolicyRule defines a FailurePolicyAction to be executed if a child job
// fails due to a reason listed in OnJobFailureReasons and a message pattern
// listed in OnJobFailureMessagePatterns. The rule must match both the job
// failure reason and the job failure message. The rules are evaluated in
// order and the first matching rule is executed.
type FailurePolicyRule struct {
	// name of the failure policy rule.
	// The name is defaulted to 'failurePolicyRuleN' where N is the index of the failure policy rule.
	// The name must match the regular expression "^[A-Za-z]([A-Za-z0-9_,:]*[A-Za-z0-9_])?$".
	Name string `json:"name"`
	// action to take if the rule is matched.
	// Valid values are FailJobSet, RestartJobSet, RestartJobSetAndIgnoreMaxRestarts, RestartJob, RestartJobAndIgnoreMaxRestarts.
	// +kubebuilder:validation:Enum:=FailJobSet;RestartJobSet;RestartJobSetAndIgnoreMaxRestarts;RestartJob;RestartJobAndIgnoreMaxRestarts
	Action FailurePolicyAction `json:"action"`
	// onJobFailureReasons is a list of job failures reasons.
	// The requirement is satisfied
	// if at least one reason matches the list. An empty list matches any job
	// failure reason.
	// +kubebuilder:validation:UniqueItems:true
	OnJobFailureReasons []string `json:"onJobFailureReasons,omitempty"`
	// onJobFailureMessagePatterns is a requirement on the job failure messages.
	// The requirement is satisfied
	// if at least one pattern (regex) matches the job failure message. An
	// empty list matches any job failure message.
	// The syntax of the regular expressions accepted is the same general
	// syntax used by Perl, Python, and other languages. More precisely, it is
	// the syntax accepted by RE2 and described at https://golang.org/s/re2syntax,
	// except for \C. For an overview of the syntax, see
	// https://pkg.go.dev/regexp/syntax.
	// +kubebuilder:validation:UniqueItems:true
	OnJobFailureMessagePatterns []string `json:"onJobFailureMessagePatterns,omitempty"`
	// targetReplicatedJobs are the names of the replicated jobs the operator applies to.
	// An empty list will apply to all replicatedJobs.
	// +optional
	// +listType=atomic
	TargetReplicatedJobs []string `json:"targetReplicatedJobs,omitempty"`
}

type FailurePolicy struct {
	// maxRestarts defines the limit on the number of JobSet restarts.
	// If the restart strategy "InPlaceRestart" is used, this field
	// also defines the limit on the number of container restarts of
	// any child container. This is required to handle the edge case
	// in which a container keeps failing too fast to complete a JobSet
	// restart.
	MaxRestarts int32 `json:"maxRestarts,omitempty"`

	// restartStrategy defines the strategy to use when restarting the JobSet.
	// Defaults to Recreate.
	// +optional
	// +kubebuilder:default=Recreate
	RestartStrategy JobSetRestartStrategy `json:"restartStrategy,omitempty"`

	// rules is a list of failure policy rules for this JobSet.
	// For a given Job failure, the rules will be evaluated in order,
	// and only the first matching rule will be executed.
	// If no matching rule is found, the RestartJobSet action is applied.
	Rules []FailurePolicyRule `json:"rules,omitempty"`
}

// +kubebuilder:validation:Enum=Recreate;BlockingRecreate;InPlaceRestart
type JobSetRestartStrategy string

const (
	// Restart the JobSet by recreating all Jobs. Each Job is recreated as soon
	// as its previous iteration (and its Pods) is deleted.
	Recreate JobSetRestartStrategy = "Recreate"

	// Restart the JobSet by recreating all Jobs. Ensures that all Jobs (and
	// their Pods) from the previous iteration are deleted before creating new
	// Jobs.
	BlockingRecreate JobSetRestartStrategy = "BlockingRecreate"

	// When no Job has failed, restart the JobSet by restarting healthy Pods
	// in-place and recreating failed Pods. When a Job has failed, fall back to
	// action "Recreate" and execute the matching failure policy rule.
	// Importantly, the in-place restart strategy assumes that Jobs never fail
	// because the backoffLimit is set to the max (this is enforced by the webhook).
	// If a job does fail, it is not optimal but it is also not a problem because
	// in-place restart can handle Pods being recreated. The JobSet controller will
	// recreate the failed Jobs as if the restart strategy is set to "Recreate".
	// The barrier is lifted only when all agents are ready and in the new "in-place
	// restart attempt".
	InPlaceRestart JobSetRestartStrategy = "InPlaceRestart"
)

type SuccessPolicy struct {
	// operator determines either All or Any of the selected jobs should succeed to consider the JobSet successful
	// +kubebuilder:validation:Enum=All;Any
	Operator Operator `json:"operator"`

	// targetReplicatedJobs are the names of the replicated jobs the operator will apply to.
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
	// startupPolicyOrder determines the startup order of the ReplicatedJobs.
	// AnyOrder means to start replicated jobs in any order.
	// InOrder means to start them as they are listed in the JobSet. A ReplicatedJob is started only
	// when all the jobs of the previous one are ready.
	// +kubebuilder:validation:Enum=AnyOrder;InOrder
	StartupPolicyOrder StartupPolicyOptions `json:"startupPolicyOrder"`
}

// Coordinator defines which pod can be marked as the coordinator for the JobSet workload.
type Coordinator struct {
	// replicatedJob is the name of the ReplicatedJob which contains
	// the coordinator pod.
	ReplicatedJob string `json:"replicatedJob"`

	// jobIndex is the index of Job which contains the coordinator pod
	// (i.e., for a ReplicatedJob with N replicas, there are Job indexes 0 to N-1).
	JobIndex int `json:"jobIndex,omitempty"`

	// podIndex is the Job completion index of the coordinator pod.
	PodIndex int `json:"podIndex,omitempty"`
}

// JobSetScheduling defines the Workload-Aware Scheduling configuration for a JobSet.
// A JobSet must configure scheduling using exactly one of two mutually exclusive
// models, since composite Gang-of-Gangs PodGroup hierarchies linking a parent
// PodGroup to leaf PodGroups are not implemented in alpha:
//   - the top-level (composite) model: set schedulingPolicy,
//     schedulingConstraints, and/or disruptionMode to configure a single PodGroup
//     covering the whole JobSet, and leave replicatedJobs unset.
//   - the per-ReplicatedJob (composite) model: set replicatedJobs and leave
//     schedulingPolicy, schedulingConstraints, and disruptionMode unset at the top
//     level. Every ReplicatedJob must then be targeted by exactly one
//     replicatedJobs entry, since there is no top-level policy for an
//     untargeted ReplicatedJob to fall back to.
//
// Sequenced startup (DependsOn or an InOrder StartupPolicy) creates Jobs
// sequentially, so a single top-level PodGroup could never reach its minCount. A
// JobSet using sequenced startup must therefore use the per-ReplicatedJob model;
// top-level scheduling (including the defaulted top-level gang) is rejected with
// sequenced startup.
//
// +kubebuilder:validation:XValidation:rule="!has(self.replicatedJobs) || size(self.replicatedJobs) == 0 || (!has(self.schedulingPolicy) && !has(self.schedulingConstraints) && !has(self.disruptionMode))",message="schedulingPolicy, schedulingConstraints, and disruptionMode cannot be set together with replicatedJobs"
type JobSetScheduling struct {
	// schedulingPolicy defines the composite-level scheduling policy for the entire JobSet.
	// Defaults to Gang when spec.scheduling is set but schedulingPolicy is nil and
	// replicatedJobs is not set.
	// Top-level scheduling, including this defaulted top-level gang, is forbidden with
	// sequenced startup (DependsOn or an InOrder StartupPolicy), since a single PodGroup
	// spanning the whole JobSet could never reach its minCount while Jobs are created
	// sequentially. Configure Gang policies under replicatedJobs instead in that case.
	// Mutually exclusive with replicatedJobs: see the type-level comment.
	// +optional
	// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.gang)",message="exactly one of basic or gang must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.gang) || !has(self.gang.minGroupCount)",message="minGroupCount is not supported until composite PodGroups are implemented"
	SchedulingPolicy *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// schedulingConstraints defines composite-level topology constraints for the entire JobSet.
	// Mutually exclusive with replicatedJobs: see the type-level comment.
	// +optional
	SchedulingConstraints *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`

	// disruptionMode defines how the entire composite group can be disrupted.
	// Mutually exclusive with replicatedJobs: see the type-level comment.
	// +optional
	DisruptionMode *schedulingv1alpha3.WorkloadCompositePodGroupDisruptionMode `json:"disruptionMode,omitempty"`

	// replicatedJobs specifies per-ReplicatedJob composite-level scheduling overrides.
	// Each entry targets one or more named ReplicatedJobs. Mutually exclusive with the
	// top-level schedulingPolicy, schedulingConstraints, and disruptionMode fields: see
	// the type-level comment. When set, every ReplicatedJob in the JobSet must be
	// targeted by exactly one entry.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=50
	ReplicatedJobs []ReplicatedJobScheduling `json:"replicatedJobs,omitempty"`
}

// ReplicatedJobScheduling targets one or more named ReplicatedJobs with
// composite-level scheduling configuration.
//
// +kubebuilder:validation:XValidation:rule="!has(self.job) || (size(self.targetReplicatedJobs) == 1 && !has(self.schedulingPolicy) && !has(self.schedulingConstraints) && !has(self.disruptionMode))",message="when job is set, targetReplicatedJobs must contain exactly one name and schedulingPolicy, schedulingConstraints, and disruptionMode must not be set"
type ReplicatedJobScheduling struct {
	// targetReplicatedJobs is the list of ReplicatedJob names this policy applies to.
	// When more than one name is listed, the targeted ReplicatedJobs share a single
	// PodGroup. Every name must be unique across all replicatedJobs entries.
	// The list is limited to a maximum of 50 jobs
	// and the length of each replicatedJobName can not exceed 256 characters.
	// +required
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=50
	// +kubebuilder:validation:items:MaxLength=256
	TargetReplicatedJobs []string `json:"targetReplicatedJobs,omitempty"`

	// schedulingPolicy defines the composite-level scheduling policy (basic or gang) for
	// jobs created by the targeted ReplicatedJobs.
	// Defaults to Gang when not specified.
	// +optional
	// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.gang)",message="exactly one of basic or gang must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.gang) || !has(self.gang.minGroupCount)",message="minGroupCount is not supported until composite PodGroups are implemented"
	SchedulingPolicy *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// schedulingConstraints defines composite-level topology constraints for the targeted
	// ReplicatedJobs' pods.
	// +optional
	SchedulingConstraints *schedulingv1alpha3.WorkloadCompositePodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`

	// disruptionMode defines how pods within the targeted ReplicatedJobs can be disrupted.
	// +optional
	DisruptionMode *schedulingv1alpha3.WorkloadCompositePodGroupDisruptionMode `json:"disruptionMode,omitempty"`

	// job defines job-level (replica-level) scheduling configuration,
	// where each replica of the targeted ReplicatedJobs forms its own independent gang
	// (i.e. one PodGroup per Job) instead of sharing a single PodGroup across every
	// replica of the targeted ReplicatedJobs. This is part of the Gang-of-Gangs model.
	// When set, targetReplicatedJobs must contain exactly one ReplicatedJob name, and
	// the composite-level schedulingPolicy, schedulingConstraints, and disruptionMode
	// fields on this ReplicatedJobScheduling must not be set, since they configure
	// a shared PodGroup that job replaces with one PodGroup per Job.
	// +optional
	Job *JobScheduling `json:"job,omitempty"`
}

// JobScheduling defines scheduling configuration applied at the individual
// Job (ReplicatedJob replica) level, enabling each replica to be scheduled as its
// own independent gang. This is part of the Gang-of-Gangs model: the JobSet
// controller compiles one PodGroupTemplate/PodGroup per Job (replica) of the
// targeted ReplicatedJob, sized to that Job's own parallelism, rather than one
// PodGroup shared across all of the ReplicatedJob's replicas.
type JobScheduling struct {
	// schedulingPolicy defines the scheduling policy (basic or gang) applied to each
	// replica of the targeted ReplicatedJobs.
	// +optional
	// +kubebuilder:validation:XValidation:rule="has(self.basic) != has(self.gang)",message="exactly one of basic or gang must be set"
	// +kubebuilder:validation:XValidation:rule="!has(self.gang) || !has(self.gang.minCount) || self.gang.minCount >= 1",message="minCount must be a positive integer"
	SchedulingPolicy *schedulingv1alpha3.WorkloadPodGroupSchedulingPolicy `json:"schedulingPolicy,omitempty"`

	// schedulingConstraints defines topology constraints applied to each replica of
	// the targeted ReplicatedJobs.
	// +optional
	SchedulingConstraints *schedulingv1alpha3.WorkloadPodGroupSchedulingConstraints `json:"schedulingConstraints,omitempty"`

	// disruptionMode defines how pods within a single replica can be disrupted.
	// +optional
	DisruptionMode *schedulingv1alpha3.WorkloadPodGroupDisruptionMode `json:"disruptionMode,omitempty"`

	// resourceClaims defines claims shared by all pods in each Job's PodGroup.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=4
	ResourceClaims []schedulingv1alpha3.WorkloadPodGroupResourceClaim `json:"resourceClaims,omitempty"`
}

// volumeClaimPolicy defines volume claim templates and lifecycle management for shared PVCs.
// +kubebuilder:validation:XValidation:rule="self.templates.all(t, !has(t.metadata.namespace) || size(t.metadata.namespace) == 0)",message="namespace cannot be set for VolumeClaimPolicies templates"
type VolumeClaimPolicy struct {
	// templates is a list of shared PVC claims that ReplicatedJobs are allowed to reference.
	// The JobSet controller is responsible for creating shared PVCs that can be mounted by
	// multiple ReplicatedJobs. Every claim in this list must have a matching (by name)
	// volumeMount in one container or initContainer in at least one ReplicatedJob template.
	// ReplicatedJob template must not have volumes with the same name as defined in this template.
	// PVC template must not have the namespace parameter.
	// Generated PVC naming convention: <claim-name>-<jobset-name>
	// Example: "model-cache-trainjob" (shared volume across all ReplicatedJobs).
	// +kubebuilder:validation:MaxItems=50
	// +optional
	// +listType=atomic
	Templates []corev1.PersistentVolumeClaim `json:"templates,omitempty"`

	// retentionPolicy describes the lifecycle of persistent volume claims created from the template.
	// By default, all persistent volume claims are deleted once JobSet is deleted.
	// +optional
	RetentionPolicy *VolumeRetentionPolicy `json:"retentionPolicy,omitempty"`
}

// volumeRetentionPolicy defines the retention policy used for PVCs created from the JobSet VolumeClaimPolicies.
type VolumeRetentionPolicy struct {
	// whenDeleted specifies what happens to PVCs when JobSet is deleted.
	// +optional
	// +kubebuilder:default=Delete
	WhenDeleted *RetentionPolicyType `json:"whenDeleted,omitempty"`
}

// retentionPolicyType defines the retention policy for PVCs.
// +kubebuilder:validation:Enum=Delete;Retain
type RetentionPolicyType string

const (
	// retentionPolicyDelete is the default retention policy and specifies that
	// PVC associated with JobSet VolumeClaimTemplates will be deleted.
	RetentionPolicyDelete RetentionPolicyType = "Delete"

	// retentionPolicyRetain is the retention policy and specifies that
	// PVC associated with JobSet VolumeClaimTemplates will not be deleted.
	RetentionPolicyRetain RetentionPolicyType = "Retain"
)
