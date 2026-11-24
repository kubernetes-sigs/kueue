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

package preemption

import (
	"context"
	"iter"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/workload"
)

type candidateIterator interface {
	Reset()
	Next(borrow bool) (candidate *workload.Info, evictReason string)
}

// classicalPreemptionStrategy implements a heuristic to find a minimal set of Workloads
// to preempt.
// The heuristic first removes candidates, in the input order, while their
// ClusterQueues are still borrowing resources and while the incoming Workload
// doesn't fit in the quota.
func classicalPreemptionStrategy(ctx context.Context, preemptor *Preemptor, preemptionCtx *preemptionCtx) iter.Seq[PreemptionStrategy] {
	log := log.FromContext(ctx)
	hierarchicalReclaimCtx := &classical.HierarchicalPreemptionCtx{
		Log:               log,
		Wl:                preemptionCtx.preemptor.Obj,
		Cq:                preemptionCtx.preemptorCQ,
		FrsNeedPreemption: preemptionCtx.frsNeedPreemption,
		Requests:          preemptionCtx.workloadUsage.Quota.Assigned,
		WorkloadOrdering:  preemptor.workloadOrdering,
	}
	var strategyOpts []strategyOption
	if features.Enabled(features.PartialPreemption) && features.Enabled(features.PartialPreemptionPriorityOrder) {
		hierarchyCQs := classical.HierarchyClusterQueues(hierarchicalReclaimCtx)
		strategyOpts = append(strategyOpts, withBuckets(
			sameBucketFunc(log, preemptor.enabledAfs, preemptionCtx.preemptorCQ.Name, hierarchyCQs)))
	}
	candidatesGenerator := classical.NewCandidateIterator(
		hierarchicalReclaimCtx,
		preemptor.enabledAfs,
		preemptionCtx.frsNeedPreemption,
		preemptionCtx.snapshot,
		preemptor.clock,
		policy.CandidatesOrdering,
	)
	var attemptPossibleOpts []preemptionAttemptOpts
	borrowWithinCohortForbidden, _ := classical.IsBorrowingWithinCohortForbidden(preemptionCtx.preemptorCQ)
	// We have three types of candidates:
	// 1. Hierarchy candidates. Candidates over which the incoming workload has a
	//    hierarchical advantage (it is closer to the quota used by the candidate).
	//    We can preempt such candidates regardless of their priority.
	// 2. Priority candidates. Candidates over which there is no hiearchical advantage
	//    but the possibility to preempt is determined based on priorities.
	//    We respect the BorrowWithinCohort configuration only for these candidates.
	// 3. Same queue candidates.
	// We can only preempt a priority candidate with priority > MaxPriorityThreshold
	// if the target CQ is not borrowing (by the definition of the MaxPriorityThreshold).
	// We sometimes need to consider both options allowBorrowing = true and false
	// (because with false we have more candidates but cannot use borrowing).
	// The order in which the options are considered is arbitrary and the condition
	// in which we try allowBorrowing=false before true is to keep compatibility with
	// previous versions.
	switch {
	case candidatesGenerator.NoCandidateFromOtherQueues() || (borrowWithinCohortForbidden && !queueUnderNominalInResourcesNeedingPreemption(preemptionCtx)):
		attemptPossibleOpts = []preemptionAttemptOpts{{true}}
	case borrowWithinCohortForbidden && candidatesGenerator.NoCandidateForHierarchicalReclaim():
		attemptPossibleOpts = []preemptionAttemptOpts{{false}, {true}}
	default:
		attemptPossibleOpts = []preemptionAttemptOpts{{true}, {false}}
	}

	return func(yieldStrategy func(PreemptionStrategy) bool) {
		for _, opts := range attemptPossibleOpts {
			allowBorrowing := opts.borrowing
			candidateIter := func(yieldCandidate func(*Target) bool) {
				cont := iterateOverCandidates(ctx, preemptionCtx, candidatesGenerator, allowBorrowing, yieldCandidate)
				if cont && features.Enabled(features.ConfigurablePreemptions) {
					preemptionCtx.configurableEvaluator.FindCandidates(
						preemptionCtx.snapshot,
						preemptionCtx.frsNeedPreemption,
						func() bool { return workloadQuotaFits(preemptionCtx, allowBorrowing) },
						yieldCandidate,
					)
				}
			}
			if !yieldStrategy(newPreemptionStrategy(candidateIter, allowBorrowing, preemptionCtx, strategyOpts...)) {
				return
			}
		}
	}
}

const (
	// Candidates of a ClusterQueue that the preemptor has hierarchical advantage over.
	classHierarchy = iota
	// Candidates that the preemptor may preempt because of their priority.
	classPriority
	// Candidates of the preemptor's ClusterQueue.
	classSameQueue
	// Candidates that a PreemptionConfig rule selected.
	classConfigurable
)

// sameBucketFunc returns the function that tells whether two candidates, which
// the classical strategy offers one after the other, are in the same bucket: they
// are of the same kind, and CandidatesBucketOrdering only tells them apart by the time
// they were admitted. The strategy offers the buckets in order, so the candidates
// of a bucket are next to each other.
func sameBucketFunc(
	log logr.Logger,
	enabledAfs bool,
	preemptorCQ kueue.ClusterQueueReference,
	hierarchyCQs sets.Set[kueue.ClusterQueueReference],
) func(a, b *Target) bool {
	class := func(t *Target) int {
		switch {
		case t.Reason == kueue.ConfigurablePreemptionReason:
			return classConfigurable
		case t.WorkloadInfo.ClusterQueue == preemptorCQ:
			return classSameQueue
		case hierarchyCQs.Has(t.WorkloadInfo.ClusterQueue):
			return classHierarchy
		default:
			return classPriority
		}
	}
	return func(a, b *Target) bool {
		return class(a) == class(b) &&
			policy.CandidatesBucketOrdering(log, enabledAfs, a.WorkloadInfo, b.WorkloadInfo, preemptorCQ) == 0
	}
}

func iterateOverCandidates(
	ctx context.Context,
	preemptionCtx *preemptionCtx,
	iterator candidateIterator,
	allowBorrowing bool,
	yield func(*Target) bool,
) (cont bool) {
	yield = policy.YieldFromSnapshot(preemptionCtx.snapshot, yield)
	iterator.Reset()
	for candidateWl, reason := iterator.Next(allowBorrowing); candidateWl != nil; candidateWl, reason = iterator.Next(allowBorrowing) {
		candidate := &Target{
			WorkloadInfo: candidateWl,
			Reason:       reason,
			WorkloadCq:   preemptionCtx.snapshot.ClusterQueue(candidateWl.ClusterQueue),
		}
		if !yield(candidate) {
			return
		}
	}
	return true
}
