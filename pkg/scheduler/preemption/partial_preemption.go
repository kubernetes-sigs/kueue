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
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/pkg/workload/patching"
)

// What a plan leaves to a victim is a count of Pods of its shrinkable PodSet, or
// one of these two. A lower value takes more from the victim.
const (
	// keepNone evicts a victim. It is less than any count: keeping 0 Pods of the
	// shrinkable PodSet leaves the Workload admitted with its other PodSets.
	keepNone int32 = -1
	// keepAll leaves a victim as it was when the search started. It is more than
	// any count.
	keepAll int32 = math.MaxInt32
)

// victim is what a partialPlan memorizes about one candidate.
type victim struct {
	// target is the candidate as the strategy yielded it first. Its WorkloadInfo
	// is the victim as it was when the search started.
	target *Target
	// eviction is the candidate as the search without partial preemption evicts
	// it, nil if that search does not evict the victim. The plan evicts no other
	// victim.
	eviction *Target
	// podSet is the PodSet that partial preemption may shrink, empty when the
	// victim can only be evicted whole.
	podSet kueue.PodSetReference
	// shrinkLimit is the lowest count that the shrink pass may leave to the
	// victim under the strategy that offered it. It equals v.shrunk() unless the
	// strategy capped the shrink higher (e.g. to satisfy LessThanOrEqualToFinalShare).
	shrinkLimit int32
	// keep is what the plan leaves to the victim right now.
	keep int32
	// current is the instance of the victim that is in the snapshot right now,
	// nil when keep is keepNone.
	current *workload.Info
}

// info is the victim as it was when the search started.
func (v *victim) info() *workload.Info {
	return v.target.WorkloadInfo
}

// canShrink tells whether partial preemption may shrink the victim.
func (v *victim) canShrink() bool {
	return v.podSet != ""
}

// floor is the lowest count that podSet may be shrunk to.
func (v *victim) floor() int32 {
	floor, _ := workload.PreemptionMinRetainedCount(v.info().Obj, v.podSet)
	return floor
}

// full is the count of podSet that the victim is charged for.
func (v *victim) full() int32 {
	return podSetRequestCount(v.info(), v.podSet)
}

// inProgress is what the victim keeps once the preemptions that are already in
// progress are done: nothing if it is being evicted, and the count that an
// earlier partial preemption asked for if it has not shrunk to it yet.
func (v *victim) inProgress() int32 {
	if workloadevict.IsEvicted(v.info().Obj) {
		return keepNone
	}
	if count, found := workload.PreemptionRetainedCount(v.info().Obj, v.podSet); found && count < v.full() {
		return count
	}
	return keepAll
}

// shrunk is the least that the victim keeps without a new eviction.
func (v *victim) shrunk() int32 {
	if v.canShrink() {
		return v.floor()
	}
	return v.inProgress()
}

// partialPlan refines the preemption target search of one strategy with partial
// preemption: victims shrink before any victim is evicted.
//
// A strategy offers its candidates one at a time: it removes a candidate from
// the snapshot, yields it, and looks at the snapshot again to pick the next one.
// The search without partial preemption walks the candidates leaving each one
// removed, until the workload fits (see getTargets). The plan adds a walk that
// puts each victim back with the least it keeps without a new eviction, so the
// strategy offers the candidates that follow as if the victim had shrunk.
//
// The plan memorizes the victims of both walks, and takes from them in two
// steps:
//
//  1. shrink pass: every victim keeps the least it can without a new eviction: its
//     floor if it can shrink, and otherwise what an eviction or a shrink that is
//     already in progress leaves it;
//  2. eviction pass: if that is not enough, the targets of the search without partial
//     preemption are evicted on top of the shrinks, in the order in which they
//     were offered, until the workload fits.
//
// Then it fills back what the workload does not need, starting with what
// disrupts the victims most (fillBack).
type partialPlan struct {
	strategy PreemptionStrategy
	// victims are the memorized candidates, in the order in which the strategy
	// offered them first.
	victims []*victim
	// byKey indexes the victims by Workload, for a walk to find a victim that
	// was offered before.
	byKey map[workload.Reference]*victim
	// nextShrinkLimit carries the fair-sharing shrink bound computed by the
	// strategy for the candidate it is about to yield to shrinkPass.
	nextShrinkLimit *int32
	// hasSecondFsStrategy is true when the strategy has a second Fair Sharing
	// rule (LessThanInitialShare), which allows shrinking a capped victim
	// further down to its floor if the first rule's shrink bounds are not enough.
	hasSecondFsStrategy bool
	// done tells that the plan has nothing to try: no victim can shrink.
	done bool
}

