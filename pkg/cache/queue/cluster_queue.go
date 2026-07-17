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
	"maps"
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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
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
	"sigs.k8s.io/kueue/pkg/util/resource"
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

// stickyWorkload is the workload at the ClusterQueue head which is
// currently preempting workloads. It is only enabled for
// BestEffortFIFO policy, and prevents skipped over ineligible
// workloads from going back to the head of the queue.  A workload is
// considered sticky until it is admitted, unschedulable, or deleted.
// See Kueue#6929 and Kueue#7101 for motivation.
//
// The workloadName field is accessed concurrently and drives both the CQ heap
// ordering and the Snapshot ordering used by the visibility server. Two
// mechanisms keep every sort transitive (see Kueue#12740):
//   - Writes (set/clear) happen under the ClusterQueue's rwm lock, so heap
//     operations, which also hold the lock, never observe it changing mid-sort.
//   - Snapshot sorts a copy of the pending workloads without holding the lock,
//     so it captures the sticky workload once per sort via capturedMatcher()
//     instead of re-reading it on every comparison.
//
// The field holds a single whole value (nil means no sticky workload), so an
// atomic.Pointer keeps individual reads and writes memory-safe on top of the
// ordering guarantees above.
type stickyWorkload struct {
	workloadName atomic.Pointer[workload.Reference]
}

func (s *stickyWorkload) matches(workload workload.Reference) bool {
	name := s.workloadName.Load()
	return name != nil && *name == workload
}

// capturedMatcher captures the current sticky workload once and returns a
// predicate bound to that fixed value. A sort that compares through the returned
// predicate stays transitive even if set/clear runs concurrently, because every
// comparison in that sort observes the same sticky workload. See Kueue#12740.
func (s *stickyWorkload) capturedMatcher() func(workload.Reference) bool {
	name := s.workloadName.Load()
	if name == nil {
		return func(workload.Reference) bool { return false }
	}
	captured := *name
	return func(key workload.Reference) bool {
		return captured == key
	}
}

func (s *stickyWorkload) clear() {
	s.workloadName.Store(nil)
}

func (s *stickyWorkload) set(workload workload.Reference) {
	s.workloadName.Store(&workload)
}

func logStickyWorkloadSelectionIfVerbose(log logr.Logger, wl *kueue.Workload) {
	if logV := log.V(5); logV.Enabled() {
		logV.Info("Prioritizing sticky workload", "workload", workload.Key(wl))
	}
}

type ClusterQueue struct {
	hierarchy.ClusterQueue[*cohort]
	name              kueue.ClusterQueueReference
	heap              heap.Heap[workload.Info, workload.Reference]
	namespaceSelector labels.Selector
	active            bool

	// inadmissibleWorkloads are workloads that have been tried at least once and couldn't be admitted.
	inadmissibleWorkloads inadmissibleWorkloads

	// hashToBulkMoveReason tracks scheduling equivalence classes and the reason
	// why workloads with that hash were bulk-moved to inadmissibleWorkloads.
	// Cleared when queueInadmissibleWorkloads runs.
	hashToBulkMoveReason map[workload.EquivalenceHash]QuotaReservedReason

	// schedulingHashes tracks the scheduling equivalence hashes of pending
	// workloads for the pending_scheduling_hashes metric.
	schedulingHashes *schedulingHashCounts

	finishedWorkloads sets.Set[workload.Reference]

	// popCycle identifies the last call to Pop. It's incremented when calling Pop.
	// popCycle and queueInadmissibleCycle are used to track when there is a requeuing
	// of inadmissible workloads while a workload is being scheduled.
	popCycle int64

	// inflight is non-nil when a workload has been popped by the scheduler but
	// not yet requeued or deleted.
	inflight *workload.Info

	// queueInadmissibleCycle stores the popId at the time when
	// QueueInadmissibleWorkloads is called.
	queueInadmissibleCycle int64

	compareFunc  func(a, b *workload.Info) int
	snapshotSort func(elements []*workload.Info)

	queueingStrategy kueue.QueueingStrategy

	rwm sync.RWMutex

	clock clock.Clock

	AdmissionScope *kueue.AdmissionScope

	afsEntryPenalties         *queueafs.AfsEntryPenalties
	localQueuesInClusterQueue map[utilqueue.LocalQueueReference]bool

	sw *stickyWorkload

	ConcurrentAdmissionPolicy *kueue.ConcurrentAdmissionPolicy
	// pendingResourcesTotal is the incremental sum of TotalRequests across workloads
	// in heap and inadmissibleWorkloads (not inflight). Updated at each mutation site so
	// pendingResources() is O(1) rather than O(N).
	// Configured resources are seeded at 0 by Update() so they appear in metrics
	// even when no workloads are pending; stale zero entries are pruned on Update().
	pendingResourcesTotal map[corev1.ResourceName]int64
}

