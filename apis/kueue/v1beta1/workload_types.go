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

package v1beta1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadSpec defines the desired state of Workload
// +kubebuilder:validation:XValidation:rule="has(self.priorityClassName) ? has(self.priority) : true", message="priority should not be nil when priorityClassName is set"
type WorkloadSpec struct {
	// podSets is a list of sets of homogeneous pods, each described by a Pod spec
	// and a count.
	// There must be at least one element and at most 8.
	// podSets cannot be changed.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:MinItems=1
	PodSets []PodSet `json:"podSets"`

	// queueName is the name of the LocalQueue the Workload is associated with.
	// queueName cannot be changed while .status.admission is not null.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	QueueName string `json:"queueName,omitempty"`

	// If specified, indicates the workload's priority.
	// "system-node-critical" and "system-cluster-critical" are two special
	// keywords which indicate the highest priorities with the former being
	// the highest priority. Any other name must be defined by creating a
	// PriorityClass object with that name. If not specified, the workload
	// priority will be default or zero if there is no default.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// Priority determines the order of access to the resources managed by the
	// ClusterQueue where the workload is queued.
	// The priority value is populated from PriorityClassName.
	// The higher the value, the higher the priority.
	// If priorityClassName is specified, priority must not be null.
	Priority *int32 `json:"priority,omitempty"`

	// priorityClassSource determines whether the priorityClass field refers to a pod PriorityClass or kueue.x-k8s.io/workloadpriorityclass.
	// Workload's PriorityClass can accept the name of a pod priorityClass or a workloadPriorityClass.
	// When using pod PriorityClass, a priorityClassSource field has the scheduling.k8s.io/priorityclass value.
	// +kubebuilder:default=""
	// +kubebuilder:validation:Enum=kueue.x-k8s.io/workloadpriorityclass;scheduling.k8s.io/priorityclass;""
	PriorityClassSource string `json:"priorityClassSource,omitempty"`

	// Active determines if a workload can be admitted into a queue.
	// Changing active from true to false will evict any running workloads.
	// Possible values are:
	//
	//   - false: indicates that a workload should never be admitted and evicts running workloads
	//   - true: indicates that a workload can be evaluated for admission into it's respective queue.
	//
	// Defaults to true
	// +kubebuilder:default=true
	Active *bool `json:"active,omitempty"`

	// maximumExecutionTimeSeconds if provided, determines the maximum time, in seconds,
	// the workload can be admitted before it's automatically deactivated.
	//
	// If unspecified, no execution time limit is enforced on the Workload.
	//
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaximumExecutionTimeSeconds *int32 `json:"maximumExecutionTimeSeconds,omitempty"`
}

// PodSetTopologyRequest defines the topology request for a PodSet.
type PodSetTopologyRequest struct {
	// required indicates the topology level required by the PodSet, as
	// indicated by the `kueue.x-k8s.io/podset-required-topology` PodSet
	// annotation.
	//
	// +optional
	Required *string `json:"required,omitempty"`

	// preferred indicates the topology level preferred by the PodSet, as
	// indicated by the `kueue.x-k8s.io/podset-preferred-topology` PodSet
	// annotation.
	//
	// +optional
	Preferred *string `json:"preferred,omitempty"`
}

type Admission struct {
	// clusterQueue is the name of the ClusterQueue that admitted this workload.
	ClusterQueue ClusterQueueReference `json:"clusterQueue"`

	// PodSetAssignments hold the admission results for each of the .spec.podSets entries.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	PodSetAssignments []PodSetAssignment `json:"podSetAssignments"`
}