// newPartialPlan returns the plan for the strategy, nil if the PartialPreemption
// feature gate is disabled. A nil plan finds no targets.
func newPartialPlan(strategy PreemptionStrategy) *partialPlan {
	if !features.Enabled(features.PartialPreemption) {
		return nil
	}
	return &partialPlan{strategy: strategy, byKey: make(map[workload.Reference]*victim)}
}

// fairShrinkTarget evaluates fsStrategy for candWl during the shrink pass.
// For a non-shrinkable candidate it evaluates whole removal. For a shrinkable
// candidate it finds the lowest retained count in [floor, maxKeep] that passes
// fsStrategy, so a candidate is not rejected merely because removing it whole
// (or shrinking it all the way to its floor) would drop its ClusterQueue's
// share below the preemptor's. When a shrinkable candidate passes, the bound
// is recorded in nextShrinkLimit for shrinkPass to apply on the yield.
func (p *partialPlan) fairShrinkTarget(
	candWl *workload.Info,
	candCQ *fairsharing.TargetClusterQueue,
	fsStrategy fairsharing.Strategy,
	preemptorNewShare fairsharing.PreemptorNewShare,
	targetOldShare fairsharing.TargetOldShare,
) (fairsharing.TargetNewShare, bool) {
	p.nextShrinkLimit = nil
	podSet, ok := workload.PartialPreemptionPodSet(candWl.Obj, candWl.TotalRequests)
	if !ok {
		targetNewShare := candCQ.ComputeTargetShareAfterRemoval(candWl.Usage())
		return targetNewShare, fsStrategy(preemptorNewShare, targetOldShare, targetNewShare)
	}
	floor, _ := workload.PreemptionMinRetainedCount(candWl.Obj, podSet)
	full := podSetRequestCount(candWl, podSet)
	maxKeep := full - 1
	if count, found := workload.PreemptionRetainedCount(candWl.Obj, podSet); found && count < full {
		maxKeep = min(maxKeep, count)
	}
	shareAt := func(keep int32) fairsharing.TargetNewShare {
		return candCQ.ComputeTargetShareAfterRemoval(usageReleasedByShrink(candWl, podSet, keep))
	}
	targetNewShare := shareAt(floor)
	if fsStrategy(preemptorNewShare, targetOldShare, targetNewShare) {
		p.nextShrinkLimit = &floor
		return targetNewShare, true
	}
	if floor >= maxKeep {
		return targetNewShare, false
	}
	targetNewShare = shareAt(maxKeep)
	if !fsStrategy(preemptorNewShare, targetOldShare, targetNewShare) {
		return targetNewShare, false
	}
	// floor fails and maxKeep passes; bisect for the lowest retained count that passes.
	lo, hi := floor, maxKeep
	for lo+1 < hi {
		mid := lo + (hi-lo)/2
		if midShare := shareAt(mid); fsStrategy(preemptorNewShare, targetOldShare, midShare) {
			hi = mid
			targetNewShare = midShare
		} else {
			lo = mid
		}
	}
	p.nextShrinkLimit = &hi
	return targetNewShare, true
}

// shrinkPass walks the candidates and returns the targets that make the workload
// fit without a new eviction, the first step of the plan. It returns nil if
// there are none, and leaves the snapshot as it found it.
func (p *partialPlan) shrinkPass(ctx context.Context) []*Target {
	if p == nil {
		return nil
	}
	defer p.restore()
	p.strategy.pCtx.shrinkingPlan = p
	for candidate := range p.strategy.candidates {
		v := p.observe(candidate)
		keep := v.shrunk()
		if p.nextShrinkLimit != nil {
			keep = *p.nextShrinkLimit
			p.nextShrinkLimit = nil
		}
		v.shrinkLimit = keep
		p.setKeep(v, keep)
	}
	p.nextShrinkLimit = nil
	p.strategy.pCtx.shrinkingPlan = nil
	if !slices.ContainsFunc(p.victims, (*victim).canShrink) {
		// The search without partial preemption finds the same targets.
		p.done = true
		return nil
	}
	if !p.fits(ctx) {
		if !p.canLowerShrinkLimits() {
			return nil
		}
		for _, v := range p.victims {
			p.setKeep(v, v.shrunk())
		}
		if !p.fits(ctx) {
			return nil
		}
	}
	return p.buildTargets(ctx)
}

