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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/source"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	requeueBatchPeriodProd = 1 * time.Second
)

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

// replaceAll replaces all inadmissible workloads with the provided map.
func (iw *inadmissibleWorkloads) replaceAll(newMap inadmissibleWorkloads) {
	*iw = newMap
}

// requeueWorkloadsCQ moves all workloads in the same
// cohort with this ClusterQueue from inadmissibleWorkloads to heap.
// It expects to be passed a ClusterQueue without any Cohort.
// WARNING: must only be called by the InadmissibleWorkloadRequeuer
func requeueWorkloadsCQ(ctx context.Context, m *Manager, clusterQueueName kueue.ClusterQueueReference) {
	m.Lock()
	defer m.Unlock()
	cq := m.hm.ClusterQueue(clusterQueueName)
	if cq == nil {
		return
	}
	if queueInadmissibleWorkloads(ctx, cq, m.client) {
		reportMetrics(m, cq.name)
		m.Broadcast()
	}
}

// requeueWorkloadsCohort moves all inadmissible
// workloads in the Cohort tree. It expects to be
// passed a root Cohort.
//
// RequeueCohort moves all inadmissibleWorkloads in
// corresponding Cohort to heap. If at least one workload queued,
// we will broadcast the event.
// WARNING: must only be called by the InadmissibleWorkloadRequeuer
func requeueWorkloadsCohort(ctx context.Context, m *Manager, cohortName kueue.CohortReference) {
	m.Lock()
	defer m.Unlock()
	cohort := m.hm.Cohort(cohortName)
	if cohort == nil {
		return
	}
	log := ctrl.LoggerFrom(ctx)

	if hierarchy.HasCycle(cohort) {
		log.V(2).Info("Attempted to move workloads from Cohort which has cycle", "cohort", cohort.GetName())
		return
	}
	log.V(2).Info("Attempting to move workloads", "cohort", cohort.Name)
	if requeueWorkloadsCohortSubtree(ctx, m, cohort) {
		m.Broadcast()
	}
}

// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func requeueWorkloadsCohortSubtree(ctx context.Context, m *Manager, cohort *cohort) bool {
	queued := false
	for _, clusterQueue := range cohort.ChildCQs() {
		if queueInadmissibleWorkloads(ctx, clusterQueue, m.client) {
			reportMetrics(m, clusterQueue.name)
			queued = true
		}
	}
	for _, childCohort := range cohort.ChildCohorts() {
		queued = requeueWorkloadsCohortSubtree(ctx, m, childCohort) || queued
	}
	return queued
}

// queueInadmissibleWorkloads moves all workloads from inadmissibleWorkloads to heap.
// If at least one workload is moved, returns true, otherwise returns false.
// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func queueInadmissibleWorkloads(ctx context.Context, c *ClusterQueue, client client.Client) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	log := ctrl.LoggerFrom(ctx)
	c.queueInadmissibleCycle = c.popCycle
	if c.inadmissibleWorkloads.empty() {
		return false
	}
	log.V(2).Info("Resetting the head of the ClusterQueue", "clusterQueue", c.name)
	newInadmissibleWorkloads := make(inadmissibleWorkloads)
	moved := false
	for key, wInfo := range c.inadmissibleWorkloads {
		ns := corev1.Namespace{}
		err := client.Get(ctx, types.NamespacedName{Name: wInfo.Obj.Namespace}, &ns)
		if err != nil || !c.namespaceSelector.Matches(labels.Set(ns.Labels)) || !c.backoffWaitingTimeExpired(wInfo) {
			newInadmissibleWorkloads.insert(key, wInfo)
		} else {
			moved = c.heap.PushIfNotPresent(wInfo) || moved
		}
	}

	c.inadmissibleWorkloads.replaceAll(newInadmissibleWorkloads)
	log.V(5).Info("Moved all workloads from inadmissibleWorkloads back to heap", "clusterQueue", c.name)
	return moved
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

