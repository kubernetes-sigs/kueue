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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// PreemptionConfigNameAnnotation is the annotation key used on ClusterQueue to reference
	// a PreemptionConfig during Alpha.
	// This annotation will be removed in Beta when it becomes field on ClusterQueue.
	PreemptionConfigNameAnnotation = "kueue.x-k8s.io/preemption-config-name"
)

// NumericComparison defines how a specified numeric property (e.g., priority or custom numeric
// label value) of the candidate compares to the same property of the preemptor.
// Possible values are:
// - "LessThan": permits preemption if candidate field value < preemptor field value
// - "GreaterThan": permits preemption if candidate field value > preemptor field value
// - "LessThanOrEqual": permits preemption if candidate field value <= preemptor field value
// - "GreaterThanOrEqual": permits preemption if candidate field value >= preemptor field value
//
// +kubebuilder:validation:Enum=LessThan;GreaterThan;LessThanOrEqual;GreaterThanOrEqual
type NumericComparison string

const (
	// LessThan permits preemption if candidate field value < preemptor field value
	LessThan NumericComparison = "LessThan"
	// GreaterThan permits preemption if candidate field value > preemptor field value
	GreaterThan NumericComparison = "GreaterThan"
	// LessThanOrEqual permits preemption if candidate field value <= preemptor field value
	LessThanOrEqual NumericComparison = "LessThanOrEqual"
	// GreaterThanOrEqual permits preemption if candidate field value >= preemptor field value
	GreaterThanOrEqual NumericComparison = "GreaterThanOrEqual"
)

// PreemptionConfigNumericLabelConstraint describes the rule for filtering a custom numerical label.
// For example, this can be used to filter candidates based on the label describing the
// required topology domain size, such as the "number of TPUs".
// If a user has a label "number-of-tpus" that describes the number of TPUs required in a single cube,
// it can be used to create a rule that selects only workloads requiring smaller cube slices
// by defining comparison: "LessThan". Such a configuration would allow preemption of "smaller"
// workloads, to achieve better cluster utilization and decrease fragmentation.
// Please note that those labels are not copied out of the box from job-like objects.
// You should remember to append the designated labels to the list of labels
// copied to the workload via the Kueue main configuration if you wish to use a custom label.
// As Kubernetes label values cannot start with '-', integer labels are always non-negative.
// A negative fallbackValue can thus ensure workloads without the label compare smaller than any
// labeled workload if this is desired.
// If neither comparison, minValue, nor maxValue are specified, the constraint checks only that
// candidate workloads possess the designated label key with a valid integer.
//
// +kubebuilder:validation:XValidation:rule="!has(self.minValue) || !has(self.maxValue) || self.minValue <= self.maxValue",message="minValue must be less than or equal to maxValue"
type PreemptionConfigNumericLabelConstraint struct {
	// key is the label key that stores the integer value in the workload that will
	// be used for candidate selection.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MaxLength=316
	Key string `json:"key"`

	// fallbackValue is used when a workload does not have the label key
	// or the value under the key cannot be parsed as an integer.
	// If not specified, workloads without the label or
	// with a label value not parsable as int are treated as incomparable,
	// and therefore excluded from preemption candidates.
	//
	// +optional
	FallbackValue *int32 `json:"fallbackValue,omitempty"`

	// comparison defines how the candidate's label value compares to the preemptor's.
	//
	// +optional
	Comparison *NumericComparison `json:"comparison,omitempty"`

	// minValue specifies the lowest label value a candidate workload can have to be
	// considered for preemption.
	// If not specified, no lower bound is enforced.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	MinValue *int32 `json:"minValue,omitempty"`

	// maxValue specifies the highest label value a candidate workload can have to be
	// considered for preemption.
	// If not specified, no upper bound is enforced.
	//
	// +optional
	// +kubebuilder:validation:Minimum=0
	MaxValue *int32 `json:"maxValue,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName={preempcfg}

// PreemptionConfig is the Schema for the preemptionconfigs API
type PreemptionConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`
	Spec              PreemptionConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// PreemptionConfigList contains a list of PreemptionConfig
type PreemptionConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PreemptionConfig `json:"items"`
}

// PreemptionConfigSpec defines the desired state of PreemptionConfig
type PreemptionConfigSpec struct {
	// rules specifies preemption candidate selection rules.
	//
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Rules []PreemptionConfigPreemptionRule `json:"rules,omitempty"`
}

