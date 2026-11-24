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
	"iter"
	"math"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
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

// victim is what a defaultPartialPlan memorizes about one candidate.
type victim struct {
	// target is the candidate as the strategy yielded it first. Its WorkloadInfo
	// is the victim as it was when the search started.
	target *Target
	// eviction is the candidate as the search without partial preemption evicts
	// it, nil if that search does not evict the victim. A defaultPartialPlan evicts no
	// other victim. A bucketedPartialPlan does not set it, and evicts target.
	eviction *Target
	// podSet is the PodSet that partial preemption may shrink, empty when the
	// victim can only be evicted whole.
	podSet kueue.PodSetReference
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

// defaultPartialPlan refines the preemption target search of one strategy
// with partial preemption: victims shrink before any victim is evicted.
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
type defaultPartialPlan struct {
	strategy PreemptionStrategy
	// victims are the memorized candidates, in the order in which the strategy
	// offered them first.
	victims []*victim
	// byKey indexes the victims by Workload, for a walk to find a victim that
	// was offered before.
	byKey map[workload.Reference]*victim
	// done tells that the plan has nothing to try: no victim can shrink.
	done bool
}

// shrinkPass walks the candidates and returns the targets that make the workload
// fit without a new eviction, the first step of the plan. It returns nil if
// there are none, and leaves the snapshot as it found it.
func (p *defaultPartialPlan) shrinkPass(ctx context.Context) []*Target {
	if p == nil {
		return nil
	}
	defer p.restore()
	for candidate := range p.strategy.candidates {
		v := p.observe(candidate)
		p.setKeep(v, v.shrunk())
	}
	if !slices.ContainsFunc(p.victims, (*victim).canShrink) {
		// The search without partial preemption finds the same targets.
		p.done = true
		return nil
	}
	if !p.fits(ctx) {
		return nil
	}
	return p.buildTargets(ctx)
}

// walkOrder returns a copy of the candidates that the search without partial
// preemption removed from the snapshot, in that order, for evictionPass: the
// fill-back of the search reorders them. It returns nil if the plan has nothing
// to try.
func (p *defaultPartialPlan) walkOrder(walked []*Target) []*Target {
	if p == nil || p.done {
		return nil
	}
	return slices.Clone(walked)
}

// evictionPass returns the targets that make the workload fit by evicting
// victims on top of the shrinks, the second step of the plan. walked are the
// candidates that the search without partial preemption removed from the
// snapshot, in that order. The ones that its fill-back did not put back are its
// targets: the plan evicts no other victim. If evictionPass returns targets, the
// snapshot is as the search found it. Otherwise it is as evictionPass found it.
func (p *defaultPartialPlan) evictionPass(ctx context.Context, walked []*Target) []*Target {
	if p == nil || p.done {
		return nil
	}
	var evictions []*victim
	for _, candidate := range walked {
		if v := p.observe(candidate); v.keep == keepNone {
			v.eviction = candidate
			evictions = append(evictions, v)
		}
	}
	for _, v := range p.victims {
		p.setKeep(v, v.shrunk())
	}
	// The workload fits at the latest when all of these are evicted, as in the
	// search without partial preemption.
	fits := p.fits(ctx)
	for i := 0; !fits && i < len(evictions); i++ {
		p.setKeep(evictions[i], keepNone)
		fits = p.fits(ctx)
	}
	var targets []*Target
	if fits {
		targets = p.buildTargets(ctx)
	}
	p.restore()
	if targets == nil {
		// The search without partial preemption goes on with its own targets.
		for _, v := range evictions {
			p.setKeep(v, keepNone)
		}
	}
	return targets
}

// partialPlan is what a strategy's target search uses for partial preemption.
// defaultPartialPlan implements it for a strategy without buckets, and
// bucketedPartialPlan for a strategy that gives its candidates buckets.
type partialPlan interface {
	shrinkPass(ctx context.Context) []*Target
	walkOrder(walked []*Target) []*Target
	evictionPass(ctx context.Context, walked []*Target) []*Target
}

// newPartialPlan returns the plan for the strategy: a bucketedPartialPlan if the
// strategy gives its candidates buckets, and a defaultPartialPlan otherwise.
// If the PartialPreemption feature gate is disabled, the plan is a nil
// *defaultPartialPlan, which finds no targets.
func newPartialPlan(strategy PreemptionStrategy) partialPlan {
	if !features.Enabled(features.PartialPreemption) {
		return (*defaultPartialPlan)(nil)
	}
	plan := &defaultPartialPlan{strategy: strategy, byKey: make(map[workload.Reference]*victim)}
	if strategy.sameBucket != nil {
		return &bucketedPartialPlan{defaultPartialPlan: plan}
	}
	return plan
}

// observe returns the victim for the candidate, and records what the search
// left of it in the snapshot: nothing if it removed the candidate, and the
// victim as it was when the search started otherwise.
func (p *defaultPartialPlan) observe(candidate *Target) *victim {
	key := workload.Key(candidate.WorkloadInfo.Obj)
	v, found := p.byKey[key]
	if !found {
		v = &victim{target: candidate, podSet: shrinkablePodSet(candidate)}
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

// buildTargets fills back what the workload does not need, and returns the targets
// for what the plan still takes from the victims. It returns nil if there are
// none, or if the strategy does not accept them: it verifies them like the
// targets of the search without partial preemption, so Fair Sharing verifies
// them only with the FairSharingVerifyFinalTargets feature gate.
func (p *defaultPartialPlan) buildTargets(ctx context.Context) []*Target {
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
func (p *defaultPartialPlan) fillBack(ctx context.Context) {
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
	// A. A victim that the plan evicted and that can shrink keeps its floor.
	for _, v := range slices.Backward(p.victims) {
		if v.keep == keepNone && v.canShrink() {
			tryKeep(v, v.floor())
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
		if v.keep == keepNone || v.keep >= inProgress {
			continue
		}
		if tryKeep(v, inProgress) {
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
func (p *defaultPartialPlan) setKeep(v *victim, keep int32) {
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

// restore puts every memorized victim back into the snapshot.
func (p *defaultPartialPlan) restore() {
	for _, v := range p.victims {
		p.setKeep(v, keepAll)
	}
}

func (p *defaultPartialPlan) fits(ctx context.Context) bool {
	return workloadFits(ctx, p.strategy.pCtx, p.strategy.allowBorrowing)
}

// bucketedPartialPlan is a partialPlan for a strategy that tells which candidates
// are in the same bucket (sameBucket), that is, which candidates are as appealing
// to preempt as each other.
//
// Instead of shrinking every victim and then evicting the targets of the search
// without partial preemption, it takes from the victims one bucket at a time, from
// the bucket that is most appealing to preempt to the one that is least. It takes
// nothing from a bucket while another bucket can still give, so that a victim of a
// later bucket never grows back at the expense of a victim of an earlier bucket: a
// victim that only shrinks can grow back, while an evicted victim cannot.
//
// Within a bucket, the plan shrinks the victims before it evicts any of them, as
// defaultPartialPlan does for all victims.
//
// The plan makes the whole decision in its shrink pass, and has nothing left to
// do after the walk of the search without partial preemption: it does not depend
// on that walk, and it may evict any victim that it memorized. A victim that the
// strategy offers only while another victim is shrunk is one of them. If the
// plan evicts that other victim in the end, the strategy would not have offered
// the victim, because its queue is back within its nominal quota, and yet the
// plan may take from it.
type bucketedPartialPlan struct {
	*defaultPartialPlan
}

// shrinkPass returns the targets that make the workload fit by taking from the
// victims bucket by bucket, and leaves the snapshot as it found it. It returns nil
// if there are none.
//
// The pass goes as follows:
//
//  1. it walks the candidates, putting each victim back with the least it keeps
//     without a new eviction, to memorize all of the victims that the strategy
//     offers while the victims shrink;
//  2. it puts every victim back, so that the steps that follow take from the
//     victims one at a time, instead of putting back what the workload does not
//     need;
//  3. it takes what the preemption in progress takes: nothing for a victim that
//     is being evicted, and the count that an earlier partial preemption asked
//     for for a victim whose shrink is pending. Taking from those victims costs
//     nothing, as their Pods are terminating anyway, so the pass selects them
//     whatever their bucket;
//  4. it takes from the buckets in order, two steps for each of them, and goes on
//     to the next bucket only if the workload does not fit yet:
//     a. the victims of the bucket give up Pods in rounds: each round takes at
//     most the max reclaimed count of a victim, from its annotation, or all
//     that it may give up without a new eviction, and the pass stops at the
//     first round where the workload fits. A victim without the annotation is
//     thus at its floor after one round;
//     b. the victims of the bucket are evicted.
func (p *bucketedPartialPlan) shrinkPass(ctx context.Context) []*Target {
	defer p.restore()
	for candidate := range p.strategy.candidates {
		v := p.observe(candidate)
		p.setKeep(v, v.shrunk())
	}
	if !slices.ContainsFunc(p.victims, (*victim).canShrink) {
		// The search without partial preemption finds the same targets.
		p.done = true
		return nil
	}
	p.restore()
	for _, v := range p.victims {
		p.setKeep(v, v.inProgress())
	}
	fits := p.fits(ctx)
	// take leaves keep to the victim if that is less than it keeps now, and stops
	// taking once the workload fits.
	take := func(v *victim, keep int32) {
		if !fits && keep < v.keep {
			p.setKeep(v, keep)
			fits = p.fits(ctx)
		}
	}
	for bucket := range p.buckets() {
		for round := int32(1); !fits; round++ {
			took := false
			for _, v := range bucket {
				keep := v.shrunk()
				if v.canShrink() {
					if step, found := workload.PreemptionMaxReclaimedCount(v.info().Obj); found {
						keep = min(v.inProgress(), max(v.floor(), int32(int64(v.full())-int64(round)*int64(step))))
					}
				}
				if keep < v.keep {
					p.setKeep(v, keep)
					took = true
				}
			}
			if !took {
				break
			}
			fits = p.fits(ctx)
		}
		for _, v := range bucket {
			take(v, keepNone)
		}
		if fits {
			return p.buildTargets(ctx)
		}
	}
	return nil
}

// walkOrder returns nil: the plan does not use the walk of the search without
// partial preemption.
func (p *bucketedPartialPlan) walkOrder([]*Target) []*Target {
	return nil
}

// evictionPass returns nil: shrinkPass made the whole decision.
func (p *bucketedPartialPlan) evictionPass(context.Context, []*Target) []*Target {
	return nil
}

// buckets yields the runs of victims that are in the same bucket, in the order in
// which the strategy offered them first, which is the order in which the plan
// takes from the buckets. The victims that are being evicted, or whose partial
// preemption is pending, are already at what the preemption in progress leaves
// them, so they need no place of their own in that order.
func (p *bucketedPartialPlan) buckets() iter.Seq[[]*victim] {
	return func(yield func([]*victim) bool) {
		start := 0
		for i := 1; i <= len(p.victims); i++ {
			if i < len(p.victims) && p.strategy.sameBucket(p.victims[i-1].target, p.victims[i].target) {
				continue
			}
			if !yield(p.victims[start:i]) {
				return
			}
			start = i
		}
	}
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