func notifyRetryInadmissibleWithoutLock(m *Manager, cqNames sets.Set[kueue.ClusterQueueReference]) {
	for name := range cqNames {
		cq := m.hm.ClusterQueue(name)
		if cq == nil {
			continue
		}
		if !cq.HasParent() {
			m.inadmissibleWorkloadRequeuer.notifyClusterQueue(cq.name)
		} else if !hierarchy.HasCycle(cq.Parent()) {
			root := cq.Parent().getRootUnsafe().GetName()
			// unnecessary to deduplicate root Cohorts, as notifyCohort handles this
			m.inadmissibleWorkloadRequeuer.notifyCohort(root)
		}
	}
}

// requeueInadmissibleListener receives notifications
// that a particular ClusterQueue (without Cohort) or a
// Root Cohort should have its Inadmissible Workloads requeued.
type requeueInadmissibleListener interface {
	// notifyClusterQueue should only be called for ClusterQueues without a Cohort.
	notifyClusterQueue(cqName kueue.ClusterQueueReference)
	// notifyCohort should only be called for Root Cohorts.
	notifyCohort(cohortName kueue.CohortReference)
}

type requeueRequest struct {
	ClusterQueue kueue.ClusterQueueReference
	Cohort       kueue.CohortReference
}

// inadmissibleWorkloadRequeuer is responsible for receiving notifications,
// and requeuering workloads as a result of these notifications.
type inadmissibleWorkloadRequeuer struct {
	qManager    *Manager
	eventCh     chan event.TypedGenericEvent[requeueRequest]
	batchPeriod time.Duration
}

func newInadmissibleWorkloadReconciler(qManager *Manager) *inadmissibleWorkloadRequeuer {
	return &inadmissibleWorkloadRequeuer{
		qManager: qManager,
		// note to reviewers: should this be a buffered channel? I imagine that
		// q.AddAfter will process this so fast that it is not necessary.
		// LLM review suggested this to derisk deadlock (during startup?), but I don't
		// see this risk.
		eventCh:     make(chan event.TypedGenericEvent[requeueRequest]),
		batchPeriod: requeueBatchPeriodProd,
	}
}

func (r *inadmissibleWorkloadRequeuer) Reconcile(ctx context.Context, req requeueRequest) (ctrl.Result, error) {
	if req.ClusterQueue != "" {
		requeueWorkloadsCQ(ctx, r.qManager, req.ClusterQueue)
	}
	if req.Cohort != "" {
		requeueWorkloadsCohort(ctx, r.qManager, req.Cohort)
	}
	return ctrl.Result{}, nil
}

func (r *inadmissibleWorkloadRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	r.eventCh <- event.TypedGenericEvent[requeueRequest]{Object: requeueRequest{ClusterQueue: cqName}}
}

func (r *inadmissibleWorkloadRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	r.eventCh <- event.TypedGenericEvent[requeueRequest]{Object: requeueRequest{Cohort: cohortName}}
}

func (r *inadmissibleWorkloadRequeuer) Create(context.Context, event.TypedCreateEvent[requeueRequest], workqueue.TypedRateLimitingInterface[requeueRequest]) {
}
func (r *inadmissibleWorkloadRequeuer) Update(context.Context, event.TypedUpdateEvent[requeueRequest], workqueue.TypedRateLimitingInterface[requeueRequest]) {
}
func (r *inadmissibleWorkloadRequeuer) Delete(context.Context, event.TypedDeleteEvent[requeueRequest], workqueue.TypedRateLimitingInterface[requeueRequest]) {
}
func (r *inadmissibleWorkloadRequeuer) Generic(_ context.Context, e event.TypedGenericEvent[requeueRequest], q workqueue.TypedRateLimitingInterface[requeueRequest]) {
	q.AddAfter(e.Object, r.batchPeriod)
}

func (r *inadmissibleWorkloadRequeuer) setupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[requeueRequest](mgr).
		Named("inadmissible_workload_requeue_controller").
		WatchesRawSource(source.TypedChannel(r.eventCh, &inadmissibleWorkloadRequeuer{})).
		WithOptions(controller.TypedOptions[requeueRequest]{
			NeedLeaderElection: ptr.To(false),
			// since a lock is required to requeue, no point in more than 1.
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}
