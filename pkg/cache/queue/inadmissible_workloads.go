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
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	// Namespace cache misses should retry without continuously scanning
	// inadmissible Workloads during informer propagation delays.
	namespaceLookupRetryBaseDelay = 5 * time.Second
	namespaceLookupRetryMaxDelay  = 2 * time.Minute
)

type requeuerOptions struct {
	batchPeriod time.Duration
	clock       clock.WithTicker
}

type RequeuerOption func(*requeuerOptions)

func WithInadmissibleWorkloadRequeuerConfig(cfg *config.InadmissibleWorkloadRequeuer) RequeuerOption {
	return func(o *requeuerOptions) {
		if !features.Enabled(features.ConfigurableInadmissibleWorkloadRequeuer) || cfg == nil || cfg.BatchPeriod == nil {
			return
		}
		o.batchPeriod = cfg.BatchPeriod.Duration
	}
}

func WithRequeuerClock(clock clock.WithTicker) RequeuerOption {
	return func(o *requeuerOptions) {
		o.clock = clock
	}
}

// inadmissibleWorkloads is a thin wrapper around a map to encapsulate
// operations on inadmissible workloads and prevent direct map access.
type inadmissibleWorkloads map[workload.Reference]*workload.Info

// get retrieves a workload from the inadmissible workloads map.
// Returns the workload if it exists, otherwise returns nil.
func (iw inadmissibleWorkloads) get(key workload.Reference) *workload.Info {
	return iw[key]
}

// delete removes a workload from the inadmissible workloads map.
func (iw inadmissibleWorkloads) delete(key workload.Reference) {
	delete(iw, key)
}

// insert adds a workload to the inadmissible workloads map.
func (iw inadmissibleWorkloads) insert(key workload.Reference, wInfo *workload.Info) {
	iw[key] = wInfo
}

// len returns the number of inadmissible workloads.
func (iw inadmissibleWorkloads) len() int {
	return len(iw)
}

// empty returns true if there are no inadmissible workloads.
func (iw inadmissibleWorkloads) empty() bool {
	return len(iw) == 0
}

// hasKey returns true if the workload exists in the inadmissible workloads map.
func (iw inadmissibleWorkloads) hasKey(key workload.Reference) bool {
	_, ok := iw[key]
	return ok
}

// requeueWorkloadsCQ moves all workloads in this ClusterQueue
// from inadmissibleWorkloads to heap.
// It expects to be passed a ClusterQueue without any Cohort.
// WARNING: must only be called by the InadmissibleWorkloadRequeuer
func requeueWorkloadsCQ(ctx context.Context, m *Manager, clusterQueueName kueue.ClusterQueueReference) int {
	moved, _ := requeueWorkloadsCQWithRetry(ctx, m, clusterQueueName)
	return moved
}

func requeueWorkloadsCQWithRetry(ctx context.Context, m *Manager, clusterQueueName kueue.ClusterQueueReference) (int, bool) {
	m.Lock()
	defer m.Unlock()
	cq := m.hm.ClusterQueue(clusterQueueName)
	if cq == nil {
		return 0, false
	}
	moved, retry := queueInadmissibleWorkloadsWithRetry(ctx, cq, m.client)
	if moved > 0 {
		log := ctrl.LoggerFrom(ctx)
		log.V(3).Info("Moved workloads", "clusterQueue", klog.KRef("", string(cq.name)), "count", moved)
		reportPendingWorkloads(m, cq.name)
		m.Broadcast()
	}
	return moved, retry
}

// requeueWorkloadsCohort moves all inadmissible
// workloads in the Cohort tree to heap. It expects to be
// passed a root Cohort. If at least one workload queued,
// we will broadcast the event.
// WARNING: must only be called by the InadmissibleWorkloadRequeuer
func requeueWorkloadsCohort(ctx context.Context, m *Manager, rootCohortName kueue.CohortReference) int {
	moved, _ := requeueWorkloadsCohortWithRetry(ctx, m, rootCohortName)
	return moved
}

func requeueWorkloadsCohortWithRetry(ctx context.Context, m *Manager, rootCohortName kueue.CohortReference) (int, bool) {
	m.Lock()
	defer m.Unlock()
	cohort := m.hm.Cohort(rootCohortName)
	if cohort == nil {
		return 0, false
	}
	log := ctrl.LoggerFrom(ctx)
	if hierarchy.HasCycle(cohort) {
		log.V(3).Info("Attempted to move workloads from Cohort which has cycle", "cohort", klog.KRef("", string(cohort.GetName())))
		return 0, false
	}
	log.V(4).Info("Attempting to move workloads", "rootCohort", klog.KRef("", string(cohort.Name)))
	moved, retry := requeueWorkloadsCohortSubtreeWithRetry(ctx, m, cohort)
	if moved > 0 {
		log.V(3).Info("Moved inadmissible workloads in tree", "rootCohort", klog.KRef("", string(cohort.Name)), "count", moved)
		m.Broadcast()
	}
	return moved, retry
}

// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func requeueWorkloadsCohortSubtreeWithRetry(ctx context.Context, m *Manager, cohort *cohort) (int, bool) {
	total := 0
	retry := false
	for _, clusterQueue := range cohort.ChildCQs() {
		moved, shouldRetry := queueInadmissibleWorkloadsWithRetry(ctx, clusterQueue, m.client)
		if moved > 0 {
			reportPendingWorkloads(m, clusterQueue.name)
			total += moved
		}
		retry = retry || shouldRetry
	}
	for _, childCohort := range cohort.ChildCohorts() {
		moved, shouldRetry := requeueWorkloadsCohortSubtreeWithRetry(ctx, m, childCohort)
		total += moved
		retry = retry || shouldRetry
	}
	return total, retry
}

// queueInadmissibleWorkloads moves all (eligible) workloads from inadmissibleWorkloads to heap.
// Returns the number of workloads moved.
// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func queueInadmissibleWorkloads(ctx context.Context, c *ClusterQueue, k8sClient client.Client) int {
	moved, _ := queueInadmissibleWorkloadsWithRetry(ctx, c, k8sClient)
	return moved
}

// Track Namespace read errors so the worker does not lose its only retry signal.
func queueInadmissibleWorkloadsWithRetry(ctx context.Context, c *ClusterQueue, k8sClient client.Client) (int, bool) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	log := ctrl.LoggerFrom(ctx)
	c.queueInadmissibleCycle = c.popCycle
	// Clear bulk-move scheduling hashes so re-queued workloads are re-evaluated fresh.
	c.hashToBulkMoveReason = make(map[workload.EquivalenceHash]QuotaReservedReason)
	if c.workloads.inadmissible.empty() {
		return 0, false
	}
	log.V(5).Info("Resetting the head of the ClusterQueue", "clusterQueue", klog.KRef("", string(c.name)))
	moved := 0
	retry := false
	for key, wInfo := range c.workloads.inadmissible {
		// A Workload still in backoff is ineligible regardless of its Namespace.
		if !c.backoffWaitingTimeExpired(wInfo) {
			continue
		}
		ns := corev1.Namespace{}
		err := k8sClient.Get(ctx, types.NamespacedName{Name: wInfo.Obj.Namespace}, &ns)
		if err != nil {
			// A Namespace can be missing from the informer cache even when its
			// Workload is present. Retry NotFound along with other read errors.
			if ctx.Err() == nil {
				retry = true
			}
			continue
		}
		if !c.namespaceSelector.Matches(labels.Set(ns.Labels)) {
			continue
		}
		if c.workloads.MoveToActive(key, wInfo) {
			moved++
		}
	}
	log.V(5).Info("Moved workloads from inadmissibleWorkloads back to heap",
		"clusterQueue", klog.KRef("", string(c.name)),
		"workloadsMoved", moved,
		"workloadsNotMoved", c.workloads.inadmissible.len())
	return moved, retry
}

// NotifyRetryInadmissible requests that inadmissible workloads
// from given ClusterQueues, and from all ClusterQueues in these
// ClusterQueues' Cohort Trees, are moved from
// inadmissibleQueue to the active workload heap.
func NotifyRetryInadmissible(m *Manager, cqNames sets.Set[kueue.ClusterQueueReference]) {
	m.RLock()
	defer m.RUnlock()
	notifyRetryInadmissibleWithoutLock(m, cqNames)
}

// NotifyRetryInadmissibleAfter is like NotifyRetryInadmissible, but waits at
// least delay. The requeuer's batch period is the minimum wait.
func NotifyRetryInadmissibleAfter(m *Manager, cqNames sets.Set[kueue.ClusterQueueReference], delay time.Duration) {
	m.RLock()
	defer m.RUnlock()
	notifyRetryInadmissibleWithDelayWithoutLock(m, cqNames, &delay)
}

func notifyRetryInadmissibleWithoutLock(m *Manager, cqNames sets.Set[kueue.ClusterQueueReference]) {
	notifyRetryInadmissibleWithDelayWithoutLock(m, cqNames, nil)
}

