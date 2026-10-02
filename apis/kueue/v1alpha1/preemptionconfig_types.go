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
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=316
	Key string `json:"key,omitempty"`

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
	metav1.TypeMeta `json:",inline"`

	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the preemption rules of the PreemptionConfig.
	// +optional
	Spec PreemptionConfigSpec `json:"spec"`
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
	// +required
	Trigger PreemptionConfigActivationTrigger `json:"trigger,omitempty"`
}

// PreemptionConfigPreemptionRule defines a single rule under which preemptions can be triggered
// and the candidate workloads eligible for preemption.
type PreemptionConfigPreemptionRule struct {
	// name is the identifier of the preemption rule.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name"`

	// preemptorSelector is a label selector indicating which workloads can trigger preemptions
	// using this rule. Accepts all workloads if not set.
	//
	// +optional
	PreemptorSelector *metav1.LabelSelector `json:"preemptorSelector,omitempty"`

	// preemptorPriorityClassSelector filters which preempting workloads can activate this rule
	// based on their spec.priorityClassRef.name.
	// If omitted or empty, workloads of any priority class can trigger this rule.
	//
	// +optional
	PreemptorPriorityClassSelector *PreemptionConfigPriorityClassSelector `json:"preemptorPriorityClassSelector,omitempty"`

	// activationPolicy determines when this rule contributes matching
	// candidates to preemption evaluation.
	//
	// +required
	ActivationPolicy PreemptionConfigActivationPolicy `json:"activationPolicy,omitzero"`

	// candidateSelectors specifies the selection rules for workloads that are candidates for preemption.
	// Candidates resulting from multiple selectors are summed into one set.
	// No selectors result in an empty candidate set, thereby disallowing any preemptions with this rule.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=32
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
	// +required
	Scope PreemptionConfigPreemptionQueueScope `json:"scope,omitempty"`

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
	// +kubebuilder:validation:MaxItems=32
	NumericLabels []PreemptionConfigNumericLabelConstraint `json:"numericLabels,omitempty"`

	// priority defines the requirements for the priority of candidates.
	// Workloads not matching those requirements will not be considered as preemption candidates.
	// If nil, no priority requirements are enforced.
	//
	// +optional
	Priority *PreemptionConfigPriorityConstraint `json:"priority,omitempty"`
}

// PreemptionConfigPriorityConstraint defines how candidate priority is evaluated.
// +kubebuilder:validation:XValidation:rule="has(self.mode) == has(self.comparison)",message="mode and comparison must be specified together"
type PreemptionConfigPriorityConstraint struct {
	// mode specifies which priority value to compare.
	// Must be specified together with comparison.
	//
	// +optional
	Mode *PreemptionConfigPriorityMode `json:"mode,omitempty"`

	// comparison is the relational operator comparing the candidate's priority
	// against the preemptor's priority (i.e., <candidate> <comparison> <preemptor>).
	// For example, LessThan means the candidate must have strictly lower priority than the preemptor.
	// Must be specified together with mode.
	//
	// +optional
	Comparison *NumericComparison `json:"comparison,omitempty"`

	// PreemptionConfigPriorityClassSelector filters candidate workloads by priority class name.
	PreemptionConfigPriorityClassSelector `json:",inline"`
}

// PreemptionConfigPriorityClassSelector filters workloads by their priority class name
// (matched against the Workload's spec.priorityClassRef.name, which is populated by Kueue
// for both WorkloadPriorityClass and Pod PriorityClass).
type PreemptionConfigPriorityClassSelector struct {
	// matchNames is an allowlist of PriorityClass or WorkloadPriorityClass names.
	// A workload matches if its spec.priorityClassRef.name equals any name in this list (OR semantics).
	// Workloads without a priorityClassRef do not match when matchNames is non-empty.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	MatchNames []string `json:"matchNames,omitempty"`

	// notMatchNames is a denylist of PriorityClass or WorkloadPriorityClass names.
	// A workload matches only if its spec.priorityClassRef.name does not equal any name in this list.
	// Workloads without a priorityClassRef match any notMatchNames constraint.
	// If both matchNames and notMatchNames are specified, both conditions must be satisfied (AND semantics).
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=32
	// +kubebuilder:validation:items:MinLength=1
	// +kubebuilder:validation:items:MaxLength=253
	NotMatchNames []string `json:"notMatchNames,omitempty"`
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
