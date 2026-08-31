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

package queue

import (
	"cmp"
	"context"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	"sigs.k8s.io/kueue/pkg/util/heap"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

type RequeueReason string

const (
	RequeueReasonFailedAfterNomination  RequeueReason = "FailedAfterNomination"
	RequeueReasonNamespaceMismatch      RequeueReason = "NamespaceMismatch"
	RequeueReasonGeneric                RequeueReason = ""
	RequeueReasonPreemptionGated        RequeueReason = "PreemptionGated"
	RequeueReasonPendingPreemption      RequeueReason = "PendingPreemption"
	RequeueReasonPendingMigration       RequeueReason = "PendingMigration"
	RequeueReasonPreemptionFailed       RequeueReason = "PreemptionFailed"
	RequeueReasonNoFit                  RequeueReason = "NoFit"
	RequeueReasonPreemptionNoCandidates RequeueReason = "PreemptionNoCandidates"
	RequeueReasonSnapshotFailed         RequeueReason = "SnapshotFailed"
)

// QuotaReservedReason represents the reason for the WorkloadQuotaReserved condition
// computed by the scheduler or queue manager during evaluation.
type QuotaReservedReason string

const (
	QuotaReservedReasonGeneric                      QuotaReservedReason = ""
	QuotaReservedReasonPendingEvaluation            QuotaReservedReason = kueue.WorkloadQuotaReservedReasonPendingEvaluation
	QuotaReservedReasonWaitingForQuota              QuotaReservedReason = kueue.WorkloadQuotaReservedReasonWaitingForQuota
	QuotaReservedReasonWaitingForPreemptedWorkloads QuotaReservedReason = kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads
	QuotaReservedReasonSuspended                    QuotaReservedReason = kueue.WorkloadQuotaReservedReasonSuspended
	QuotaReservedReasonMisconfigured                QuotaReservedReason = kueue.WorkloadQuotaReservedReasonMisconfigured
	QuotaReservedReasonAdmissionGated               QuotaReservedReason = kueue.WorkloadAdmissionGated
)

var (
	realClock = clock.RealClock{}
)

// preemptorWorkload is the workload at the ClusterQueue head which is
// currently preempting workloads. For BestEffortFIFO policy, isSticky is
// set to true and prevents skipped over ineligible workloads from going back
// to the head of the queue. A workload is considered a preemptor until it is
// admitted, unschedulable, or deleted.
// See Kueue#6929 and Kueue#7101 for motivation.
//
// The state field is accessed concurrently and drives both the CQ heap
// ordering and the Snapshot ordering used by the visibility server. Two
// mechanisms keep every sort transitive (see Kueue#12740):
//   - Writes (set/clear) happen under the ClusterQueue's rwm lock, so heap
//     operations, which also hold the lock, never observe it changing mid-sort.
//   - Snapshot sorts a copy of the pending workloads without holding the lock,
//     so it captures the sticky workload once per sort via capturedStickyMatcher()
//     instead of re-reading it on every comparison.
//
// The field holds a single whole value (nil means no preemptor workload), so an
// atomic.Pointer keeps individual reads and writes memory-safe on top of the
// ordering guarantees above.
type preemptorWorkloadState struct {
	workloadName workload.Reference
	generation   int64
	isSticky     bool
}

type preemptorWorkload struct {
	state atomic.Pointer[preemptorWorkloadState]
}

func (p *preemptorWorkload) matches(workload workload.Reference, strict bool, generation int64) bool {
	state := p.state.Load()
	matchesKey := state != nil && state.workloadName == workload
	if !strict {
		return matchesKey
	}
	return matchesKey && state.generation == generation
}

func (p *preemptorWorkload) stickyMatches(workload workload.Reference) bool {
	state := p.state.Load()
	return state != nil && state.isSticky && state.workloadName == workload
}

// capturedStickyMatcher captures the current sticky workload once and returns a
// predicate bound to that fixed value. A sort that compares through the returned
// predicate stays transitive even if set/clear runs concurrently, because every
// comparison in that sort observes the same sticky workload. See Kueue#12740.
func (p *preemptorWorkload) capturedStickyMatcher() func(workload.Reference) bool {
	state := p.state.Load()
	if state == nil || !state.isSticky {
		return func(workload.Reference) bool { return false }
	}
	captured := state.workloadName
	return func(key workload.Reference) bool {
		return captured == key
	}
}

func (p *preemptorWorkload) clear() {
	p.state.Store(nil)
}

func (p *preemptorWorkload) set(workload workload.Reference, isSticky bool, generation int64) {
	p.state.Store(&preemptorWorkloadState{
		workloadName: workload,
		generation:   generation,
		isSticky:     isSticky,
	})
}

func logStickyWorkloadSelectionIfVerbose(log logr.Logger, wl *kueue.Workload) {
	if logV := log.V(5); logV.Enabled() {
		logV.Info("Prioritizing sticky workload", "workload", workload.Key(wl))
	}
}

type ClusterQueue struct {
	hierarchy.ClusterQueue[*cohort]
	name              kueue.ClusterQueueReference
	namespaceSelector labels.Selector
	active            bool
	workloads         *PendingWorkloads

	// hashToBulkMoveReason tracks scheduling equivalence classes and the reason
	// why workloads with that hash were bulk-moved to inadmissibleWorkloads.
	// Cleared when queueInadmissibleWorkloads runs.
	hashToBulkMoveReason map[workload.EquivalenceHash]QuotaReservedReason

	finishedWorkloads sets.Set[workload.Reference]

	// popCycle identifies the current evaluation epoch: Pop advances it,
	// PopMidCycle does not. popCycle and queueInadmissibleCycle are used to
	// track when there is a requeuing of inadmissible workloads while a
	// workload is being scheduled.
	popCycle int64

	// queueInadmissibleCycle stores the popId at the time when
	// QueueInadmissibleWorkloads is called.
	queueInadmissibleCycle int64

	compareFunc  func(a, b *workload.Info) int
	snapshotSort func(elements []*workload.Info)

	queueingStrategy kueue.QueueingStrategy

	rwm sync.RWMutex

	clock clock.Clock

	AdmissionScope *kueue.AdmissionScope

	afsUsageLedger *queueafs.AfsUsageLedger

	// lqWeights holds the LocalQueues that belong to this ClusterQueue, mapped to
	// their fair-sharing weight. Presence denotes membership; the heap comparator
	// reads the weight without a fallible API call, and missing entries fall back
	// to weight 1.0. Guarded by rwm. See Kueue#13476.
	lqWeights map[utilqueue.LocalQueueReference]float64

	pw *preemptorWorkload

	ConcurrentAdmissionPolicy *kueue.ConcurrentAdmissionPolicy
}

func (c *ClusterQueue) GetName() kueue.ClusterQueueReference {
	return c.name
}

func (c *ClusterQueue) IsPreemptor(wInfo *workload.Info) bool {
	return c.pw.matches(workloadKey(wInfo), true, wInfo.Obj.Generation)
}

func workloadKey(i *workload.Info) workload.Reference {
	return workload.Key(i.Obj)
}

type clusterQueueOption func(*clusterQueueOptions)

type clusterQueueOptions struct {
	fsResWeights      map[corev1.ResourceName]float64
	enableAdmissionFs bool
	afsUsageLedger    *queueafs.AfsUsageLedger
}

func withFSResWeights(weights map[corev1.ResourceName]float64) clusterQueueOption {
	return func(o *clusterQueueOptions) {
		o.fsResWeights = weights
	}
}

func withEnableAdmissionFs(enable bool) clusterQueueOption {
	return func(o *clusterQueueOptions) {
		o.enableAdmissionFs = enable
	}
}

func withAfsUsageLedger(ledger *queueafs.AfsUsageLedger) clusterQueueOption {
	return func(o *clusterQueueOptions) {
		o.afsUsageLedger = ledger
	}
}

func newClusterQueue(
	ctx context.Context,
	client client.Client,
	cq *kueue.ClusterQueue,
	cl *metrics.CustomLabels,
	wo workload.Ordering,
	afsConfig *configapi.AdmissionFairSharing,
	afsUsageLedger *queueafs.AfsUsageLedger,
) (*ClusterQueue, error) {
	enableAdmissionFs, fsResWeights := afs.ResourceWeights(cq.Spec.AdmissionScope, afsConfig)
	cqImpl := newClusterQueueImpl(
		ctx,
		client,
		cl,
		wo,
		realClock,
		withFSResWeights(fsResWeights),
		withEnableAdmissionFs(enableAdmissionFs),
		withAfsUsageLedger(afsUsageLedger),
	)
	err := cqImpl.Update(cq)
	if err != nil {
		return nil, err
	}
	return cqImpl, nil
}

func newClusterQueueImpl(ctx context.Context, client client.Client, cl *metrics.CustomLabels, wo workload.Ordering, clock clock.Clock, opts ...clusterQueueOption) *ClusterQueue {
	options := &clusterQueueOptions{}
	for _, opt := range opts {
		opt(options)
	}
	pw := preemptorWorkload{}
	// lqWeights is shared by reference with the ClusterQueue struct below so
	// weight updates are visible to the comparator. All access holds rwm.
	lqWeights := make(map[utilqueue.LocalQueueReference]float64)
	getLQWeight := func(lqKey utilqueue.LocalQueueReference) float64 {
		if w, ok := lqWeights[lqKey]; ok {
			return w
		}
		return 1.0
	}
	// The comparator reads the sticky workload and cached weights live; safe
	// because those writes and heap operations all hold rwm.
	compareFunc := queueOrderingFunc(ctx, getLQWeight, wo, options.fsResWeights, options.enableAdmissionFs, options.afsUsageLedger, pw.stickyMatches)
	// Derive lessFunc from compareFunc for the heap.
	lessFunc := func(a, b *workload.Info) bool { return compareFunc(a, b) < 0 }
	// Snapshot sorts without the lock, so it captures the sticky workload once
	// per sort rather than reading it live. See Kueue#12740.
	snapshotSort := buildSnapshotSort(
		ctx, wo, &pw, client,
		options.enableAdmissionFs, options.fsResWeights,
		options.afsUsageLedger,
	)
	return &ClusterQueue{
		workloads: &PendingWorkloads{
			customLabels:          cl,
			active:                *heap.New(workloadKey, lessFunc),
			activeTracker:         metrics.NewLabelValsTracker(),
			inadmissible:          make(inadmissibleWorkloads),
			inadmissibleTracker:   metrics.NewLabelValsTracker(),
			pendingResourcesTotal: make(map[corev1.ResourceName]int64),
			schedulingHashes:      newSchedulingHashCounts(),
			inflight:              make(map[workload.Reference]*workload.Info),
		},
		hashToBulkMoveReason:   make(map[workload.EquivalenceHash]QuotaReservedReason),
		finishedWorkloads:      sets.New[workload.Reference](),
		queueInadmissibleCycle: -1,
		compareFunc:            compareFunc,
		snapshotSort:           snapshotSort,
		rwm:                    sync.RWMutex{},
		clock:                  clock,
		afsUsageLedger:         options.afsUsageLedger,
		lqWeights:              lqWeights,
		pw:                     &pw,
	}
}

// Update updates the properties of this ClusterQueue.
func (c *ClusterQueue) Update(apiCQ *kueue.ClusterQueue) error {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.name = kueue.ClusterQueueReference(apiCQ.Name)
	c.queueingStrategy = apiCQ.Spec.QueueingStrategy
	nsSelector, err := metav1.LabelSelectorAsSelector(apiCQ.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.namespaceSelector = nsSelector
	c.active = apimeta.IsStatusConditionTrue(apiCQ.Status.Conditions, kueue.ClusterQueueActive)
	if features.Enabled(features.ConcurrentAdmission) {
		c.ConcurrentAdmissionPolicy = apiCQ.Spec.ConcurrentAdmissionPolicy
	}
	c.workloads.UpdateConfiguredResources(apiCQ)
	return nil
}

// AddFromLocalQueue pushes all workloads belonging to this queue to
// the ClusterQueue. If at least one workload is added, returns true,
// otherwise returns false.
func (c *ClusterQueue) AddFromLocalQueue(q *LocalQueue, roleTracker *roletracker.RoleTracker, cl *metrics.CustomLabels) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	added := false
	for _, info := range q.items {
		if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsParent(info.Obj) {
			// Parent Workloads are not pushed onto heap
			continue
		}
		// A workload already tracked as inadmissible stays there; retrying it
		// is owned by the requeue paths, which respect the backoff time.
		if c.workloads.PushActiveIfNotPresent(info) {
			added = true
		}
	}
	for finishedWorkload := range q.finishedWorkloads {
		c.finishedWorkloads.Insert(finishedWorkload)
	}
	reportCQFinishedWorkloads(c, roleTracker, cl)
	return added
}

func (c *ClusterQueue) ConcurrentAdmissionEnabled() bool {
	if !features.Enabled(features.ConcurrentAdmission) {
		return false
	}
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.ConcurrentAdmissionPolicy != nil
}

// PushOrUpdate pushes the workload to ClusterQueue.
// If the workload is already present, updates with the new one.
func (c *ClusterQueue) PushOrUpdate(wInfo *workload.Info) {
	if features.Enabled(features.ConcurrentAdmission) && concurrentadmission.IsParent(wInfo.Obj) {
		// Parent Workloads are not pushed onto heap
		return
	}
	c.rwm.Lock()
	defer c.rwm.Unlock()
	key := workload.Key(wInfo.Obj)
	// Skip if the scheduler is actively processing this workload.
	// RequeueWorkload will handle placement with the latest version.
	if c.workloads.HasInflight(key) {
		return
	}
	if oldInfo := c.workloads.GetInadmissible(key); oldInfo != nil {
		specChangedSinceEval := oldInfo.LastEvaluatedGeneration != 0 &&
			wInfo.Obj.Generation != oldInfo.LastEvaluatedGeneration

		// Update in place if the workload didn't change to potentially become admissible,
		// unless Eviction/Requeued status changed which can affect queue order.
		if !specChangedSinceEval &&
			equality.Semantic.DeepEqual(oldInfo.Obj.Spec, wInfo.Obj.Spec) &&
			!priorityBoostAnnotationChanged(oldInfo, wInfo) &&
			equality.Semantic.DeepEqual(oldInfo.Obj.Status.ReclaimablePods, wInfo.Obj.Status.ReclaimablePods) &&
			equality.Semantic.DeepEqual(apimeta.FindStatusCondition(oldInfo.Obj.Status.Conditions, kueue.WorkloadEvicted),
				apimeta.FindStatusCondition(wInfo.Obj.Status.Conditions, kueue.WorkloadEvicted)) &&
			equality.Semantic.DeepEqual(apimeta.FindStatusCondition(oldInfo.Obj.Status.Conditions, kueue.WorkloadRequeued),
				apimeta.FindStatusCondition(wInfo.Obj.Status.Conditions, kueue.WorkloadRequeued)) &&
			workload.HasClosedPreemptionGate(oldInfo.Obj) == workload.HasClosedPreemptionGate(wInfo.Obj) &&
			!draRequestsChanged(oldInfo, wInfo) {
			c.workloads.UpdateInadmissible(key, oldInfo, wInfo)
			return
		}
		// Workload is leaving inadmissible; account for its resources before moving.
		c.workloads.RemoveFromInadmissible(key, oldInfo)
	}
	if c.workloads.GetActive(key) == nil && !c.backoffWaitingTimeExpired(wInfo) {
		c.workloads.InsertInadmissible(key, wInfo)
		return
	}
	// Skip to inadmissible if the workload's equivalence class was already bulk-moved to inadmissible
	// (only for BestEffortFIFO; StrictFIFO preserves strict ordering).
	if c.queueingStrategy == kueue.BestEffortFIFO && c.workloads.GetActive(key) == nil && wInfo.SchedulingHash != workload.SchedulingHashUnknown {
		if _, has := c.hashToBulkMoveReason[wInfo.SchedulingHash]; has {
			c.workloads.InsertInadmissible(key, wInfo)
			return
		}
	}
	c.workloads.PushOrUpdateActive(wInfo)
}

func priorityBoostAnnotationChanged(oldInfo, newInfo *workload.Info) bool {
	if !features.Enabled(features.PriorityBoost) {
		return false
	}
	return oldInfo.Obj.Annotations[controllerconstants.PriorityBoostAnnotationKey] != newInfo.Obj.Annotations[controllerconstants.PriorityBoostAnnotationKey]
}

// draRequestsChanged returns true if DRA preprocessing changed TotalRequests.
// DRA extended resources are resolved in Reconcile, which can modify TotalRequests
// without changing the workload Spec.
func draRequestsChanged(oldInfo, newInfo *workload.Info) bool {
	if !features.Enabled(features.KueueDRAIntegration) {
		return false
	}
	return !workload.Semantic.DeepEqual(oldInfo.TotalRequests, newInfo.TotalRequests)
}

func (c *ClusterQueue) GetNoFitReason(wl workload.Reference) (string, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	wlInfo := c.workloads.GetInadmissible(wl)
	if wlInfo == nil {
		return "", false
	}
	reason, ok := c.hashToBulkMoveReason[wlInfo.SchedulingHash]
	return string(reason), ok
}

// RebuildHeap re-establishes the heap invariant after a LocalQueue's
// fair-sharing usage changed. Workloads in the same LocalQueue share that usage,
// so their order relative to each other is unchanged; what changes is their
// position relative to every other LocalQueue's workloads, and all of them shift
// at once. heap.Fix assumes a single element moved while the rest of the heap is
// valid, so fixing these entries one by one can leave the heap invalid; instead
// the whole heap is rebuilt in O(n) with heap.Init. lqName identifies the
// LocalQueue whose reconcile triggered the rebuild and is used only for logging.
func (c *ClusterQueue) RebuildHeap(log logr.Logger, lqName string) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	log.V(3).Info("Rebuilding heap after LocalQueue fair-sharing usage change", "clusterQueue", c.name, "localQueue", lqName)
	c.workloads.RebuildActiveHeap()
}