// PreemptionConfigActivationTrigger specifies when preemption rule should be treated as active.
// +kubebuilder:validation:Enum=Always;InsufficientQuota;QuotaFeasibleAndInsufficientTopology
type PreemptionConfigActivationTrigger string

const (
	// Always contributes matching candidates unconditionally.
	Always PreemptionConfigActivationTrigger = "Always"

	// InsufficientQuota contributes matching candidates only if preempting baseline candidates
	// does not yield sufficient quota to admit the preemptor workload.
	InsufficientQuota PreemptionConfigActivationTrigger = "InsufficientQuota"

	// QuotaFeasibleAndInsufficientTopology contributes matching candidates only if quota
	// is feasible for the entire preemptor under at least one eligible flavor assignment
	// (after preempting baseline candidates and any candidates from InsufficientQuota rules),
	// but the workload cannot be admitted because no eligible flavor assignment satisfies
	// its topology requirements.
	QuotaFeasibleAndInsufficientTopology PreemptionConfigActivationTrigger = "QuotaFeasibleAndInsufficientTopology"
)

// PreemptionConfigActivationPolicy defines when a preemption rule contributes candidates.
type PreemptionConfigActivationPolicy struct {
	// trigger specifies the prerequisite for contributing candidates.
	//
	// Possible values are:
	// - Always: contributes matching candidates unconditionally.
	// - InsufficientQuota: contributes matching candidates only if preempting baseline candidates
	//   does not yield sufficient quota to admit the preemptor workload.
	// - QuotaFeasibleAndInsufficientTopology: contributes matching candidates only if quota
	//   is feasible for the entire preemptor under at least one eligible flavor assignment
	//   (after preempting baseline candidates and any candidates from InsufficientQuota rules),
	//   but the workload cannot be admitted because no eligible flavor assignment satisfies
	//   its topology requirements.
	//
	// Baseline candidates are the deduplicated union of:
	// - candidates selected by the preemptor's ClusterQueue.spec.preemption policy;
	// - candidates selected by applicable rules in the referenced PreemptionConfig
	//   whose activationPolicy.trigger is Always.
	//
	// +kubebuilder:validation:Required
	Trigger PreemptionConfigActivationTrigger `json:"trigger"`
}

// PreemptionConfigPreemptionRule defines a single rule under which preemptions can be triggered
// and the candidate workloads eligible for preemption.
type PreemptionConfigPreemptionRule struct {
	// name is the identifier of the preemption rule.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name"`

	// preemptorSelector is a label selector indicating which workloads can trigger preemptions
	// using this rule. Accepts all workloads if not set.
	//
	// +optional
	PreemptorSelector *metav1.LabelSelector `json:"preemptorSelector,omitempty"`

	// activationPolicy determines when this rule contributes matching
	// candidates to preemption evaluation.
	//
	// +kubebuilder:validation:Required
	ActivationPolicy PreemptionConfigActivationPolicy `json:"activationPolicy"`

	// candidateSelectors specifies the selection rules for workloads that are candidates for preemption.
	// Candidates resulting from multiple selectors are summed into one set.
	// No selectors result in an empty candidate set, thereby disallowing any preemptions with this rule.
	//
	// +optional
	CandidateSelectors []PreemptionConfigPreemptionCandidateSelector `json:"candidateSelectors,omitempty"`
}

// PreemptionConfigPreemptionQueueScope specifies the relational boundary between
// the preempting workload's queue and candidate workloads' queues.
// Possible values are:
// - "WithinLocalQueue": restricts preemption candidates to workloads submitted to the exact same LocalQueue (matching name and namespace).
// - "WithinClusterQueue": restricts preemption candidates to workloads submitted to the same ClusterQueue as the preemptor.
// - "WithinParentCohort": restricts preemption candidates to workloads in ClusterQueues that share the exact same immediate direct Cohort, as well as workloads in the preemptor's own ClusterQueue (even if standalone).
// - "WithinCohortTree": restricts preemption candidates to workloads in ClusterQueues that belong to the same Cohort Tree (sharing the same root ancestor Cohort), as well as workloads in the preemptor's own ClusterQueue (even if standalone).
// - "AnyClusterQueue": places no relationship restrictions on preemption candidates.
//
// +kubebuilder:validation:Enum=WithinLocalQueue;WithinClusterQueue;WithinParentCohort;WithinCohortTree;AnyClusterQueue
type PreemptionConfigPreemptionQueueScope string

