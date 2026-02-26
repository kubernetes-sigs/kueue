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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	RequeueBatchPeriodProd = 1 * time.Second
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
func requeueWorkloadsCQ(ctx context.Context, m *Manager, clusterQueueName kueue.ClusterQueueReference) int {
	m.Lock()
	defer m.Unlock()
	cq := m.hm.ClusterQueue(clusterQueueName)
	if cq == nil {
		return 0
	}
	moved := queueInadmissibleWorkloads(ctx, cq, m.client)
	if moved > 0 {
		log := ctrl.LoggerFrom(ctx)
		log.V(2).Info("Moved workloads", "clusterqueue", cq.name, "count", moved)
		reportPendingWorkloads(m, cq.name)
		m.Broadcast()
	}
	return moved
}

// requeueWorkloadsCohort moves all inadmissible
// workloads in the Cohort tree to heap. It expects to be
// passed a root Cohort. If at least one workload queued,
// we will broadcast the event.
// WARNING: must only be called by the InadmissibleWorkloadRequeuer
func requeueWorkloadsCohort(ctx context.Context, m *Manager, rootCohortName kueue.CohortReference) int {
	m.Lock()
	defer m.Unlock()
	cohort := m.hm.Cohort(rootCohortName)
	if cohort == nil {
		return 0
	}
	log := ctrl.LoggerFrom(ctx)

	if hierarchy.HasCycle(cohort) {
		log.V(2).Info("Attempted to move workloads from Cohort which has cycle", "cohort", cohort.GetName())
		return 0
	}
	log.V(2).Info("Attempting to move workloads", "rootCohort", cohort.Name)
	moved := requeueWorkloadsCohortSubtree(ctx, m, cohort)
	if moved > 0 {
		log.V(2).Info("Moved inadmissible workloads in tree", "rootCohort", cohort.Name, "count", moved)
		m.Broadcast()
	}
	return moved
}

// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func requeueWorkloadsCohortSubtree(ctx context.Context, m *Manager, cohort *cohort) int {
	total := 0
	for _, clusterQueue := range cohort.ChildCQs() {
		if moved := queueInadmissibleWorkloads(ctx, clusterQueue, m.client); moved > 0 {
			reportPendingWorkloads(m, clusterQueue.name)
			total += moved
		}
	}
	for _, childCohort := range cohort.ChildCohorts() {
		total += requeueWorkloadsCohortSubtree(ctx, m, childCohort)
	}
	return total
}

// queueInadmissibleWorkloads moves all workloads from inadmissibleWorkloads to heap.
// Returns the number of workloads moved.
// WARNING: must only be called (indirectly) by InadmissibleWorkloadRequeuer.
func queueInadmissibleWorkloads(ctx context.Context, c *ClusterQueue, client client.Client) int {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	log := ctrl.LoggerFrom(ctx)
	c.queueInadmissibleCycle = c.popCycle
	if c.inadmissibleWorkloads.empty() {
		return 0
	}
	log.V(2).Info("Resetting the head of the ClusterQueue", "clusterQueue", c.name)
	newInadmissibleWorkloads := make(inadmissibleWorkloads)
	moved := 0
	for key, wInfo := range c.inadmissibleWorkloads {
		ns := corev1.Namespace{}
		err := client.Get(ctx, types.NamespacedName{Name: wInfo.Obj.Namespace}, &ns)
		if err != nil || !c.namespaceSelector.Matches(labels.Set(ns.Labels)) || !c.backoffWaitingTimeExpired(wInfo) {
			newInadmissibleWorkloads.insert(key, wInfo)
		} else if c.heap.PushIfNotPresent(wInfo) {
			moved++
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
		switch {
		case !cq.HasParent():
			m.requeuer.notifyClusterQueue(cq.name)
		case !hierarchy.HasCycle(cq.Parent()):
			rootName := cq.Parent().getRootUnsafe().GetName()
			m.requeuer.notifyCohort(rootName)
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
	// notifyCohort should only be called for Root Cohorts.
	notifyCohort(cohortName kueue.CohortReference)
	setManager(manager *Manager)
}

type requeueRequest struct {
	ClusterQueue kueue.ClusterQueueReference
	Cohort       kueue.CohortReference
}

// workqueueRequeuer satisfies the inadmissibleRequeuer
// interface, implemented via a workqueue.TypedDelayingQueue.
type workqueueRequeuer struct {
	manager     *Manager
	queue       workqueue.TypedDelayingInterface[requeueRequest]
	batchPeriod time.Duration
}

func NewRequeuer(batchPeriod time.Duration) *workqueueRequeuer {
	return &workqueueRequeuer{
		queue:       workqueue.NewTypedDelayingQueue[requeueRequest](),
		batchPeriod: batchPeriod,
	}
}

func (r *workqueueRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	r.queue.AddAfter(requeueRequest{ClusterQueue: cqName}, r.batchPeriod)
}

func (r *workqueueRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	r.queue.AddAfter(requeueRequest{Cohort: cohortName}, r.batchPeriod)
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
	if req.ClusterQueue != "" {
		requeueWorkloadsCQ(ctx, r.manager, req.ClusterQueue)
	}
	if req.Cohort != "" {
		requeueWorkloadsCohort(ctx, r.manager, req.Cohort)
	}
}