// backoffWaitingTimeExpired returns true if the current time is after the requeueAt
// and Requeued condition not present or equal True.
func (c *ClusterQueue) backoffWaitingTimeExpired(wInfo *workload.Info) bool {
	if apimeta.IsStatusConditionFalse(wInfo.Obj.Status.Conditions, kueue.WorkloadRequeued) {
		return false
	}
	if wInfo.Obj.Status.RequeueState == nil || wInfo.Obj.Status.RequeueState.RequeueAt == nil {
		return true
	}
	// It needs to verify the requeueAt by "Equal" function
	// since the "After" function evaluates the nanoseconds despite the metav1.Time is seconds level precision.
	return c.clock.Now().After(wInfo.Obj.Status.RequeueState.RequeueAt.Time) ||
		c.clock.Now().Equal(wInfo.Obj.Status.RequeueState.RequeueAt.Time)
}

// Delete removes the workload from ClusterQueue.
func (c *ClusterQueue) Delete(log logr.Logger, wlKey workload.Reference) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.delete(log, wlKey)
}

// delete removes the workload from ClusterQueue without lock.
func (c *ClusterQueue) delete(log logr.Logger, key workload.Reference) {
	// A pending workload is tracked in at most one bucket (see the invariant on
	// inadmissibleWorkloads); each removal below is a no-op when the bucket does
	// not hold the key, and self-consistent when it does.
	// Inflight is skipped because its resources were already removed from pendingResourcesTotal at Pop time.
	if old := c.workloads.GetInadmissible(key); old != nil {
		c.workloads.RemoveFromInadmissible(key, old)
	}

	c.workloads.RemoveActive(key)
	c.workloads.ForgetInflightByKey(key)
	if c.pw.matches(key, false, 0) {
		if logV := log.V(5); logV.Enabled() {
			logV.Info("Clearing preemptor workload due to deletion", "clusterQueue", c.name, "workload", key)
		}
		c.pw.clear()
	}
}