func (p *partialPlan) canLowerShrinkLimits() bool {
	if !p.hasSecondFsStrategy {
		return false
	}
	return slices.ContainsFunc(p.victims, func(v *victim) bool {
		return v.shrinkLimit > v.shrunk()
	})
}

// walkOrder returns a copy of the candidates that the search without partial
// preemption removed from the snapshot, in that order, for evictionPass: the fill-back
// of the search reorders them. It returns nil if the plan has nothing to try.
func (p *partialPlan) walkOrder(walked []*Target) []*Target {
	if p == nil || p.done {
		return nil
	}
	return slices.Clone(walked)
}

// evictionPass returns the targets that make the workload fit by evicting victims on
// top of the shrinks, the second step of the plan. walked are the candidates
// that the search without partial preemption removed from the snapshot, in
// that order. The ones that its fill-back did not put back are its targets: the
// plan evicts no other victim. If evictionPass returns targets, the snapshot is as the
// search found it. Otherwise it is as evictionPass found it.
func (p *partialPlan) evictionPass(ctx context.Context, walked []*Target) []*Target {
	if p == nil || p.done {
		return nil
	}
	evictions := p.collectPlainEvictions(walked)
	targets := p.tryEvictions(ctx, evictions)
	p.restore()
	if targets == nil {
		// The search without partial preemption goes on with its own targets.
		for _, v := range evictions {
			p.setKeep(v, keepNone)
		}
	}
	return targets
}

// evictionPassAfterUnfitWalk runs when the whole-eviction walk exhausted its
// candidates without making the workload fit, because some victims could only
// be offered by shrinking (or while an earlier victim stayed shrunk). It tries
// combining the shrinks from shrinkPass with the evictions the plain walk found,
// and, for strategies with verification (Fair Sharing), with non-shrinkable
// victims that shrinkPass already found while shrinkable victims stayed shrunk.
// If it returns targets, the snapshot is as the search found it. Otherwise it
// leaves the snapshot as it found it.
func (p *partialPlan) evictionPassAfterUnfitWalk(ctx context.Context, walked []*Target) []*Target {
	if p == nil || p.done {
		return nil
	}
	plainEvictions := p.collectPlainEvictions(walked)
	targets := p.tryEvictions(ctx, plainEvictions)
	if targets == nil && p.strategy.verify != nil && features.Enabled(features.FairSharingVerifyFinalTargets) {
		var fixedVictims []*victim
		for _, v := range p.victims {
			if !v.canShrink() {
				fixedVictims = append(fixedVictims, v)
			}
		}
		if len(fixedVictims) > 0 {
			targets = p.tryEvictions(ctx, fixedVictims)
		}
	}
	p.restore()
	if targets == nil {
		for _, v := range plainEvictions {
			p.setKeep(v, keepNone)
		}
	}
	return targets
}

func (p *partialPlan) collectPlainEvictions(walked []*Target) []*victim {
	var evictions []*victim
	for _, candidate := range walked {
		if v := p.observe(candidate); v.keep == keepNone {
			v.eviction = candidate
			evictions = append(evictions, v)
		}
	}
	return evictions
}

func (p *partialPlan) tryEvictions(ctx context.Context, evictions []*victim) []*Target {
	for _, v := range p.victims {
		p.setKeep(v, v.shrinkLimit)
	}
	fits := p.fits(ctx)
	for i := 0; !fits && i < len(evictions); i++ {
		p.setKeep(evictions[i], keepNone)
		fits = p.fits(ctx)
	}
	if !fits {
		return nil
	}
	return p.buildTargets(ctx)
}

// observe returns the victim for the candidate, and records what the search
// left of it in the snapshot: nothing if it removed the candidate, and the
// victim as it was when the search started otherwise.
func (p *partialPlan) observe(candidate *Target) *victim {
	key := workload.Key(candidate.WorkloadInfo.Obj)
	v, found := p.byKey[key]
	if !found {
		v = &victim{target: candidate, podSet: shrinkablePodSet(candidate)}
		v.shrinkLimit = v.shrunk()
		p.byKey[key] = v
		p.victims = append(p.victims, v)
	}
	v.keep, v.current = keepNone, nil
	cq := p.strategy.pCtx.snapshot.ClusterQueue(candidate.WorkloadInfo.ClusterQueue)
	if _, inSnapshot := cq.Workloads[key]; inSnapshot {
		v.keep, v.current = keepAll, v.info()
	}
	return v
}

