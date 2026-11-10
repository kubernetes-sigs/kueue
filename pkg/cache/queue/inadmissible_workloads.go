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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

const requeueBatchPeriod = time.Second

type requeuerOptions struct {
	batchPeriod time.Duration
}

type RequeuerOption func(*requeuerOptions)

func WithBatchPeriod(period time.Duration) RequeuerOption {
	return func(o *requeuerOptions) {
		o.batchPeriod = period
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

// requeueWorkloadsCQ moves inadmissible workloads from a ClusterQueue without a
// Cohort to the active heap. Tests also call this helper synchronously.
func requeueWorkloadsCQ(ctx context.Context, m *Manager, clusterQueueName kueue.ClusterQueueReference) int {
	moved, _ := requeueWorkloadsCQWithRetry(ctx, m, clusterQueueName)
	return moved
}

// requeueWorkloadsCQWithRetry returns whether any Namespace read failed
// transiently (or a new Namespace appeared after the initial snapshot).
func requeueWorkloadsCQWithRetry(ctx context.Context, m *Manager, clusterQueueName kueue.ClusterQueueReference) (int, bool) {
	// Snapshot the needed Namespaces under a read lock, then perform client
	// lookups outside m.Lock. Holding the write lock over API/cache reads
	// stalls unrelated queue operations for the duration of the batch.
	m.RLock()
	cq := m.hm.ClusterQueue(clusterQueueName)
	if cq == nil {
		m.RUnlock()
		return 0, false
	}
	namespaces := sets.New[string]()
	collectInadmissibleNamespaces(cq, namespaces)
	m.RUnlock()

	resolved := readNamespaces(ctx, m.client, namespaces)

	m.Lock()
	defer m.Unlock()
	// A ClusterQueue may have been deleted/recreated during the reads.
	cq = m.hm.ClusterQueue(clusterQueueName)
	if cq == nil {
		return 0, false
	}
	moved, retry := moveInadmissibleWorkloads(ctx, cq, resolved)
	if moved > 0 {
		log := ctrl.LoggerFrom(ctx)
		log.V(2).Info("Moved workloads", "clusterQueue", cq.name, "count", moved)
		reportPendingWorkloads(m, cq.name)
		m.Broadcast()
	}
	return moved, retry
}

// requeueWorkloadsCohort moves inadmissible workloads in a Cohort tree.
func requeueWorkloadsCohort(ctx context.Context, m *Manager, rootCohortName kueue.CohortReference) int {
	moved, _ := requeueWorkloadsCohortWithRetry(ctx, m, rootCohortName)
	return moved
}

func requeueWorkloadsCohortWithRetry(ctx context.Context, m *Manager, rootCohortName kueue.CohortReference) (int, bool) {
	m.RLock()
	cohort := m.hm.Cohort(rootCohortName)
	if cohort == nil {
		m.RUnlock()
		return 0, false
	}
	if hierarchy.HasCycle(cohort) {
		m.RUnlock()
		ctrl.LoggerFrom(ctx).V(2).Info("Attempted to move workloads from Cohort which has cycle", "cohort", rootCohortName)
		return 0, false
	}
	namespaces := sets.New[string]()
	collectCohortNamespaces(cohort, namespaces)
	m.RUnlock()

	resolved := readNamespaces(ctx, m.client, namespaces)

	m.Lock()
	defer m.Unlock()
	// Recheck the live topology: the Cohort or its children can change
	// while Namespace lookups are in flight.
	cohort = m.hm.Cohort(rootCohortName)
	if cohort == nil || hierarchy.HasCycle(cohort) {
		return 0, false
	}
	log := ctrl.LoggerFrom(ctx)
	log.V(2).Info("Attempting to move workloads", "rootCohort", cohort.Name)
	moved, retry := requeueWorkloadsCohortSubtree(ctx, m, cohort, resolved)
	if moved > 0 {
		log.V(2).Info("Moved inadmissible workloads in tree", "rootCohort", cohort.Name, "count", moved)
		m.Broadcast()
	}
	return moved, retry
}

// Caller holds the manager lock.
func requeueWorkloadsCohortSubtree(ctx context.Context, m *Manager, cohort *cohort, resolved map[string]namespaceRead) (int, bool) {
	total := 0
	retry := false
	for _, cq := range cohort.ChildCQs() {
		moved, shouldRetry := moveInadmissibleWorkloads(ctx, cq, resolved)
		if moved > 0 {
			reportPendingWorkloads(m, cq.name)
			total += moved
		}
		retry = retry || shouldRetry
	}
	for _, child := range cohort.ChildCohorts() {
		moved, shouldRetry := requeueWorkloadsCohortSubtree(ctx, m, child, resolved)
		total += moved
		retry = retry || shouldRetry
	}
	return total, retry
}

type namespaceRead struct {
	labels labels.Set
	found  bool
	retry  bool
}

// collectInadmissibleNamespaces is called under m.RLock (or c.rwm alone
// for direct queue tests), preserving the same lock order as queue mutation.
func collectInadmissibleNamespaces(c *ClusterQueue, names sets.Set[string]) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	for _, info := range c.workloads.inadmissible {
		names.Insert(info.Obj.Namespace)
	}
}

// Caller holds m.RLock. A snapshot of all distinct Namespace names is
// shared across the entire Cohort so one Namespace incurs only one Get.
func collectCohortNamespaces(cohort *cohort, names sets.Set[string]) {
	for _, cq := range cohort.ChildCQs() {
		collectInadmissibleNamespaces(cq, names)
	}
	for _, child := range cohort.ChildCohorts() {
		collectCohortNamespaces(child, names)
	}
}

// readNamespaces must run without the queue-manager lock. NotFound is not
// retried: a deleted Namespace cannot become eligible without a new event.
func readNamespaces(ctx context.Context, k8sClient client.Client, names sets.Set[string]) map[string]namespaceRead {
	resolved := make(map[string]namespaceRead, names.Len())
	log := ctrl.LoggerFrom(ctx)
	for name := range names {
		var ns corev1.Namespace
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name}, &ns)
		switch {
		case err == nil:
			resolved[name] = namespaceRead{labels: labels.Set(ns.Labels), found: true}
		case apierrors.IsNotFound(err):
			resolved[name] = namespaceRead{}
		default:
			log.Error(err, "Failed to read Namespace while retrying inadmissible workloads", "namespace", name)
			resolved[name] = namespaceRead{retry: true}
		}
	}
	return resolved
}