func (c *ClusterQueue) GetName() kueue.ClusterQueueReference {
	return c.name
}

func workloadKey(i *workload.Info) workload.Reference {
	return workload.Key(i.Obj)
}

type clusterQueueOption func(*clusterQueueOptions)

type clusterQueueOptions struct {
	fsResWeights         map[corev1.ResourceName]float64
	enableAdmissionFs    bool
	afsEntryPenalties    *queueafs.AfsEntryPenalties
	afsConsumedResources *queueafs.AfsConsumedResources
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

func withAfsEntryPenalties(penalties *queueafs.AfsEntryPenalties) clusterQueueOption {
	return func(o *clusterQueueOptions) {
		o.afsEntryPenalties = penalties
	}
}

func withAfsConsumedResources(consumed *queueafs.AfsConsumedResources) clusterQueueOption {
	return func(o *clusterQueueOptions) {
		o.afsConsumedResources = consumed
	}
}

func newClusterQueue(
	ctx context.Context,
	client client.Client,
	cq *kueue.ClusterQueue,
	wo workload.Ordering,
	afsConfig *config.AdmissionFairSharing,
	afsEntryPenalties *queueafs.AfsEntryPenalties,
	afsConsumedResources *queueafs.AfsConsumedResources,
) (*ClusterQueue, error) {
	enableAdmissionFs, fsResWeights := afs.ResourceWeights(cq.Spec.AdmissionScope, afsConfig)
	cqImpl := newClusterQueueImpl(
		ctx,
		client,
		wo,
		realClock,
		withFSResWeights(fsResWeights),
		withEnableAdmissionFs(enableAdmissionFs),
		withAfsEntryPenalties(afsEntryPenalties),
		withAfsConsumedResources(afsConsumedResources),
	)
	err := cqImpl.Update(cq)
	if err != nil {
		return nil, err
	}
	return cqImpl, nil
}

func newClusterQueueImpl(ctx context.Context, client client.Client, wo workload.Ordering, clock clock.Clock, opts ...clusterQueueOption) *ClusterQueue {
	options := &clusterQueueOptions{}
	for _, opt := range opts {
		opt(options)
	}
	sw := stickyWorkload{}
	// The heap comparator reads the sticky workload live on every comparison;
	// this is safe because sticky writes and heap operations both hold rwm.
	compareFunc := queueOrderingFunc(ctx, client, wo, options.fsResWeights, options.enableAdmissionFs, options.afsEntryPenalties, options.afsConsumedResources, sw.matches)
	// Derive lessFunc from compareFunc for the heap.
	lessFunc := func(a, b *workload.Info) bool { return compareFunc(a, b) < 0 }
	// Snapshot sorts without the lock, so it captures the sticky workload once
	// per sort rather than reading it live. See Kueue#12740.
	snapshotSort := buildSnapshotSort(
		ctx, wo, &sw, client,
		options.enableAdmissionFs, options.fsResWeights,
		options.afsEntryPenalties, options.afsConsumedResources,
	)
	return &ClusterQueue{
		heap:                      *heap.New(workloadKey, lessFunc),
		inadmissibleWorkloads:     make(inadmissibleWorkloads),
		hashToBulkMoveReason:      make(map[workload.EquivalenceHash]QuotaReservedReason),
		schedulingHashes:          newSchedulingHashCounts(),
		finishedWorkloads:         sets.New[workload.Reference](),
		queueInadmissibleCycle:    -1,
		compareFunc:               compareFunc,
		snapshotSort:              snapshotSort,
		rwm:                       sync.RWMutex{},
		clock:                     clock,
		afsEntryPenalties:         options.afsEntryPenalties,
		localQueuesInClusterQueue: make(map[utilqueue.LocalQueueReference]bool),
		sw:                        &sw,
		pendingResourcesTotal:     make(map[corev1.ResourceName]int64),
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
	c.updateConfiguredResources(apiCQ)
	return nil
}

// updateConfiguredResources seeds pendingResourcesTotal with 0 for newly configured
// resources so they appear in metrics even when no workloads are pending, and prunes
// zero entries for resources removed from the spec.
func (c *ClusterQueue) updateConfiguredResources(apiCQ *kueue.ClusterQueue) {
	newConfigured := sets.New[corev1.ResourceName]()
	for _, rg := range apiCQ.Spec.ResourceGroups {
		for _, fq := range rg.Flavors {
			for _, r := range fq.Resources {
				newConfigured.Insert(r.Name)
				if _, exists := c.pendingResourcesTotal[r.Name]; !exists {
					c.pendingResourcesTotal[r.Name] = 0
				}
			}
		}
	}
	for r, v := range c.pendingResourcesTotal {
		if v == 0 && !newConfigured.Has(r) {
			delete(c.pendingResourcesTotal, r)
		}
	}
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
		if c.pushActiveIfNotPresent(info) {
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
	if c.inflight != nil && workload.Key(c.inflight.Obj) == key {
		return
	}
	if oldInfo := c.inadmissibleWorkloads.get(key); oldInfo != nil {
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
			c.updateInadmissible(key, oldInfo, wInfo)
			return
		}
		// Workload is leaving inadmissible; account for its resources before moving.
		c.removeFromInadmissible(key, oldInfo)
	}
	if c.heap.GetByKey(key) == nil && !c.backoffWaitingTimeExpired(wInfo) {
		c.insertInadmissible(key, wInfo)
		return
	}
	// Skip to inadmissible if the workload's equivalence class was already bulk-moved to inadmissible
	// (only for BestEffortFIFO; StrictFIFO preserves strict ordering).
	if c.queueingStrategy == kueue.BestEffortFIFO && c.heap.GetByKey(key) == nil && wInfo.SchedulingHash != workload.SchedulingHashUnknown {
		if _, has := c.hashToBulkMoveReason[wInfo.SchedulingHash]; has {
			c.insertInadmissible(key, wInfo)
			return
		}
	}
	c.pushOrUpdateActive(wInfo)
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
	return !equality.Semantic.DeepEqual(oldInfo.TotalRequests, newInfo.TotalRequests)
}

func (c *ClusterQueue) GetNoFitReason(wl workload.Reference) (string, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	wlInfo := c.inadmissibleWorkloads.get(wl)
	if wlInfo == nil {
		return "", false
	}
	reason, ok := c.hashToBulkMoveReason[wlInfo.SchedulingHash]
	return string(reason), ok
}

func (c *ClusterQueue) RebuildLocalQueue(lqName string) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	for _, wl := range c.heap.List() {
		if string(wl.Obj.Spec.QueueName) == lqName {
			c.heap.PushOrUpdate(wl)
		}
	}
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

func (c *ClusterQueue) addPendingResources(wInfo *workload.Info) {
	for _, ps := range wInfo.TotalRequests {
		for name, q := range ps.Requests {
			c.pendingResourcesTotal[name] += q
		}
	}
}

func (c *ClusterQueue) subtractPendingResources(wInfo *workload.Info) {
	for _, ps := range wInfo.TotalRequests {
		for name, q := range ps.Requests {
			c.pendingResourcesTotal[name] -= q
		}
	}
}

func (c *ClusterQueue) pushActiveIfNotPresent(wInfo *workload.Info) bool {
	if !c.heap.PushIfNotPresent(wInfo) {
		return false
	}
	c.schedulingHashes.addActive(wInfo)
	c.addPendingResources(wInfo)
	return true
}

func (c *ClusterQueue) pushOrUpdateActive(wInfo *workload.Info) {
	if old := c.heap.GetByKey(workloadKey(wInfo)); old != nil {
		c.schedulingHashes.removeActive(old)
		c.subtractPendingResources(old)
	}
	c.heap.PushOrUpdate(wInfo)
	c.schedulingHashes.addActive(wInfo)
	c.addPendingResources(wInfo)
}

func (c *ClusterQueue) deleteActive(key workload.Reference) {
	old := c.heap.GetByKey(key)
	if old == nil {
		return
	}
	c.heap.Delete(key)
	c.schedulingHashes.removeActive(old)
	c.subtractPendingResources(old)
}

func (c *ClusterQueue) popActive() *workload.Info {
	wInfo := c.heap.Pop()
	c.schedulingHashes.removeActive(wInfo)
	c.subtractPendingResources(wInfo)
	return wInfo
}

func (c *ClusterQueue) insertInadmissible(key workload.Reference, wInfo *workload.Info) {
	c.inadmissibleWorkloads.insert(key, wInfo)
	c.schedulingHashes.addInadmissible(wInfo)
	c.addPendingResources(wInfo)
}

func (c *ClusterQueue) updateInadmissible(key workload.Reference, oldInfo, newInfo *workload.Info) {
	// This is the in-place path for updates that cannot change admissibility,
	// so retain the existing pendingResourcesTotal contribution.
	c.schedulingHashes.removeInadmissible(oldInfo)
	c.inadmissibleWorkloads.insert(key, newInfo)
	c.schedulingHashes.addInadmissible(newInfo)
}

func (c *ClusterQueue) moveInadmissibleToActive(key workload.Reference, wInfo *workload.Info) bool {
	if old := c.inadmissibleWorkloads.get(key); old != nil {
		wInfo = old
		c.schedulingHashes.removeInadmissible(old)
		c.inadmissibleWorkloads.delete(key)
		if c.heap.PushIfNotPresent(wInfo) {
			// Keep the existing resource contribution while changing buckets.
			c.schedulingHashes.addActive(wInfo)
			return true
		}
		// AddFromLocalQueue already counted the heap copy separately.
		c.subtractPendingResources(old)
		return false
	}
	return c.pushActiveIfNotPresent(wInfo)
}

func (c *ClusterQueue) moveActiveToInadmissible(wInfo *workload.Info) {
	key := workloadKey(wInfo)
	c.heap.Delete(key)
	c.schedulingHashes.removeActive(wInfo)
	if old := c.inadmissibleWorkloads.get(key); old != nil {
		c.removeFromInadmissible(key, old)
	}
	// Keep the active resource contribution while changing buckets.
	c.inadmissibleWorkloads.insert(key, wInfo)
	c.schedulingHashes.addInadmissible(wInfo)
}

func (c *ClusterQueue) removeFromInadmissible(key workload.Reference, wInfo *workload.Info) {
	c.schedulingHashes.removeInadmissible(wInfo)
	c.subtractPendingResources(wInfo)
	c.inadmissibleWorkloads.delete(key)
}

// Delete removes the workload from ClusterQueue.
func (c *ClusterQueue) Delete(log logr.Logger, wlKey workload.Reference) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.delete(log, wlKey)
}

// delete removes the workload from ClusterQueue without lock.
func (c *ClusterQueue) delete(log logr.Logger, key workload.Reference) {
	// Remove the workload from every bucket; AddFromLocalQueue resync can
	// temporarily leave the same key in both inadmissibleWorkloads and the heap.
	// Inflight is skipped because its resources were already removed from pendingResourcesTotal at Pop time.
	if old := c.inadmissibleWorkloads.get(key); old != nil {
		c.removeFromInadmissible(key, old)
	}
	c.deleteActive(key)
	c.forgetInflightByKey(key)
	if c.sw.matches(key) {
		if logV := log.V(5); logV.Enabled() {
			logV.Info("Clearing sticky workload due to deletion", "clusterQueue", c.name, "workload", key)
		}
		c.sw.clear()
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
	// When preemptions are in-progress, keep re-attempting the same workload at
	// the head for BestEffortFIFO queues (see documentation of stickyWorkload).
	// The sticky workload is set under the lock so heap operations, which also
	// hold the lock, never observe it changing mid-sort. See Kueue#12740.
	if (reason == RequeueReasonPendingPreemption || reason == RequeueReasonPendingMigration) && c.queueingStrategy == kueue.BestEffortFIFO {
		if logV := log.V(5); logV.Enabled() {
			logV.Info("Setting sticky workload", "clusterQueue", wInfo.ClusterQueue, "workload", key)
		}
		c.sw.set(key)
	}
	c.forgetInflightByKey(key)

	inadmissibleWl := c.inadmissibleWorkloads.get(key)

	if c.backoffWaitingTimeExpired(wInfo) &&
		(immediate || c.queueInadmissibleCycle >= c.popCycle || wInfo.LastAssignment.PendingFlavors()) {
		return c.moveInadmissibleToActive(key, wInfo)
	}

	if inadmissibleWl != nil {
		return false
	}

	if c.heap.GetByKey(key) != nil {
		return false
	}

	c.insertInadmissible(key, wInfo)
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

func (c *ClusterQueue) forgetInflightByKey(key workload.Reference) {
	if c.inflight != nil && workload.Key(c.inflight.Obj) == key {
		c.inflight = nil
	}
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
	for _, wInfo := range c.heap.List() {
		if wInfo.SchedulingHash == hash {
			c.moveActiveToInadmissible(wInfo)
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
	result := maps.Clone(c.pendingResourcesTotal)
	if c.inflight != nil {
		for _, ps := range c.inflight.TotalRequests {
			for name, q := range ps.Requests {
				result[name] += q
			}
		}
	}
	return result
}

// PendingTotal returns the total number of pending workloads.
func (c *ClusterQueue) PendingTotal() int {
	active, inadmissible := c.Pending()
	return active + inadmissible
}

// Pending returns the number of active and inadmissible pending workloads.
func (c *ClusterQueue) Pending() (int, int) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.pendingActive(), c.pendingInadmissible()
}

// pendingActive returns the number of active pending workloads,
// workloads that are in the admission queue.
func (c *ClusterQueue) pendingActive() int {
	result := c.heap.Len()
	if c.inflight != nil {
		result++
	}
	return result
}

// pendingInadmissible returns the number of inadmissible pending workloads,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (c *ClusterQueue) pendingInadmissible() int {
	return c.inadmissibleWorkloads.len()
}

// PendingInLocalQueue returns the number of active and inadmissible pending workloads in LocalQueue.
func (c *ClusterQueue) PendingInLocalQueue(lqRef utilqueue.LocalQueueReference) (int, int) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.pendingActiveInLocalQueue(lqRef), c.pendingInadmissibleInLocalQueue(lqRef)
}

// pendingActiveInLocalQueue returns the number of active pending workloads in LocalQueue,
// workloads that are in the admission queue.
func (c *ClusterQueue) pendingActiveInLocalQueue(lqRef utilqueue.LocalQueueReference) (active int) {
	for _, wl := range c.heap.List() {
		wlLqKey := utilqueue.KeyFromWorkload(wl.Obj)
		if wlLqKey == lqRef {
			active++
		}
	}
	if c.inflight != nil && utilqueue.KeyFromWorkload(c.inflight.Obj) == lqRef {
		active++
	}
	return
}

// pendingInadmissibleInLocalQueue returns the number of inadmissible pending workloads in LocalQueue,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (c *ClusterQueue) pendingInadmissibleInLocalQueue(lqRef utilqueue.LocalQueueReference) (inadmissible int) {
	for _, wl := range c.inadmissibleWorkloads {
		wlLqKey := utilqueue.KeyFromWorkload(wl.Obj)
		if wlLqKey == lqRef {
			inadmissible++
		}
	}
	return
}

// Pop removes the head of the queue and returns it. It returns nil if the
// queue is empty.
func (c *ClusterQueue) Pop() *workload.Info {
	c.rwm.Lock()
	defer c.rwm.Unlock()

	if c.hasPendingPenalties() {
		c.rebuildAll()
	}

	c.popCycle++
	if c.heap.Len() == 0 {
		c.inflight = nil
		return nil
	}
	wl := c.popActive()
	c.inflight = wl
	c.inflight.LastEvaluatedGeneration = c.inflight.Obj.Generation
	return c.inflight
}

// rebuildAll rebuilds the entire heap. Must be called with lock held.
func (c *ClusterQueue) rebuildAll() {
	for _, wl := range c.heap.List() {
		c.heap.PushOrUpdate(wl)
	}
}

func (c *ClusterQueue) hasPendingPenalties() bool {
	if c.afsEntryPenalties == nil {
		return false
	}

	for lqKey := range c.localQueuesInClusterQueue {
		lqPenalty := c.afsEntryPenalties.Peek(lqKey)
		if !resource.IsZero(lqPenalty) {
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
	if c.heap.Len() == 0 {
		return nil, false
	}
	elements := make([]workload.Reference, c.heap.Len())
	for i, info := range c.heap.List() {
		elements[i] = workload.Key(info.Obj)
	}
	return elements, true
}

func (c *ClusterQueue) DumpInadmissible() ([]workload.Reference, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	if c.inadmissibleWorkloads.empty() {
		return nil, false
	}
	elements := make([]workload.Reference, 0, c.inadmissibleWorkloads.len())
	for _, info := range c.inadmissibleWorkloads {
		elements = append(elements, workload.Key(info.Obj))
	}
	return elements, true
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
// workload once per sort (via stickyWorkload.capturedMatcher) to keep the comparison
// transitive even if the sticky workload changes concurrently. See Kueue#12740.
// When fair-sharing is enabled, it also pre-computes FS usage per LocalQueue from
// deep-copied AFS state to avoid inconsistent comparisons from concurrent updates.
func buildSnapshotSort(
	ctx context.Context,
	wo workload.Ordering,
	sw *stickyWorkload,
	cl client.Client,
	enableAdmissionFs bool,
	fsResWeights map[corev1.ResourceName]float64,
	afsEntryPenalties *queueafs.AfsEntryPenalties,
	afsConsumedResources *queueafs.AfsConsumedResources,
) func(elements []*workload.Info) {
	log := ctrl.LoggerFrom(ctx)
	if !enableAdmissionFs {
		return func(elements []*workload.Info) {
			slices.SortFunc(elements, baseCompareFunc(log, wo, sw.capturedMatcher()))
		}
	}

	getLQWeight := func(lqKey utilqueue.LocalQueueReference) (float64, bool) {
		if cl == nil {
			return 1, true
		}
		ns, name := utilqueue.MustParseLocalQueueReference(lqKey)
		var lq kueue.LocalQueue
		if err := cl.Get(ctx, client.ObjectKey{Namespace: ns, Name: string(name)}, &lq); err != nil {
			log.V(2).Error(err, "Failed to get LocalQueue for FS weight", "localQueue", klog.KRef(ns, string(name)))
			return 0, false
		}
		return afs.LQWeightAsFloat64(&lq), true
	}

	return func(elements []*workload.Info) {
		// Capture the sticky workload once so the sort stays transitive without
		// holding the lock. See Kueue#12740.
		baseCmp := baseCompareFunc(log, wo, sw.capturedMatcher())
		usageCache := make(map[utilqueue.LocalQueueReference]float64)
		for _, wInfo := range elements {
			lqKey := utilqueue.KeyFromWorkload(wInfo.Obj)
			if _, exists := usageCache[lqKey]; exists {
				continue
			}
			var consumed, penalty corev1.ResourceList
			if afsConsumedResources != nil {
				if entry, found := afsConsumedResources.Get(lqKey); found {
					consumed = entry.Resources.DeepCopy()
				}
			}
			if afsEntryPenalties != nil {
				penalty = afsEntryPenalties.Peek(lqKey).DeepCopy()
			}
			lqWeight, ok := getLQWeight(lqKey)
			if !ok {
				continue
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
	return c.heap.GetByKey(key)
}

// totalElements returns all pending workloads (heap + inadmissible + inflight).
// The returned order is non-deterministic; callers should sort if needed.
func (c *ClusterQueue) totalElements() []*workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	totalLen := c.heap.Len() + c.inadmissibleWorkloads.len()
	elements := make([]*workload.Info, 0, totalLen)
	elements = append(elements, c.heap.List()...)
	for _, e := range c.inadmissibleWorkloads {
		elements = append(elements, e)
	}
	if c.inflight != nil {
		elements = append(elements, c.inflight)
	}
	return elements
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
func queueOrderingFunc(
	ctx context.Context,
	cl client.Client,
	wo workload.Ordering,
	fsResWeights map[corev1.ResourceName]float64,
	enableAdmissionFs bool,
	afsEntryPenalties *queueafs.AfsEntryPenalties,
	afsConsumedResources *queueafs.AfsConsumedResources,
	stickyMatches func(workload.Reference) bool,
) func(a, b *workload.Info) int {
	log := ctrl.LoggerFrom(ctx)
	baseCmp := baseCompareFunc(log, wo, stickyMatches)
	if !enableAdmissionFs {
		return baseCmp
	}
	return func(a, b *workload.Info) int {
		lqAUsage, errA := a.CalcLocalQueueFSUsage(ctx, cl, fsResWeights, afsEntryPenalties, afsConsumedResources)
		lqBUsage, errB := b.CalcLocalQueueFSUsage(ctx, cl, fsResWeights, afsEntryPenalties, afsConsumedResources)
		switch {
		case errA != nil:
			log.V(2).Error(errA, "Error determining LocalQueue usage")
		case errB != nil:
			log.V(2).Error(errB, "Error determining LocalQueue usage")
		default:
			log.V(3).Info("Resource usage from LocalQueue", "localQueue", klog.KRef(a.Obj.Namespace, string(a.Obj.Spec.QueueName)), "usage", lqAUsage)
			log.V(3).Info("Resource usage from LocalQueue", "localQueue", klog.KRef(b.Obj.Namespace, string(b.Obj.Spec.QueueName)), "usage", lqBUsage)
			if cmpResult := cmp.Compare(lqAUsage, lqBUsage); cmpResult != 0 {
				return cmpResult
			}
		}
		return baseCmp(a, b)
	}
}

func (c *ClusterQueue) addLocalQueue(lqKey utilqueue.LocalQueueReference) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.localQueuesInClusterQueue[lqKey] = true
}

func (c *ClusterQueue) deleteLocalQueue(lqKey utilqueue.LocalQueueReference) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	delete(c.localQueuesInClusterQueue, lqKey)
}
