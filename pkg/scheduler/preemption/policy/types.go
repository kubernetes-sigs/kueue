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

package policy

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/logging"
	stringsutils "sigs.k8s.io/kueue/pkg/util/strings"
	"sigs.k8s.io/kueue/pkg/workload"
)

// PartialPreemptionTarget holds the parameters of a partial preemption request:
// instead of evicting the victim entirely, Kueue asks it to scale down one PodSet
// to RetainedCount pods.
type PartialPreemptionTarget struct {
	PodSet        kueue.PodSetReference
	RetainedCount int32
	// ReducedInfo represents the victim after the requested scale-down.
	ReducedInfo *workload.Info
}

// Target represents a workload selected for preemption.
type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
	WorkloadCq   *scheduler.ClusterQueueSnapshot

	// ConfigurablePreemptionReasonData stores data which resulted in eviction.
	// Specified only when eviction is due to configurable preemption.
	ConfigurablePreemptionReasonData *ConfigurablePreemptionReasonData

	// PartialPreemption is set when the victim is asked to scale down a PodSet
	// rather than be evicted entirely. Nil for a whole-eviction target.
	PartialPreemption *PartialPreemptionTarget
}

// PreemptedUsage returns the usage that the target gives up. An evicted target
// gives up all of its usage. A partially preempted one gives up the quota of
// the Pods that it does not retain, and no TAS usage: partial preemption does
// not shrink a PodSet with a topology request.
func (t *Target) PreemptedUsage() workload.Usage {
	if t.PartialPreemption == nil {
		return t.WorkloadInfo.Usage()
	}
	before := t.WorkloadInfo.ResourceUsage().Assigned
	after := t.PartialPreemption.ReducedInfo.ResourceUsage().Assigned
	return workload.Usage{Quota: workload.ResourceUsage{
		Assigned: before.Sub(after),
	}}
}

type ConfigurablePreemptionReasonData struct {
	ConfigName PreemptionConfigReference
	// RuleNameToSelectorIndexes maps rule names to the indexes of selectors
	// that the workload satisfies.
	RuleNameToSelectorIndexes map[PreemptionConfigRuleReference][]int
	// ConsumedLimits are the PreemptionLimits consumed by preempting this target.
	ConsumedLimits ConsumedLimits
}

// PreemptionConfigReference is a dedicated type to reference PreemptionConfig
type PreemptionConfigReference string

// PreemptionConfigRuleReference is a dedicated type to reference PreemptionConfigPreemptionRule
type PreemptionConfigRuleReference string

// PreemptionLimitReference is a dedicated type to reference PreemptionLimit
type PreemptionLimitReference string

// PreemptionLimitAggregationKey identifies an aggregation unit within a PreemptionLimit
// (e.g. "Global", a ClusterQueue name, or "<namespace>/<workload-name>").
type PreemptionLimitAggregationKey string

// GlobalAggregationKey is the aggregation key for a PreemptionLimit with GlobalLimitAggregation.
const GlobalAggregationKey = PreemptionLimitAggregationKey(kueuealpha.GlobalLimitAggregation)

// PreemptionLimitAggregationCounts maps each aggregation key of a single PreemptionLimit
// to its number of preemptions.
type PreemptionLimitAggregationCounts = map[PreemptionLimitAggregationKey]int32

// ConsumedLimit is the aggregation key of a PreemptionLimit consumed by a
// preemption.
type ConsumedLimit struct {
	AggregationKey PreemptionLimitAggregationKey
	// uid and generation identify the version of the PreemptionLimit
	// AggregationKey was resolved against (see IsStaleFor).
	uid        types.UID
	generation int64
}

// NewConsumedLimit returns the aggregationKey of limit consumed by a preemption.
func NewConsumedLimit(limit *kueuealpha.PreemptionLimit, aggregationKey PreemptionLimitAggregationKey) ConsumedLimit {
	return ConsumedLimit{AggregationKey: aggregationKey, uid: limit.UID, generation: limit.Generation}
}

// IsStaleFor reports whether c was resolved against another PreemptionLimit
// than the one with uid, or against a generation older than minGeneration,
// so it no longer counts for the events tracked for that PreemptionLimit.
func (c ConsumedLimit) IsStaleFor(uid types.UID, minGeneration int64) bool {
	return c.uid != uid || c.generation < minGeneration
}

// ConsumedLimits maps each PreemptionLimit to the limit consumed by a preemption.
type ConsumedLimits map[PreemptionLimitReference]ConsumedLimit

// BlockingPreemptionLimit identifies a PreemptionLimit blocking preemption and
// when that limit is expected to elapse. RetryAt is nil when waiting for
// recorded events to expire cannot unblock it (e.g., spec.limit is 0, or already
// selected preemption candidates alone exhaust spec.limit).
type BlockingPreemptionLimit struct {
	Limit       PreemptionLimitReference
	Aggregation kueuealpha.PreemptionLimitAggregation
	RetryAt     *time.Time
}

// Message formats the human-readable reason why preemptions were blocked by the limit.
func (b BlockingPreemptionLimit) Message() string {
	if b.RetryAt == nil {
		return fmt.Sprintf("preemptions blocked by PreemptionLimit %s (aggregation %s)", b.Limit, b.Aggregation)
	}
	return fmt.Sprintf("preemptions blocked by PreemptionLimit %s (aggregation %s) until %s", b.Limit, b.Aggregation, b.RetryAt.UTC().Format(time.RFC3339))
}

// Earlier returns whichever of b and other is expected to elapse sooner (keeping
// b on ties), or nil if both are nil. A non-nil RetryAt elapses sooner than a
// nil RetryAt.
func (b *BlockingPreemptionLimit) Earlier(other *BlockingPreemptionLimit) *BlockingPreemptionLimit {
	switch {
	case b == nil:
		return other
	case other == nil:
		return b
	case other.RetryAt == nil:
		return b
	case b.RetryAt == nil:
		return other
	case other.RetryAt.Before(*b.RetryAt):
		return other
	default:
		return b
	}
}

func (d *ConfigurablePreemptionReasonData) EvictionMessage(preemptor *kueue.Workload) string {
	return fmt.Sprintf("Preempted by %s because of preemption config %s rule %s",
		workload.Key(preemptor),
		d.ConfigName,
		stringsutils.JoinMap(d.RuleNameToSelectorIndexes, "/", ",", "; "))
}

type yieldCandidate = func(*Target) bool

// YieldFromSnapshot wraps a candidate (Target) yielder with
// logic removing the candidate from the provided snapshot.
func YieldFromSnapshot(snapshot *scheduler.Snapshot, yield yieldCandidate) yieldCandidate {
	return func(t *Target) bool {
		snapshot.RemoveWorkload(t.WorkloadInfo)
		return yield(t)
	}
}

// ensures that Target implements ObjectRefProvider interface at compile time
var _ logging.ObjectRefProvider = (*Target)(nil)

// GetObject implements the ObjectRefProvider interface.
func (t *Target) GetObject() client.Object {
	return t.WorkloadInfo.Obj
}

// PreemptionPossibility represents the result
// of a preemption simulation.
type PreemptionPossibility int

const (
	// NoCandidates were found.
	NoCandidates PreemptionPossibility = iota
	// Preemption targets were found.
	Preempt
	// Preemption targets were found, and
	// all of them are outside of preempting
	// ClusterQueue.
	Reclaim
)

func (p PreemptionPossibility) String() string {
	switch p {
	case NoCandidates:
		return "NoCandidates"
	case Preempt:
		return "Preempt"
	case Reclaim:
		return "Reclaim"
	}
	return "Unknown"
}