func notifyRetryInadmissibleWithDelayWithoutLock(m *Manager, cqNames sets.Set[kueue.ClusterQueueReference], delay *time.Duration) {
	for name := range cqNames {
		cq := m.hm.ClusterQueue(name)
		if cq == nil {
			continue
		}
		switch {
		case !cq.HasParent():
			if delay == nil {
				m.requeuer.notifyClusterQueue(cq.name)
			} else {
				m.requeuer.notifyClusterQueueAfter(cq.name, *delay)
			}
		case !hierarchy.HasCycle(cq.Parent()):
			rootName := cq.Parent().getRootUnsafe().GetName()
			if delay == nil {
				m.requeuer.notifyCohort(rootName)
			} else {
				m.requeuer.notifyCohortAfter(rootName, *delay)
			}
		}
		// We silently ignore Cohort trees with cycles.
		// Once the cycle is removed, we will reconcile
		// and process the entire tree(s).
	}
}

// inadmissibleRequeuer receives notifications
// that a particular ClusterQueue (without Cohort) or a
// Root Cohort should have its Inadmissible Workloads requeued.
type inadmissibleRequeuer interface {
	// notifyClusterQueue should only be called for ClusterQueues without a Cohort.
	notifyClusterQueue(cqName kueue.ClusterQueueReference)
	// notifyClusterQueueAfter should only be called for ClusterQueues without a Cohort.
	notifyClusterQueueAfter(cqName kueue.ClusterQueueReference, delay time.Duration)
	// notifyCohort should only be called for Root Cohorts.
	notifyCohort(cohortName kueue.CohortReference)
	// notifyCohortAfter should only be called for Root Cohorts.
	notifyCohortAfter(cohortName kueue.CohortReference, delay time.Duration)
	setManager(manager *Manager)
}

type requeueRequest struct {
	ClusterQueue kueue.ClusterQueueReference
	Cohort       kueue.CohortReference
}

// workqueueRequeuer satisfies the inadmissibleRequeuer interface.
// It batches notifications and rate-limits failed Namespace lookups.
type workqueueRequeuer struct {
	manager     *Manager
	queue       workqueue.TypedRateLimitingInterface[requeueRequest]
	batchPeriod time.Duration
}

func NewRequeuer(opts ...RequeuerOption) *workqueueRequeuer {
	options := requeuerOptions{
		batchPeriod: config.DefaultInadmissibleWorkloadRequeuerBatchPeriod,
	}
	for _, opt := range opts {
		opt(&options)
	}
	return &workqueueRequeuer{
		queue: workqueue.NewTypedRateLimitingQueueWithConfig(
			workqueue.NewTypedItemExponentialFailureRateLimiter[requeueRequest](namespaceLookupRetryBaseDelay, namespaceLookupRetryMaxDelay),
			workqueue.TypedRateLimitingQueueConfig[requeueRequest]{Clock: options.clock},
		),
		batchPeriod: options.batchPeriod,
	}
}

func (r *workqueueRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	r.queue.AddAfter(requeueRequest{ClusterQueue: cqName}, r.batchPeriod)
}

// notifyClusterQueueAfter waits at least delay, using batchPeriod as the minimum delay.
func (r *workqueueRequeuer) notifyClusterQueueAfter(cqName kueue.ClusterQueueReference, delay time.Duration) {
	r.queue.AddAfter(requeueRequest{ClusterQueue: cqName}, max(delay, r.batchPeriod))
}

func (r *workqueueRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	r.queue.AddAfter(requeueRequest{Cohort: cohortName}, r.batchPeriod)
}

// notifyCohortAfter waits at least delay, using batchPeriod as the minimum delay.
func (r *workqueueRequeuer) notifyCohortAfter(cohortName kueue.CohortReference, delay time.Duration) {
	r.queue.AddAfter(requeueRequest{Cohort: cohortName}, max(delay, r.batchPeriod))
}

func (r *workqueueRequeuer) setManager(manager *Manager) {
	r.manager = manager
}

func (r *workqueueRequeuer) Start(ctx context.Context) error {
	log := ctrl.LoggerFrom(ctx).WithName("inadmissible_workload_requeue_worker")
	ctx = ctrl.LoggerInto(ctx, log)
	go func() {
		<-ctx.Done()
		r.queue.ShutDown()
	}()
	for {
		item, shutdown := r.queue.Get()
		if shutdown {
			return nil
		}
		r.reconcile(ctx, item)
		r.queue.Done(item)
	}
}

func (r *workqueueRequeuer) reconcile(ctx context.Context, req requeueRequest) {
	retry := false
	if req.ClusterQueue != "" {
		_, retry = requeueWorkloadsCQWithRetry(ctx, r.manager, req.ClusterQueue)
	}
	if req.Cohort != "" {
		_, cohortRetry := requeueWorkloadsCohortWithRetry(ctx, r.manager, req.Cohort)
		retry = retry || cohortRetry
	}
	if retry && ctx.Err() == nil {
		r.queue.AddRateLimited(req)
	} else {
		r.queue.Forget(req)
	}
}
