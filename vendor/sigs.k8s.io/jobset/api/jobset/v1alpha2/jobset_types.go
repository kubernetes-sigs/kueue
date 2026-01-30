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
	corev1 "k8s.io/api/core/v1"
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
	// JobSetStartupPolicyInProgress means the StartupPolicy is in progress.
	JobSetStartupPolicyInProgress JobSetConditionType = "StartupPolicyInProgress"
	// JobSetStartupPolicyCompleted means the StartupPolicy has completed.
	JobSetStartupPolicyCompleted JobSetConditionType = "StartupPolicyCompleted"
)

// JobSetSpec defines the desired state of JobSet
// +kubebuilder:validation:XValidation:rule="!(has(self.startupPolicy) && self.startupPolicy.startupPolicyOrder == 'InOrder' && self.replicatedJobs.exists(x, has(x.dependsOn)))",message="StartupPolicy and DependsOn APIs are mutually exclusive"
type JobSetSpec struct {
	// replicatedJobs is the group of jobs that will form the set.
	// +listType=map
	// +listMapKey=name
	ReplicatedJobs []ReplicatedJob `json:"replicatedJobs,omitempty"`

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
	// with a custom value for this field.
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
	// +kubebuilder:validation:Minimum=0
	// +optional
	TTLSecondsAfterFinished *int32 `json:"ttlSecondsAfterFinished,omitempty"`

	// volumeClaimPolicies is a list of policies for persistent volume claims that pods are allowed
	// to reference. JobSet controller automatically adds the required volume claims to the
	// pod template. Every claim in this list must have at least one matching (by name)
	// volumeMount in one container in the template.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="Value is immutable"
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=50
	VolumeClaimPolicies []VolumeClaimPolicy `json:"volumeClaimPolicies,omitempty"`
}

// JobSetStatus defines the observed state of JobSet
type JobSetStatus struct {
	// conditions track status
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// restarts tracks the number of times the JobSet has restarted (i.e. recreated in case of RecreateAll policy).
	// +optional
	Restarts int32 `json:"restarts"`

	// restartsCountTowardsMax tracks the number of times the JobSet has restarted that counts towards the maximum allowed number of restarts.
	// +optional
	RestartsCountTowardsMax int32 `json:"restartsCountTowardsMax,omitempty"`

	// terminalState tracks the state of the JobSet when it finishes execution.
	// It can be either Completed or Failed. Otherwise, it is empty by default.
	// +optional
	TerminalState string `json:"terminalState,omitempty"`

	// replicatedJobsStatus tracks the number of JobsReady for each replicatedJob.
	// +optional
	// +listType=map
	// +listMapKey=name
	ReplicatedJobsStatus []ReplicatedJobStatus `json:"replicatedJobsStatus,omitempty"`

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
	// +listType=map
	// +listMapKey=name
	DependsOn []DependsOn `json:"dependsOn,omitempty"`
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
	// Fail the JobSet immediately, regardless of maxRestarts.
	FailJobSet FailurePolicyAction = "FailJobSet"

	// Restart the JobSet if the number of restart attempts is less than MaxRestarts.
	// Otherwise, fail the JobSet.
	RestartJobSet FailurePolicyAction = "RestartJobSet"

	// Do not count the failure against maxRestarts.
	RestartJobSetAndIgnoreMaxRestarts FailurePolicyAction = "RestartJobSetAndIgnoreMaxRestarts"
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
	// +kubebuilder:validation:Enum:=FailJobSet;RestartJobSet;RestartJobSetAndIgnoreMaxRestarts
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

func init() {
	SchemeBuilder.Register(&JobSet{}, &JobSetList{})
}