type PodSetAssignment struct {
	// Name is the name of the podSet. It should match one of the names in .spec.podSets.
	// +kubebuilder:default=main
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^(?i)[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name"`

	// Flavors are the flavors assigned to the workload for each resource.
	Flavors map[corev1.ResourceName]ResourceFlavorReference `json:"flavors,omitempty"`

	// resourceUsage keeps track of the total resources all the pods in the podset need to run.
	//
	// Beside what is provided in podSet's specs, this calculation takes into account
	// the LimitRange defaults and RuntimeClass overheads at the moment of admission.
	// This field will not change in case of quota reclaim.
	ResourceUsage corev1.ResourceList `json:"resourceUsage,omitempty"`

	// count is the number of pods taken into account at admission time.
	// This field will not change in case of quota reclaim.
	// Value could be missing for Workloads created before this field was added,
	// in that case spec.podSets[*].count value will be used.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	Count *int32 `json:"count,omitempty"`

	// topologyAssignment indicates the topology assignment divided into
	// topology domains corresponding to the lowest level of the topology.
	// The assignment specifies the number of Pods to be scheduled per topology
	// domain and specifies the node selectors for each topology domain, in the
	// following way: the node selector keys are specified by the levels field
	// (same for all domains), and the corresponding node selector value is
	// specified by the domains.values subfield.
	//
	// Example:
	//
	// topologyAssignment:
	//   levels:
	//   - cloud.provider.com/topology-block
	//   - cloud.provider.com/topology-rack
	//   domains:
	//   - values: [block-1, rack-1]
	//     count: 4
	//   - values: [block-1, rack-2]
	//     count: 2
	//
	// Here:
	// - 4 Pods are to be scheduled on nodes matching the node selector:
	//   cloud.provider.com/topology-block: block-1
	//   cloud.provider.com/topology-rack: rack-1
	// - 2 Pods are to be scheduled on nodes matching the node selector:
	//   cloud.provider.com/topology-block: block-1
	//   cloud.provider.com/topology-rack: rack-2
	//
	// +optional
	TopologyAssignment *TopologyAssignment `json:"topologyAssignment,omitempty"`
}

type TopologyAssignment struct {
	// levels is an ordered list of keys denoting the levels of the assigned
	// topology (i.e. node label keys), from the highest to the lowest level of
	// the topology.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Levels []string `json:"levels"`

	// domains is a list of topology assignments split by topology domains at
	// the lowest level of the topology.
	//
	// +required
	Domains []TopologyDomainAssignment `json:"domains"`
}

type TopologyDomainAssignment struct {
	// values is an ordered list of node selector values describing a topology
	// domain. The values correspond to the consecutive topology levels, from
	// the highest to the lowest.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Values []string `json:"values"`

	// count indicates the number of Pods to be scheduled in the topology
	// domain indicated by the values field.
	//
	// +required
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`
}

// +kubebuilder:validation:XValidation:rule="has(self.minCount) ? self.minCount <= self.count : true", message="minCount should be positive and less or equal to count"
type PodSet struct {
	// name is the PodSet name.
	// +kubebuilder:default=main
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name,omitempty"`

	// template is the Pod template.
	//
	// The only allowed fields in template.metadata are labels and annotations.
	//
	// If requests are omitted for a container or initContainer,
	// they default to the limits if they are explicitly specified for the
	// container or initContainer.
	//
	// During admission, the rules in nodeSelector and
	// nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution that match
	// the keys in the nodeLabels from the ResourceFlavors considered for this
	// Workload are used to filter the ResourceFlavors that can be assigned to
	// this podSet.
	Template corev1.PodTemplateSpec `json:"template"`

	// count is the number of pods for the spec.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	Count int32 `json:"count"`

	// minCount is the minimum number of pods for the spec acceptable
	// if the workload supports partial admission.
	//
	// If not provided, partial admission for the current PodSet is not
	// enabled.
	//
	// Only one podSet within the workload can use this.
	//
	// This is an alpha field and requires enabling PartialAdmission feature gate.
	//
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinCount *int32 `json:"minCount,omitempty"`

	// topologyRequest defines the topology request for the PodSet.
	//
	// +optional
	TopologyRequest *PodSetTopologyRequest `json:"topologyRequest,omitempty"`
}

// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
	// admission holds the parameters of the admission of the workload by a
	// ClusterQueue. admission can be set back to null, but its fields cannot be
	// changed once set.
	Admission *Admission `json:"admission,omitempty"`

	// requeueState holds the re-queue state
	// when a workload meets Eviction with PodsReadyTimeout reason.
	//
	// +optional
	RequeueState *RequeueState `json:"requeueState,omitempty"`

	// conditions hold the latest available observations of the Workload
	// current state.
	//
	// The type of the condition could be:
	//
	// - Admitted: the Workload was admitted through a ClusterQueue.
	// - Finished: the associated workload finished running (failed or succeeded).
	// - PodsReady: at least `.spec.podSets[*].count` Pods are ready or have
	// succeeded.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// reclaimablePods keeps track of the number pods within a podset for which
	// the resource reservation is no longer needed.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	ReclaimablePods []ReclaimablePod `json:"reclaimablePods,omitempty"`

	// admissionChecks list all the admission checks required by the workload and the current status
	// +optional
	// +listType=map
	// +listMapKey=name
	// +patchStrategy=merge
	// +patchMergeKey=name
	// +kubebuilder:validation:MaxItems=8
	AdmissionChecks []AdmissionCheckState `json:"admissionChecks,omitempty" patchStrategy:"merge" patchMergeKey:"name"`

	// resourceRequests provides a detailed view of the resources that were
	// requested by a non-admitted workload when it was considered for admission.
	// If admission is non-null, resourceRequests will be empty because
	// admission.resourceUsage contains the detailed information.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	ResourceRequests []PodSetRequest `json:"resourceRequests,omitempty"`

	// accumulatedPastExexcutionTimeSeconds holds the total time, in seconds, the workload spent
	// in Admitted state, in the previous `Admit` - `Evict` cycles.
	//
	// +optional
	AccumulatedPastExexcutionTimeSeconds *int32 `json:"accumulatedPastExexcutionTimeSeconds,omitempty"`
}