// queueInadmissibleWorkloads is used by direct ClusterQueue tests. The
// production CQ/Cohort paths prefetch all Namespace reads once per batch.
func queueInadmissibleWorkloads(ctx context.Context, c *ClusterQueue, k8sClient client.Client) int {
	namespaces := sets.New[string]()
	collectInadmissibleNamespaces(c, namespaces)
	moved, _ := moveInadmissibleWorkloads(ctx, c, readNamespaces(ctx, k8sClient, namespaces))
	return moved
}

// moveInadmissibleWorkloads revalidates *current* Workload membership,
// Namespace selector and backoff after reads completed outside the manager
// lock. A Namespace absent from the snapshot means an inadmissible Workload
// arrived in the read/commit window; request another pass rather than losing it.
// Caller holds m.Lock in production; c.rwm protects the per-queue state.
func moveInadmissibleWorkloads(ctx context.Context, c *ClusterQueue, resolved map[string]namespaceRead) (int, bool) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	log := ctrl.LoggerFrom(ctx)
	c.queueInadmissibleCycle = c.popCycle
	// Preserve the original behavior for SchedulingEquivalenceHashing.
	c.hashToBulkMoveReason = make(map[workload.EquivalenceHash]QuotaReservedReason)
	if c.workloads.inadmissible.empty() {
		return 0, false
	}
	log.V(2).Info("Resetting the head of the ClusterQueue", "clusterQueue", c.name)
	moved := 0
	retry := false
	for key, info := range c.workloads.inadmissible {
		ns, found := resolved[info.Obj.Namespace]
		if !found || ns.retry {
			retry = true
			continue
		}
		if !ns.found || !c.namespaceSelector.Matches(ns.labels) || !c.backoffWaitingTimeExpired(info) {
			continue
		}
		if c.workloads.MoveToActive(key, info) {
			moved++
		}
	}
	log.V(5).Info("Moved workloads from inadmissibleWorkloads back to heap", "clusterQueue", c.name, "workloadsMoved", moved, "workloadsNotMoved", c.workloads.inadmissible.len())
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
// interface, with delayed batching and rate-limited retries.
type workqueueRequeuer struct {
	manager     *Manager
	queue       workqueue.TypedRateLimitingInterface[requeueRequest]
	batchPeriod time.Duration
}

func NewRequeuer(opts ...RequeuerOption) *workqueueRequeuer {
	options := requeuerOptions{
		batchPeriod: requeueBatchPeriod,
	}
	for _, opt := range opts {
		opt(&options)
	}
	return &workqueueRequeuer{
		queue: workqueue.NewTypedRateLimitingQueue(
			workqueue.NewTypedItemExponentialFailureRateLimiter[requeueRequest](5*time.Second, 2*time.Minute),
		),
		batchPeriod: options.batchPeriod,
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
	retry := false
	if req.ClusterQueue != "" {
		_, retry = requeueWorkloadsCQWithRetry(ctx, r.manager, req.ClusterQueue)
	}
	if req.Cohort != "" {
		_, cohortRetry := requeueWorkloadsCohortWithRetry(ctx, r.manager, req.Cohort)
		retry = retry || cohortRetry
	}
	if retry && ctx.Err() == nil {
		// Exponential backoff protects the API/cache on prolonged failures.
		r.queue.AddRateLimited(req)
	} else {
		r.queue.Forget(req)
	}
}