// DeleteFromLocalQueue removes all workloads belonging to this queue from
// the ClusterQueue.
func (c *ClusterQueue) DeleteFromLocalQueue(log logr.Logger, q *LocalQueue, roleTracker *roletracker.RoleTracker, cl *metrics.CustomLabels) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	for _, w := range q.items {
		wlKey := workloadKey(w)
		c.delete(log, wlKey)
	}
	c.workloads.ForgetInflightFromLocalQueue(q.Key)
	for fw := range q.finishedWorkloads {
		c.finishedWorkloads.Delete(fw)
	}
	reportCQFinishedWorkloads(c, roleTracker, cl)
}

// resolveQuotaReservedReason returns statusReason if set, or defaults to
// WorkloadQuotaReservedReasonPendingEvaluation when empty.
// We do not feature-gate the fallback here because this function only computes
// the reason string stored in the ClusterQueue's in-memory hashToBulkMoveReason map.
// Whether status conditions are actually written to the Workload API object is
// governed by the workload controller (shouldCheckEquivalenceHash).
func resolveQuotaReservedReason(reason QuotaReservedReason) QuotaReservedReason {
	if reason == "" {
		return QuotaReservedReasonPendingEvaluation
	}
	return reason
}

// requeueIfNotPresent inserts a workload that cannot be admitted into
// ClusterQueue, unless it is already in the queue. If immediate is true
// or if there was a call to QueueInadmissibleWorkloads after a call to Pop,
// the workload will be pushed back to heap directly. Otherwise, the workload
// will be put into the inadmissibleWorkloads.
// When SchedulingEquivalenceHashing is enabled and the reason is NoFit or
// PreemptionNoCandidates, equivalent workloads in the heap are bulk-moved
// to inadmissible.
func (c *ClusterQueue) requeueIfNotPresent(log logr.Logger, wInfo *workload.Info, immediate bool, reason RequeueReason, quotaReservedReason QuotaReservedReason) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	key := workload.Key(wInfo.Obj)
	// When preemptions are in-progress, track the preempting workload (see documentation
	// of preemptorWorkload). For BestEffortFIFO queues, this also makes it sticky at the head.
	// The preemptor workload is set under the lock so heap operations, which also hold the lock,
	// never observe it changing mid-sort. See Kueue#12740.
	if reason == RequeueReasonPendingPreemption || reason == RequeueReasonPendingMigration {
		if logV := log.V(5); logV.Enabled() {
			logV.Info("Setting preemptor workload", "clusterQueue", wInfo.ClusterQueue, "workload", key)
		}
		c.pw.set(key, c.queueingStrategy == kueue.BestEffortFIFO, wInfo.LastEvaluatedGeneration)
	} else if c.pw.matches(key, false, 0) {
		log.V(3).Info("Clearing preemptor workload", "clusterQueue", wInfo.ClusterQueue, "workload", key, "reason", reason)
		c.pw.clear()
	}
	c.workloads.ForgetInflightByKey(key)

	inadmissibleWl := c.workloads.GetInadmissible(key)

	if c.backoffWaitingTimeExpired(wInfo) &&
		(immediate || c.queueInadmissibleCycle >= c.popCycle || wInfo.LastAssignment.PendingFlavors()) {
		// If the workload was inadmissible, move it back into the queue.
		if inadmissibleWl != nil {
			return c.workloads.MoveToActive(key, inadmissibleWl)
		}
		return c.workloads.PushActiveIfNotPresent(wInfo)
	}

	if inadmissibleWl != nil {
		return false
	}

	if c.workloads.GetActive(key) != nil {
		return false
	}

	c.workloads.InsertInadmissible(key, wInfo)
	logMsg := "Workload couldn't be admitted."
	if c.queueingStrategy == kueue.BestEffortFIFO {
		logMsg += " Moving the head of this ClusterQueue to the consecutive Workload."
	}
	log.V(2).Info(logMsg, "clusterQueue", c.name, "workload", key)

	if features.Enabled(features.SchedulingEquivalenceHashing) && wInfo.SchedulingHash != workload.SchedulingHashUnknown &&
		(reason == RequeueReasonNoFit || reason == RequeueReasonPreemptionNoCandidates) {
		if moved := c.handleInadmissibleHash(wInfo.SchedulingHash, resolveQuotaReservedReason(quotaReservedReason)); moved > 0 {
			log.V(2).Info("Bulk-moved equivalent workloads to inadmissible", "hash", wInfo.SchedulingHash, "movedCount", moved)
		}
	}

	return true
}