type RequeueState struct {
	// count records the number of times a workload has been re-queued
	// When a deactivated (`.spec.activate`=`false`) workload is reactivated (`.spec.activate`=`true`),
	// this count would be reset to null.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	Count *int32 `json:"count,omitempty"`

	// requeueAt records the time when a workload will be re-queued.
	// When a deactivated (`.spec.activate`=`false`) workload is reactivated (`.spec.activate`=`true`),
	// this time would be reset to null.
	//
	// +optional
	RequeueAt *metav1.Time `json:"requeueAt,omitempty"`
}

type AdmissionCheckState struct {
	// name identifies the admission check.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=316
	Name string `json:"name"`
	// state of the admissionCheck, one of Pending, Ready, Retry, Rejected
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Enum=Pending;Ready;Retry;Rejected
	State CheckState `json:"state"`
	// lastTransitionTime is the last time the condition transitioned from one status to another.
	// This should be when the underlying condition changed.  If that is not known, then using the time when the API field changed is acceptable.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
	// message is a human readable message indicating details about the transition.
	// This may be an empty string.
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=32768
	Message string `json:"message" protobuf:"bytes,6,opt,name=message"`

	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	PodSetUpdates []PodSetUpdate `json:"podSetUpdates,omitempty"`
}

// PodSetUpdate contains a list of pod set modifications suggested by AdmissionChecks.
// The modifications should be additive only - modifications of already existing keys
// or having the same key provided by multiple AdmissionChecks is not allowed and will
// result in failure during workload admission.
type PodSetUpdate struct {
	// Name of the PodSet to modify. Should match to one of the Workload's PodSets.
	// +required
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// +optional
	Labels map[string]string `json:"labels,omitempty"`

	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`

	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`

	// +optional
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(x, !has(x.key) ? x.operator == 'Exists' : true)", message="operator must be Exists when 'key' is empty, which means 'match all values and all keys'"
	// +kubebuilder:validation:XValidation:rule="self.all(x, has(x.tolerationSeconds) ? x.effect == 'NoExecute' : true)", message="effect must be 'NoExecute' when 'tolerationSeconds' is set"
	// +kubebuilder:validation:XValidation:rule="self.all(x, !has(x.operator) || x.operator in ['Equal', 'Exists'])", message="supported toleration values: 'Equal'(default), 'Exists'"
	// +kubebuilder:validation:XValidation:rule="self.all(x, has(x.operator) && x.operator == 'Exists' ? !has(x.value) : true)", message="a value must be empty when 'operator' is 'Exists'"
	// +kubebuilder:validation:XValidation:rule="self.all(x, !has(x.effect) || x.effect in ['NoSchedule', 'PreferNoSchedule', 'NoExecute'])", message="supported taint effect values: 'NoSchedule', 'PreferNoSchedule', 'NoExecute'"
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

type ReclaimablePod struct {
	// name is the PodSet name.
	Name string `json:"name"`

	// count is the number of pods for which the requested resources are no longer needed.
	// +kubebuilder:validation:Minimum=0
	Count int32 `json:"count"`
}

type PodSetRequest struct {
	// name is the name of the podSet. It should match one of the names in .spec.podSets.
	// +kubebuilder:default=main
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^(?i)[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name"`

	// resources is the total resources all the pods in the podset need to run.
	//
	// Beside what is provided in podSet's specs, this value also takes into account
	// the LimitRange defaults and RuntimeClass overheads at the moment of consideration
	// and the application of resource.excludeResourcePrefixes and resource.transformations.
	// +optional
	Resources corev1.ResourceList `json:"resources,omitempty"`
}

const (
	// WorkloadAdmitted means that the Workload has reserved quota and all the admissionChecks
	// defined in the ClusterQueue are satisfied.
	WorkloadAdmitted = "Admitted"

	// WorkloadQuotaReserved means that the Workload has reserved quota a ClusterQueue.
	WorkloadQuotaReserved = "QuotaReserved"

	// WorkloadFinished means that the workload associated to the
	// ResourceClaim finished running (failed or succeeded).
	WorkloadFinished = "Finished"

	// WorkloadPodsReady means that at least `.spec.podSets[*].count` Pods are
	// ready or have succeeded.
	WorkloadPodsReady = "PodsReady"

	// WorkloadEvicted means that the Workload was evicted. The possible reasons
	// for this condition are:
	// - "Preempted": the workload was preempted
	// - "PodsReadyTimeout": the workload exceeded the PodsReady timeout
	// - "AdmissionCheck": at least one admission check transitioned to False
	// - "ClusterQueueStopped": the ClusterQueue is stopped
	// - "Deactivated": the workload has spec.active set to false
	// When a workload is preempted, this condition is accompanied by the "Preempted"
	// condition which contains a more detailed reason for the preemption.
	WorkloadEvicted = "Evicted"

	// WorkloadPreempted means that the Workload was preempted.
	// The possible values of the reason field are "InClusterQueue", "InCohort".
	// In the future more reasons can be introduced, including those conveying
	// more detailed information. The more detailed reasons should be prefixed
	// by one of the "base" reasons.
	WorkloadPreempted = "Preempted"

	// WorkloadRequeued means that the Workload was requeued due to eviction.
	WorkloadRequeued = "Requeued"

	// WorkloadDeactivationTarget means that the Workload should be deactivated.
	// This condition is temporary, so it should be removed after deactivation.
	WorkloadDeactivationTarget = "DeactivationTarget"
)

// Reasons for the WorkloadPreempted condition.
const (
	// InClusterQueueReason indicates the Workload was preempted due to
	// prioritization in the ClusterQueue.
	InClusterQueueReason string = "InClusterQueue"

	// InCohortReclamationReason indicates the Workload was preempted due to
	// reclamation within the Cohort.
	InCohortReclamationReason string = "InCohortReclamation"

	// InCohortFairSharingReason indicates the Workload was preempted due to
	// fair sharing within the cohort.
	InCohortFairSharingReason string = "InCohortFairSharing"

	// InCohortReclaimWhileBorrowingReason indicates the Workload was preempted
	// due to reclamation within the cohort while borrowing.
	InCohortReclaimWhileBorrowingReason string = "InCohortReclaimWhileBorrowing"
)

const (
	// WorkloadInadmissible means that the Workload can't reserve quota
	// due to LocalQueue or ClusterQueue doesn't exist or inactive.
	WorkloadInadmissible = "Inadmissible"

	// WorkloadEvictedByPreemption indicates that the workload was evicted
	// in order to free resources for a workload with a higher priority.
	WorkloadEvictedByPreemption = "Preempted"

	// WorkloadEvictedByPodsReadyTimeout indicates that the eviction took
	// place due to a PodsReady timeout.
	WorkloadEvictedByPodsReadyTimeout = "PodsReadyTimeout"

	// WorkloadEvictedByAdmissionCheck indicates that the workload was evicted
	// because at least one admission check transitioned to False.
	WorkloadEvictedByAdmissionCheck = "AdmissionCheck"

	// WorkloadEvictedByClusterQueueStopped indicates that the workload was evicted
	// because the ClusterQueue is Stopped.
	WorkloadEvictedByClusterQueueStopped = "ClusterQueueStopped"

	// WorkloadEvictedByLocalQueueStopped indicates that the workload was evicted
	// because the LocalQueue is Stopped.
	WorkloadEvictedByLocalQueueStopped = "LocalQueueStopped"

	// WorkloadEvictedByDeactivation indicates that the workload was evicted
	// because spec.active is set to false.
	// Deprecated: The reason is not set any longer, it is only kept temporarily to ensure
	// pre-existing deactivated workloads remain deactivated after upgrade from version
	// prior to 0.10. The reason declaration can be removed in 0.11.
	WorkloadEvictedByDeactivation = "InactiveWorkload"

	// WorkloadDeactivated indicates that the workload was evicted
	// because spec.active is set to false.
	WorkloadDeactivated = "Deactivated"

	// WorkloadReactivated indicates that the workload was requeued because
	// spec.active is set to true after deactivation.
	WorkloadReactivated = "Reactivated"

	// WorkloadBackoffFinished indicates that the workload was requeued because
	// backoff finished.
	WorkloadBackoffFinished = "BackoffFinished"

	// WorkloadClusterQueueRestarted indicates that the workload was requeued because
	// cluster queue was restarted after being stopped.
	WorkloadClusterQueueRestarted = "ClusterQueueRestarted"

	// WorkloadLocalQueueRestarted indicates that the workload was requeued because
	// local queue was restarted after being stopped.
	WorkloadLocalQueueRestarted = "LocalQueueRestarted"

	// WorkloadRequeuingLimitExceeded indicates that the workload exceeded max number
	// of re-queuing retries.
	WorkloadRequeuingLimitExceeded = "RequeuingLimitExceeded"

	// WorkloadMaximumExecutionTimeExceeded indicates that the workload exceeded its
	// maximum execution time.
	WorkloadMaximumExecutionTimeExceeded = "MaximumExecutionTimeExceeded"
)

const (
	// WorkloadFinishedReasonSucceeded indicates that the workload's job finished successfully.
	WorkloadFinishedReasonSucceeded = "Succeeded"

	// WorkloadFinishedReasonFailed indicates that the workload's job finished with an error.
	WorkloadFinishedReasonFailed = "Failed"

	// WorkloadFinishedReasonAdmissionChecksRejected indicates that the workload was rejected by admission checks.
	WorkloadFinishedReasonAdmissionChecksRejected = "AdmissionChecksRejected"

	// WorkloadFinishedReasonOutOfSync indicates that the prebuilt workload is not in sync with its parent job.
	WorkloadFinishedReasonOutOfSync = "OutOfSync"
)

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Queue",JSONPath=".spec.queueName",type="string",description="Name of the queue this workload was submitted to"
// +kubebuilder:printcolumn:name="Reserved in",JSONPath=".status.admission.clusterQueue",type="string",description="Name of the ClusterQueue where the workload is reserving quota"
// +kubebuilder:printcolumn:name="Admitted",JSONPath=".status.conditions[?(@.type=='Admitted')].status",type="string",description="Admission status"
// +kubebuilder:printcolumn:name="Finished",JSONPath=".status.conditions[?(@.type=='Finished')].status",type="string",description="Workload finished"
// +kubebuilder:printcolumn:name="Age",JSONPath=".metadata.creationTimestamp",type="date",description="Time this workload was created"
// +kubebuilder:resource:shortName={wl}

// Workload is the Schema for the workloads API
// +kubebuilder:validation:XValidation:rule="has(self.status) && has(self.status.conditions) && self.status.conditions.exists(c, c.type == 'QuotaReserved' && c.status == 'True') && has(self.status.admission) ? size(self.spec.podSets) == size(self.status.admission.podSetAssignments) : true", message="podSetAssignments must have the same number of podSets as the spec"
// +kubebuilder:validation:XValidation:rule="(has(oldSelf.status) && has(oldSelf.status.conditions) && oldSelf.status.conditions.exists(c, c.type == 'QuotaReserved' && c.status == 'True')) ? (oldSelf.spec.priorityClassSource == self.spec.priorityClassSource) : true", message="field is immutable"
// +kubebuilder:validation:XValidation:rule="(has(oldSelf.status) && has(oldSelf.status.conditions) && oldSelf.status.conditions.exists(c, c.type == 'QuotaReserved' && c.status == 'True') && has(oldSelf.spec.priorityClassName) && has(self.spec.priorityClassName)) ? (oldSelf.spec.priorityClassName == self.spec.priorityClassName) : true", message="field is immutable"
// +kubebuilder:validation:XValidation:rule="(has(oldSelf.status) && has(oldSelf.status.conditions) && oldSelf.status.conditions.exists(c, c.type == 'QuotaReserved' && c.status == 'True')) && (has(self.status) && has(self.status.conditions) && self.status.conditions.exists(c, c.type == 'QuotaReserved' && c.status == 'True')) && has(oldSelf.spec.queueName) && has(self.spec.queueName) ? oldSelf.spec.queueName == self.spec.queueName : true", message="field is immutable"
// +kubebuilder:validation:XValidation:rule="((has(oldSelf.status) && has(oldSelf.status.conditions) && oldSelf.status.conditions.exists(c, c.type == 'Admitted' && c.status == 'True')) && (has(self.status) && has(self.status.conditions) && self.status.conditions.exists(c, c.type == 'Admitted' && c.status == 'True')))?((has(oldSelf.spec.maximumExecutionTimeSeconds)?oldSelf.spec.maximumExecutionTimeSeconds:0) ==  (has(self.spec.maximumExecutionTimeSeconds)?self.spec.maximumExecutionTimeSeconds:0)):true", message="maximumExecutionTimeSeconds is immutable while admitted"
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadSpec   `json:"spec,omitempty"`
	Status WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadList contains a list of ResourceClaim
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workload{}, &WorkloadList{})
}