// shrinkablePodSet returns the PodSet of the candidate that partial preemption
// may shrink, empty if it may not shrink the candidate.
func shrinkablePodSet(candidate *Target) kueue.PodSetReference {
	// Victims selected by a PreemptionConfig rule are evicted whole.
	if candidate.Reason == kueue.ConfigurablePreemptionReason {
		return ""
	}
	podSet, _ := workload.PartialPreemptionPodSet(candidate.WorkloadInfo.Obj, candidate.WorkloadInfo.TotalRequests)
	return podSet
}

// buildTargets fills back what the workload does not need, and returns the targets
// for what the plan still takes from the victims. It returns nil if there are
// none, or if the strategy does not accept them: it verifies them like the
// targets of the search without partial preemption, so Fair Sharing verifies
// them only with the FairSharingVerifyFinalTargets feature gate.
func (p *partialPlan) buildTargets(ctx context.Context) []*Target {
	p.fillBack(ctx)

	var targets []*Target
	for _, v := range p.victims {
		switch v.keep {
		case keepAll:
		case keepNone:
			// A target of the search without partial preemption, or a victim that
			// is already being evicted.
			targets = append(targets, cmp.Or(v.eviction, v.target))
		default:
			target := *v.target
			target.PartialPreemption = &policy.PartialPreemptionTarget{
				PodSet:        v.podSet,
				RetainedCount: v.keep,
				ReducedInfo:   v.current,
			}
			targets = append(targets, &target)
		}
	}
	if len(targets) == 0 || !p.strategy.accepts(ctx, targets) {
		return nil
	}
	return targets
}

// fillBack returns to the victims what the workload does not need, starting
// with what disrupts them most. Each pass goes over the memorized victims in
// the reverse order. A victim with a shrink in progress never gets more than
// that shrink leaves it, unless it is left out altogether.
func (p *partialPlan) fillBack(ctx context.Context) {
	// tryKeep leaves more to the victim if the workload still fits then.
	tryKeep := func(v *victim, keep int32) bool {
		if keep <= v.keep {
			return false
		}
		previous := v.keep
		p.setKeep(v, keep)
		if p.fits(ctx) {
			return true
		}
		p.setKeep(v, previous)
		return false
	}
	// A. A victim that the plan evicted and that can shrink keeps its shrink
	// limit if that still fits, or otherwise its floor.
	for _, v := range slices.Backward(p.victims) {
		if v.keep == keepNone && v.canShrink() {
			if !tryKeep(v, v.shrinkLimit) && v.shrinkLimit != v.floor() {
				tryKeep(v, v.floor())
			}
		}
	}
	// B. A victim that the plan evicted is not evicted.
	for _, v := range slices.Backward(p.victims) {
		if v.keep == keepNone {
			tryKeep(v, v.inProgress())
		}
	}
	// C. A victim that the plan shrank does not shrink, or else shrinks to the
	// largest count that still fits.
	for _, v := range slices.Backward(p.victims) {
		inProgress := v.inProgress()
		if v.keep == keepNone || v.keep >= inProgress || tryKeep(v, inProgress) {
			continue
		}
		// The workload fits with v.keep and not with tooMany: bisect.
		tooMany := min(v.full(), inProgress)
		for v.keep+1 < tooMany {
			if count := v.keep + (tooMany-v.keep)/2; !tryKeep(v, count) {
				tooMany = count
			}
		}
	}
	// D. A victim whose eviction or shrink was already in progress is left out.
	for _, v := range slices.Backward(p.victims) {
		if v.keep == v.inProgress() {
			tryKeep(v, keepAll)
		}
	}
}

// setKeep makes the snapshot hold keep of the victim.
func (p *partialPlan) setKeep(v *victim, keep int32) {
	if v.keep == keep {
		return
	}
	snapshot := p.strategy.pCtx.snapshot
	if v.current != nil {
		snapshot.RemoveWorkload(v.current)
	}
	switch keep {
	case keepNone:
		v.current = nil
	case keepAll:
		v.current = v.info()
	default:
		v.current = reducedInfoForPartialPreemption(v.info(), v.podSet, keep)
	}
	if v.current != nil {
		snapshot.AddWorkload(v.current)
	}
	v.keep = keep
}