// forgetInflight releases the claim on a popped workload. Only correct under the
// Manager lock, which orders it against the other transitions of the claim.
func (c *ClusterQueue) forgetInflight(key workload.Reference) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.workloads.ForgetInflightByKey(key)
}

// hasQueuedWorkloads reports whether any workload is waiting in the active
// heap. Unlike pendingActive, inflight and inadmissible workloads do not
// count: this answers "would a pop find a successor right now".
func (c *ClusterQueue) hasQueuedWorkloads() bool {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.hasActive()
}

// handleInadmissibleHash bulk-moves all heap workloads matching the given
// scheduling hash to inadmissibleWorkloads. Returns the number moved.
// Only applies to BestEffortFIFO queues; in StrictFIFO the head workload
// stays in the heap and must not cause equivalent workloads to be skipped.
func (c *ClusterQueue) handleInadmissibleHash(hash workload.EquivalenceHash, reason QuotaReservedReason) int {
	if c.queueingStrategy != kueue.BestEffortFIFO {
		return 0
	}
	c.hashToBulkMoveReason[hash] = reason
	moved := 0
	for wlInfo := range c.workloads.activeIterator() {
		if wlInfo.SchedulingHash == hash {
			c.workloads.MoveToInadmissible(workloadKey(wlInfo), wlInfo)
			moved++
		}
	}
	return moved
}

