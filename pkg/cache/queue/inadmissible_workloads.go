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
		log := ctrl.LoggerFrom(ctx)
		log.V(2).Info("Moved workloads", "clusterqueue", cq.name)
		reportPendingWorkloads(m, cq.name)
		m.Broadcast()
	}
}

// requeueWorkloadsCohort moves all inadmissible
// workloads in the Cohort tree to heap. It expects to be
// passed a root Cohort. If at least one workload queued,
// we will broadcast the event.
// WARNING: must only be called by the InadmissibleWorkloadRequeuer
func requeueWorkloadsCohort(ctx context.Context, m *Manager, rootCohortName kueue.CohortReference) {
	m.Lock()
	defer m.Unlock()
	cohort := m.hm.Cohort(rootCohortName)
	if cohort == nil {
		return
	}
	log := ctrl.LoggerFrom(ctx)

	if hierarchy.HasCycle(cohort) {
		log.V(2).Info("Attempted to move workloads from Cohort which has cycle", "cohort", cohort.GetName())
		return
	}
	log.V(2).Info("Attempting to move workloads", "rootCohort", cohort.Name)
	if requeueWorkloadsCohortSubtree(ctx, m, cohort) {
		log.V(2).Info("Moved all inadmissible workloads in tree", "rootCohort", cohort.Name)
		m.Broadcast()
	}
}

// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func requeueWorkloadsCohortSubtree(ctx context.Context, m *Manager, cohort *cohort) bool {
	queued := false
	for _, clusterQueue := range cohort.ChildCQs() {
		if queueInadmissibleWorkloads(ctx, clusterQueue, m.client) {
			reportPendingWorkloads(m, clusterQueue.name)
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
	if len(cqNames) == 0 {
		return
	}

	// Track processed cohort roots to avoid requeuing the same hierarchy
	// multiple times when multiple CQs in cqNames share a root.
	processedRoots := sets.New[kueue.CohortReference]()
	for name := range cqNames {
		cq := m.hm.ClusterQueue(name)
		if cq == nil {
			continue
		}
		if !cq.HasParent() {
			m.requeuer.notifyClusterQueue(cq.name)
		}
		if cq.HasParent() && !hierarchy.HasCycle(cq.Parent()) {
			rootName := cq.Parent().getRootUnsafe().GetName()
			if processedRoots.Has(rootName) {
				continue
			}
			m.requeuer.notifyCohort(rootName)
			processedRoots.Insert(rootName)
		}
	}
}

// inadmissibleRequeuer receives notifications
// that a particular ClusterQueue (without Cohort) or a
// Root Cohort should have its Inadmissible Workloads requeued.
type inadmissibleRequeuer interface {
	// notifyClusterQueue should only be called for ClusterQueues without a Cohort.
	notifyClusterQueue(cqName kueue.ClusterQueueReference)
	// notifyCohort should only be called for Root Cohorts.
	notifyCohort(cohortName kueue.CohortReference)
	setManager(manager *Manager)
}

type requeueRequest struct {
	ClusterQueue kueue.ClusterQueueReference
	Cohort       kueue.CohortReference
}

// controllerRequeuer satisfies the inadmissibleRequeuer
// interface, implemented via a controller-runtime
// controller.
type controllerRequeuer struct {
	manager     *Manager
	eventCh     chan event.TypedGenericEvent[requeueRequest]
	batchPeriod time.Duration
}

// newRequeuer is factory with production batch period.
func newRequeuer() *controllerRequeuer {
	return NewRequeuer(requeueBatchPeriodProd)
}

func NewRequeuer(batchPeriod time.Duration) *controllerRequeuer {
	return &controllerRequeuer{
		eventCh:     make(chan event.TypedGenericEvent[requeueRequest], 128),
		batchPeriod: batchPeriod,
	}
}

func (r *controllerRequeuer) Reconcile(ctx context.Context, req requeueRequest) (ctrl.Result, error) {
	if req.ClusterQueue != "" {
		requeueWorkloadsCQ(ctx, r.manager, req.ClusterQueue)
	}
	if req.Cohort != "" {
		requeueWorkloadsCohort(ctx, r.manager, req.Cohort)
	}
	return ctrl.Result{}, nil
}

func (r *controllerRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	r.eventCh <- event.TypedGenericEvent[requeueRequest]{Object: requeueRequest{ClusterQueue: cqName}}
}

func (r *controllerRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	r.eventCh <- event.TypedGenericEvent[requeueRequest]{Object: requeueRequest{Cohort: cohortName}}
}

func (r *controllerRequeuer) setManager(manager *Manager) {
	r.manager = manager
}

func (r *controllerRequeuer) Create(context.Context, event.TypedCreateEvent[requeueRequest], workqueue.TypedRateLimitingInterface[requeueRequest]) {
}
func (r *controllerRequeuer) Update(context.Context, event.TypedUpdateEvent[requeueRequest], workqueue.TypedRateLimitingInterface[requeueRequest]) {
}
func (r *controllerRequeuer) Delete(context.Context, event.TypedDeleteEvent[requeueRequest], workqueue.TypedRateLimitingInterface[requeueRequest]) {
}
func (r *controllerRequeuer) Generic(_ context.Context, e event.TypedGenericEvent[requeueRequest], q workqueue.TypedRateLimitingInterface[requeueRequest]) {
	q.AddAfter(e.Object, r.batchPeriod)
}

func (r *controllerRequeuer) setupWithManager(mgr ctrl.Manager) error {
	return builder.TypedControllerManagedBy[requeueRequest](mgr).
		Named("inadmissible_workload_requeue_controller").
		WatchesRawSource(source.TypedChannel(r.eventCh, r)).
		WithOptions(controller.TypedOptions[requeueRequest]{
			NeedLeaderElection: ptr.To(false),
			// since a lock is required to requeue, no point in more than 1.
			MaxConcurrentReconciles: 1,
		}).
		Complete(r)
}