const (
	// WithinLocalQueue restricts preemption candidates to workloads submitted
	// to the exact same LocalQueue (matching name and namespace).
	WithinLocalQueue PreemptionConfigPreemptionQueueScope = "WithinLocalQueue"

	// WithinClusterQueue restricts preemption candidates to workloads submitted
	// to the same ClusterQueue as the preemptor.
	WithinClusterQueue PreemptionConfigPreemptionQueueScope = "WithinClusterQueue"

	// WithinParentCohort restricts preemption candidates to workloads in ClusterQueues
	// that share the exact same immediate direct Cohort, as well as workloads in the
	// preemptor's own ClusterQueue (even if standalone and lacking a parent cohort).
	WithinParentCohort PreemptionConfigPreemptionQueueScope = "WithinParentCohort"

	// WithinCohortTree restricts preemption candidates to workloads in ClusterQueues
	// that belong to the same Cohort Tree (sharing the same root ancestor Cohort),
	// as well as workloads in the preemptor's own ClusterQueue (even if standalone and lacking a parent cohort).
	WithinCohortTree PreemptionConfigPreemptionQueueScope = "WithinCohortTree"

	// AnyClusterQueue places no relationship restrictions on preemption candidates.
	AnyClusterQueue PreemptionConfigPreemptionQueueScope = "AnyClusterQueue"
)

// PreemptionConfigPreemptionCandidateSelector defines the selection criteria for workloads that are candidates for preemption.
type PreemptionConfigPreemptionCandidateSelector struct {
	// scope specifies the queue or cohort relation boundary of candidates to the preemptor workload.
	//
	// +kubebuilder:validation:Required
	Scope PreemptionConfigPreemptionQueueScope `json:"scope"`

	// clusterQueueSelector defines label selector constraints on candidate ClusterQueues.
	// Accepts all if not set.
	//
	// +optional
	ClusterQueueSelector *metav1.LabelSelector `json:"clusterQueueSelector,omitempty"`

	// labelSelector defines label selector constraints on candidate Workloads.
	// Accepts all if not set.
	//
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// numericLabels defines rules for filtering candidates using custom numeric labels on the Workload resource.
	// Multiple numeric labels are joined using AND-rule (all have to be satisfied).
	// Accepts all if not set.
	//
	// +optional
	// +listType=atomic
	NumericLabels []PreemptionConfigNumericLabelConstraint `json:"numericLabels,omitempty"`

	// priority defines the requirements for the priority of candidates.
	// Workloads not matching those requirements will not be considered as preemption candidates.
	// If nil, no priority requirements are enforced.
	//
	// +optional
	Priority *PreemptionConfigPriorityConstraint `json:"priority,omitempty"`
}

// PreemptionConfigPriorityConstraint defines the requirements for the priority of preemption candidates.
type PreemptionConfigPriorityConstraint struct {
	// mode specifies whether priority comparison uses base or boosted (effective) priority.
	//
	// +kubebuilder:validation:Required
	Mode PreemptionConfigPriorityMode `json:"mode"`

	// comparison defines how the candidate's priority compares to the preemptor's priority.
	// For example, "LessThan" means that only workloads with lower
	// priority will be allowed as preemption candidates.
	//
	// +kubebuilder:validation:Required
	Comparison NumericComparison `json:"comparison"`
}

// PreemptionConfigPriorityMode defines whether base or boosted (effective) priority is used when comparing candidates against the preemptor.
// Possible values are:
// - "Base": uses the raw priority value as assigned in the Workload resource (`spec.priority`) for both the candidate and preemptor, ignoring any priority boost.
// - "Boosted": uses the effective priority value, adjusted by the priority boost mechanism (if enabled), for both the candidate and preemptor.
//
// +kubebuilder:validation:Enum=Base;Boosted
type PreemptionConfigPriorityMode string

const (
	// Base uses the raw priority value as assigned in the Workload resource (`spec.priority`) for both the candidate and preemptor, ignoring any priority boost.
	Base PreemptionConfigPriorityMode = "Base"
	// Boosted uses the effective priority value, adjusted by the priority boost mechanism (if enabled), for both the candidate and preemptor.
	Boosted PreemptionConfigPriorityMode = "Boosted"
)