// PendingResources returns the total resources requested by all pending workloads,
// aggregated by resource name. Pending workloads have not yet been assigned to flavors.
func (c *ClusterQueue) pendingResources() map[corev1.ResourceName]int64 {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.PendingResources()
}

// PendingTotal returns the total number of pending workloads.
func (c *ClusterQueue) PendingTotal() int {
	active, inadmissible := c.PendingBreakdown()
	return active.Total() + inadmissible.Total()
}

// Pending returns the number of active and inadmissible pending workloads.
func (c *ClusterQueue) Pending() (int, int) {
	active, inadmissible := c.PendingBreakdown()
	return active.Total(), inadmissible.Total()
}

// Pending returns the number of active and inadmissible pending workloads.
func (c *ClusterQueue) PendingBreakdown() (*metrics.LabelValsTracker, *metrics.LabelValsTracker) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.PendingBreakdown()
}

// PendingInLocalQueue returns the number of active and inadmissible pending workloads in LocalQueue.
func (c *ClusterQueue) PendingInLocalQueue(lqRef utilqueue.LocalQueueReference) (int, int) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.PendingActiveInLocalQueue(lqRef), c.workloads.PendingInadmissibleInLocalQueue(lqRef)
}

// PendingBreakdownInLocalQueue returns LabelValsTrackers for active and inadmissible
// pending workloads in the given LocalQueue, keyed by workload custom label values.
func (c *ClusterQueue) PendingBreakdownInLocalQueue(lqRef utilqueue.LocalQueueReference) (*metrics.LabelValsTracker, *metrics.LabelValsTracker) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.PendingBreakdownInLocalQueue(lqRef)
}