// usageReleasedByShrink returns the quota usage released by shrinking info's
// named PodSet to retainedCount. Partial preemption does not shrink PodSets
// with a topology request, so no TAS usage is released.
func usageReleasedByShrink(info *workload.Info, podSetName kueue.PodSetReference, retainedCount int32) workload.Usage {
	reduced := reducedInfoForPartialPreemption(info, podSetName, retainedCount)
	return workload.Usage{Quota: workload.ResourceUsage{
		Assigned: info.ResourceUsage().Assigned.Sub(reduced.ResourceUsage().Assigned),
	}}
}

// reducedInfoForPartialPreemption returns a shallow copy of info with the named PodSet's usage scaled
// down to retainedCount. The original is left untouched so the snapshot can restore it unchanged.
func reducedInfoForPartialPreemption(info *workload.Info, podSetName kueue.PodSetReference, retainedCount int32) *workload.Info {
	reduced := *info
	reduced.TotalRequests = make([]workload.PodSetResources, len(info.TotalRequests))
	for idx := range info.TotalRequests {
		psr := info.TotalRequests[idx]
		if psr.Name == podSetName {
			if retainedCount >= 0 && retainedCount < psr.Count {
				psr = *psr.ScaledTo(retainedCount)
			}
		}
		reduced.TotalRequests[idx] = psr
	}
	return &reduced
}

// restore puts every memorized victim back into the snapshot.
func (p *partialPlan) restore() {
	for _, v := range p.victims {
		p.setKeep(v, keepAll)
	}
}

func (p *partialPlan) fits(ctx context.Context) bool {
	return workloadFits(ctx, p.strategy.pCtx, p.strategy.allowBorrowing)
}

func podSetRequestCount(info *workload.Info, podSet kueue.PodSetReference) int32 {
	for i := range info.TotalRequests {
		if info.TotalRequests[i].Name == podSet {
			return info.TotalRequests[i].Count
		}
	}
	return 0
}

// partiallyPreempt requests a scale-down; quota remains charged until the excess Pods terminate.
func (p *Preemptor) partiallyPreempt(ctx context.Context, preemptor *workload.Info, target *Target) error {
	pp := target.PartialPreemption
	wlCopy := target.WorkloadInfo.Obj.DeepCopy()
	message := fmt.Sprintf("Requested scale-down of PodSet %q to %d to accommodate workload %s",
		pp.PodSet, pp.RetainedCount, klog.KObj(preemptor.Obj))
	if err := patching.PatchAdmissionStatus(ctx, p.client, wlCopy, p.clock, func(wl *kueue.Workload) (bool, error) {
		setRetainedCount(wl, pp.PodSet, pp.RetainedCount)
		workload.SetPartiallyPreemptedCondition(wl, p.clock.Now(), true, target.Reason, message)
		return true, nil
	}, patching.WithLooseOnApply()); err != nil {
		return err
	}
	metrics.ReportPartialPreemption(preemptor.ClusterQueue, target.Reason, p.customLabels.CQGet(preemptor.ClusterQueue), p.roleTracker)
	ctrl.LoggerFrom(ctx).V(3).Info("Partial preemption: requested scale-down",
		"targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "podSet", pp.PodSet,
		"RetainedCount", pp.RetainedCount, "preemptingWorkload", klog.KObj(preemptor.Obj))
	p.recorder.Eventf(target.WorkloadInfo.Obj, nil, corev1.EventTypeNormal, "PartiallyPreempted", "PartiallyPreempted", "%s", message)
	return nil
}

// setRetainedCount records count as the named PodSet's requested partial-preemption target,
// reporting whether the value changed.
func setRetainedCount(wl *kueue.Workload, podSetName kueue.PodSetReference, count int32) bool {
	if wl.Status.Admission == nil {
		return false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName {
			if psa.PartialPreemption != nil && psa.PartialPreemption.RetainedCount != nil && *psa.PartialPreemption.RetainedCount <= count {
				return false
			}
			psa.PartialPreemption = &kueue.PodSetPartialPreemptionStatus{RetainedCount: new(count)}
			return true
		}
	}
	return false
}