// Pop removes the head of the queue and returns it. It returns nil if the
// queue is empty. Pop advances popCycle, starting a new evaluation epoch:
// requeueIfNotPresent treats inadmissible-requeue events stamped before the
// current epoch as stale.
func (c *ClusterQueue) Pop() *workload.Info {
	return c.pop(true)
}

// PopMidCycle removes the head of the queue like Pop, but does not advance
// popCycle. Advancing it declares the previous scheduling attempt over and its
// signals consumed; a mid-cycle pop (refill) is part of an attempt still in
// progress, whose pending "requeue the inadmissible workloads" signal must stay
// fresh.
func (c *ClusterQueue) PopMidCycle() *workload.Info {
	return c.pop(false)
}

func (c *ClusterQueue) pop(newEpoch bool) *workload.Info {
	c.rwm.Lock()
	defer c.rwm.Unlock()

	if c.hasPendingPenalties() {
		// A pending penalty can change the fair-sharing usage of several workloads from
		// the same LocalQueue at once, so they all shift relative to other LocalQueues'
		// workloads together. heap.Fix assumes a single element moved while the rest of
		// the heap is valid, so fixing these entries one by one can leave the heap
		// invalid; instead the whole invariant is re-established in O(n) with heap.Init.
		c.workloads.RebuildActiveHeap()
	}

	if newEpoch {
		c.popCycle++
	}
	return c.workloads.PopActive()
}

func (c *ClusterQueue) hasPendingPenalties() bool {
	if c.afsUsageLedger == nil {
		return false
	}

	for lqKey := range c.lqWeights {
		if c.afsUsageLedger.HasPendingPenalty(lqKey) {
			return true
		}
	}
	return false
}

// Dump produces a dump of the current workloads in the heap of
// this ClusterQueue. It returns false if the queue is empty,
// otherwise returns true.
func (c *ClusterQueue) Dump() ([]workload.Reference, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.DumpActive()
}

func (c *ClusterQueue) DumpInadmissible() ([]workload.Reference, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.DumpInadmissible()
}

func (c *ClusterQueue) DumpInflight() ([]workload.Reference, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.DumpInflight()
}

// Snapshot returns a copy of pending workloads in queue order.
// When fair-sharing is enabled, FS usage is pre-computed per LocalQueue
// from a point-in-time copy of AFS state before sorting.
func (c *ClusterQueue) Snapshot() []*workload.Info {
	elements := c.totalElements()
	c.snapshotSort(elements)
	return elements
}

// buildSnapshotSort returns a function that sorts workload elements for Snapshot().
// The sort runs without holding the ClusterQueue lock, so it captures the sticky
// workload once per sort (via preemptorWorkload.capturedStickyMatcher) to keep the comparison
// transitive even if the sticky workload changes concurrently. See Kueue#12740.
// When fair-sharing is enabled, it also pre-computes FS usage per LocalQueue from
// deep-copied AFS state to avoid inconsistent comparisons from concurrent updates.
func buildSnapshotSort(
	ctx context.Context,
	wo workload.Ordering,
	pw *preemptorWorkload,
	cl client.Client,
	enableAdmissionFs bool,
	fsResWeights map[corev1.ResourceName]float64,
	afsUsageLedger *queueafs.AfsUsageLedger,
) func(elements []*workload.Info) {
	log := ctrl.LoggerFrom(ctx)
	if !enableAdmissionFs {
		return func(elements []*workload.Info) {
			slices.SortFunc(elements, baseCompareFunc(log, wo, pw.capturedStickyMatcher()))
		}
	}

	getLQWeight := func(lqKey utilqueue.LocalQueueReference) (float64, bool) {
		if cl == nil {
			return 1, true
		}
		ns, name := utilqueue.MustParseLocalQueueReference(lqKey)
		lqWeight, err := afs.ResolveLQWeight(ctx, cl, client.ObjectKey{Namespace: ns, Name: string(name)})
		if err != nil {
			log.V(2).Error(err, "Failed to get LocalQueue for FS weight; falling back to base ordering for snapshot", "localQueue", klog.KRef(ns, string(name)))
			return 0, false
		}
		return lqWeight, true
	}

	return func(elements []*workload.Info) {
		// Capture the sticky workload once so the sort stays transitive without
		// holding the lock. See Kueue#12740.
		baseCmp := baseCompareFunc(log, wo, pw.capturedStickyMatcher())
		usageCache := make(map[utilqueue.LocalQueueReference]float64)
		for _, wInfo := range elements {
			lqKey := utilqueue.KeyFromWorkload(wInfo.Obj)
			if _, exists := usageCache[lqKey]; exists {
				continue
			}
			var consumed, penalty corev1.ResourceList
			if afsUsageLedger != nil {
				if entry, found := afsUsageLedger.Get(lqKey); found {
					consumed = entry.Resources.DeepCopy()
					penalty = entry.PendingPenalty().DeepCopy()
				}
			}
			lqWeight, ok := getLQWeight(lqKey)
			if !ok {
				// A partial FS usage cache would mix fair-sharing and base comparisons,
				// which can be non-transitive. Fall back to base ordering for the whole
				// snapshot. See Kueue#12534.
				slices.SortFunc(elements, baseCmp)
				return
			}
			usageCache[lqKey] = afs.CalculateUsage(consumed, penalty, lqWeight, fsResWeights)
		}

		slices.SortFunc(elements, func(a, b *workload.Info) int {
			lqA := utilqueue.KeyFromWorkload(a.Obj)
			lqB := utilqueue.KeyFromWorkload(b.Obj)
			usageA, okA := usageCache[lqA]
			usageB, okB := usageCache[lqB]
			if okA && okB {
				if cmpResult := cmp.Compare(usageA, usageB); cmpResult != 0 {
					return cmpResult
				}
			}
			return baseCmp(a, b)
		})
	}
}

// Info returns workload.Info for the workload key.
// Users of this method should not modify the returned object.
func (c *ClusterQueue) Info(key workload.Reference) *workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.GetActive(key)
}

func (c *ClusterQueue) trackedInfo(key workload.Reference) *workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.Get(key)
}

// totalElements returns all pending workloads (heap + inadmissible + inflight).
// The returned order is non-deterministic; callers should sort if needed.
func (c *ClusterQueue) totalElements() []*workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.workloads.DumpAll()
}

// Active returns true if the queue is active
func (c *ClusterQueue) Active() bool {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.active
}

// RequeueIfNotPresent inserts a workload that was not
// admitted back into the ClusterQueue.
// This may be done either "immediately" or "non-immediately";
// in the latter case the implementation may choose to keep the workload in "inadmissible workloads"
// where it doesn't compete with other workloads, until cluster events free up quota.
// The workload should not be reinserted if it's already in the ClusterQueue.
// quotaReservedReason represents the WorkloadQuotaReserved condition reason computed by the scheduler.
// Returns true if the workload was inserted.
func (c *ClusterQueue) RequeueIfNotPresent(ctx context.Context, wInfo *workload.Info, reason RequeueReason, quotaReservedReason QuotaReservedReason) bool {
	log := ctrl.LoggerFrom(ctx)
	var immediate bool
	if c.queueingStrategy == kueue.StrictFIFO {
		immediate = reason != RequeueReasonNamespaceMismatch
	} else {
		immediate = reason == RequeueReasonFailedAfterNomination ||
			reason == RequeueReasonSnapshotFailed ||
			reason == RequeueReasonPendingPreemption ||
			reason == RequeueReasonPendingMigration ||
			reason == RequeueReasonPreemptionFailed
	}
	return c.requeueIfNotPresent(log, wInfo, immediate, reason, quotaReservedReason)
}

// baseCompareFunc orders workloads by sticky status, priority, timestamp, and UID.
// stickyMatches reports whether a workload is the sticky one; callers pass a
// live matcher (stickyWorkload.matches) for the heap or a captured one
// (stickyWorkload.capturedMatcher) for the lock-free Snapshot sort. See Kueue#12740.
func baseCompareFunc(log logr.Logger, wo workload.Ordering, stickyMatches func(workload.Reference) bool) func(a, b *workload.Info) int {
	return func(a, b *workload.Info) int {
		aSticky := stickyMatches(workload.Key(a.Obj))
		bSticky := stickyMatches(workload.Key(b.Obj))
		if aSticky != bSticky {
			if aSticky {
				logStickyWorkloadSelectionIfVerbose(log, a.Obj)
				return -1
			}
			logStickyWorkloadSelectionIfVerbose(log, b.Obj)
			return 1
		}

		p1 := utilpriority.EffectivePriority(log, a.Obj)
		p2 := utilpriority.EffectivePriority(log, b.Obj)
		// Higher priority comes first (reverse order).
		if cmpResult := cmp.Compare(p2, p1); cmpResult != 0 {
			return cmpResult
		}

		tA := wo.GetQueueOrderTimestamp(a.Obj)
		tB := wo.GetQueueOrderTimestamp(b.Obj)
		if !tA.Equal(tB) {
			if tA.Before(tB) {
				return -1
			}
			return 1
		}
		return cmp.Compare(a.Obj.UID, b.Obj.UID)
	}
}

// queueOrderingFunc composes fair-sharing usage (when enabled) with baseCompareFunc.
// It reads the LocalQueue weight via getLQWeight (backed by the cached weights)
// instead of a fallible API read, so a failed LocalQueue lookup can no longer flip
// the ordering rule between comparisons. See Kueue#13476.
func queueOrderingFunc(
	ctx context.Context,
	getLQWeight func(utilqueue.LocalQueueReference) float64,
	wo workload.Ordering,
	fsResWeights map[corev1.ResourceName]float64,
	enableAdmissionFs bool,
	afsUsageLedger *queueafs.AfsUsageLedger,
	stickyMatches func(workload.Reference) bool,
) func(a, b *workload.Info) int {
	log := ctrl.LoggerFrom(ctx)
	baseCmp := baseCompareFunc(log, wo, stickyMatches)
	if !enableAdmissionFs {
		return baseCmp
	}
	return func(a, b *workload.Info) int {
		lqAUsage := a.ComputeLocalQueueFSUsage(getLQWeight(utilqueue.KeyFromWorkload(a.Obj)), fsResWeights, afsUsageLedger)
		lqBUsage := b.ComputeLocalQueueFSUsage(getLQWeight(utilqueue.KeyFromWorkload(b.Obj)), fsResWeights, afsUsageLedger)
		log.V(3).Info("Resource usage from LocalQueue", "localQueue", klog.KRef(a.Obj.Namespace, string(a.Obj.Spec.QueueName)), "usage", lqAUsage)
		log.V(3).Info("Resource usage from LocalQueue", "localQueue", klog.KRef(b.Obj.Namespace, string(b.Obj.Spec.QueueName)), "usage", lqBUsage)
		if cmpResult := cmp.Compare(lqAUsage, lqBUsage); cmpResult != 0 {
			return cmpResult
		}
		return baseCmp(a, b)
	}
}

func (c *ClusterQueue) addLocalQueue(lqKey utilqueue.LocalQueueReference, weight float64) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.lqWeights[lqKey] = weight
}

func (c *ClusterQueue) deleteLocalQueue(lqKey utilqueue.LocalQueueReference) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	delete(c.lqWeights, lqKey)
}

// UpdateLocalQueueWeight refreshes the cached weight for a LocalQueue and, if it
// changed, reheapifies the pending workloads so the heap stays ordered. The
// LocalQueue's workloads keep their order relative to each other but all shift at
// once relative to every other LocalQueue's workloads, so the whole heap
// invariant is re-established in O(n) with heap.Init rather than fixing entries
// individually (heap.Fix assumes only a single element moved). See Kueue#13476.
func (c *ClusterQueue) UpdateLocalQueueWeight(lqKey utilqueue.LocalQueueReference, weight float64) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	if old, ok := c.lqWeights[lqKey]; ok && old == weight {
		return
	}
	c.lqWeights[lqKey] = weight
	c.workloads.RebuildActiveHeap()
}
