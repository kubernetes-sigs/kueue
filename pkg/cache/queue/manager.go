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
	"errors"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	queueafs "sigs.k8s.io/kueue/pkg/cache/queue/afs"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ErrLocalQueueDoesNotExistOrInactive = errors.New("localQueue doesn't exist or inactive")
	ErrClusterQueueDoesNotExist         = errors.New("clusterQueue doesn't exist")
	errClusterQueueAlreadyExists        = errors.New("clusterQueue already exists")
	errWorkloadIsInadmissible           = errors.New("workload is inadmissible and can't be added to a LocalQueue")
)

// Option configures the manager.
type Option func(*Manager)

// WithClock allows to specify a custom clock
func WithClock(c clock.WithDelayedExecution) Option {
	return func(m *Manager) {
		m.clock = c
	}
}

func WithAdmissionFairSharing(cfg *config.AdmissionFairSharing) Option {
	return func(m *Manager) {
		m.admissionFairSharingConfig = cfg
	}
}

// WithPodsReadyRequeuingTimestamp sets the timestamp that is used for ordering
// workloads that have been requeued due to the PodsReady condition.
func WithPodsReadyRequeuingTimestamp(ts config.RequeuingTimestamp) Option {
	return func(m *Manager) {
		m.workloadOrdering.PodsReadyRequeuingTimestamp = ts
	}
}

// WithExcludedResourcePrefixes sets the list of excluded resource prefixes
func WithExcludedResourcePrefixes(excludedPrefixes []string) Option {
	return func(m *Manager) {
		m.workloadInfoOptions = append(m.workloadInfoOptions, workload.WithExcludedResourcePrefixes(excludedPrefixes))
	}
}

// WithResourceTransformations sets the resource transformations.
func WithResourceTransformations(transforms []config.ResourceTransformation) Option {
	return func(m *Manager) {
		m.workloadInfoOptions = append(m.workloadInfoOptions, workload.WithResourceTransformations(transforms))
	}
}

// WithRoleTracker sets the roleTracker for HA metrics.
func WithRoleTracker(tracker *roletracker.RoleTracker) Option {
	return func(m *Manager) {
		m.roleTracker = tracker
	}
}

// SetDRAReconcileChannel sets the DRA reconcile channel after manager creation.
func (m *Manager) SetDRAReconcileChannel(ch chan<- event.TypedGenericEvent[*kueue.Workload]) {
	m.draReconcileChannel = ch
	if ch != nil {
		ctrl.Log.WithName("queue-manager").Info("DRA reconcile channel connected")
	}
}

type TopologyUpdateWatcher interface {
	NotifyTopologyUpdate(oldTopology, newTopology *kueue.Topology)
}

type Manager struct {
	sync.RWMutex
	cond sync.Cond

	clock         clock.WithDelayedExecution
	client        client.Client
	statusChecker StatusChecker
	localQueues   map[queue.LocalQueueReference]*LocalQueue
	// Tracks Workload's LocalQueue assignment throughout its whole lifetime (including running and finished).
	workloadAssignedQueues map[workload.Reference]queue.LocalQueueReference
	finishedWorkloads      map[workload.Reference]queue.LocalQueueReference

	workloadOrdering workload.Ordering

	workloadInfoOptions []workload.InfoOption

	hm hierarchy.Manager[*ClusterQueue, *cohort]

	topologyUpdateWatchers []TopologyUpdateWatcher

	admissionFairSharingConfig *config.AdmissionFairSharing
	secondPassQueue            *secondPassQueue

	AfsEntryPenalties      *queueafs.AfsEntryPenalties
	AfsConsumedResources   *queueafs.AfsConsumedResources
	workloadUpdateWatchers []WorkloadUpdateWatcher

	draReconcileChannel chan<- event.TypedGenericEvent[*kueue.Workload]

	roleTracker *roletracker.RoleTracker
}

// NewManager is a factory for cache.queue.Manager. For tests,
// NewManagerForUnitTests or NewManagerForIntegrationTests should be
// used.
func NewManager(client client.Client, checker StatusChecker, options ...Option) *Manager {
	m := &Manager{
		clock:                  realClock,
		client:                 client,
		statusChecker:          checker,
		localQueues:            make(map[queue.LocalQueueReference]*LocalQueue),
		workloadAssignedQueues: make(map[workload.Reference]queue.LocalQueueReference),
		finishedWorkloads:      make(map[workload.Reference]queue.LocalQueueReference),
		workloadOrdering: workload.Ordering{
			PodsReadyRequeuingTimestamp: config.EvictionTimestamp,
		},
		workloadInfoOptions: []workload.InfoOption{},
		hm:                  hierarchy.NewManager(newCohort),

		topologyUpdateWatchers: make([]TopologyUpdateWatcher, 0),
		secondPassQueue:        newSecondPassQueue(),
		AfsEntryPenalties:      queueafs.NewPenaltyMap(),
		AfsConsumedResources:   queueafs.NewAfsConsumedResources(),
	}
	for _, option := range options {
		option(m)
	}
	m.cond.L = &m.RWMutex
	return m
}

func (m *Manager) AddTopologyUpdateWatcher(watcher TopologyUpdateWatcher) {
	m.topologyUpdateWatchers = append(m.topologyUpdateWatchers, watcher)
}

func (m *Manager) NotifyTopologyUpdateWatchers(oldTopology, newTopology *kueue.Topology) {
	for _, watcher := range m.topologyUpdateWatchers {
		watcher.NotifyTopologyUpdate(oldTopology, newTopology)
	}
}

func (m *Manager) AddFinishedWorkload(wl *kueue.Workload) {
	m.Lock()
	defer m.Unlock()
	m.addFinishedWorkloadWithoutLock(wl)
}

func (m *Manager) addFinishedWorkloadWithoutLock(wl *kueue.Workload) {
	wlKey := workload.Key(wl)

	if !workload.IsFinished(wl) {
		return
	}

	qKey := queue.KeyFromWorkload(wl)

	m.finishedWorkloads[wlKey] = qKey

	q := m.localQueues[qKey]
	if q == nil {
		return
	}
	q.finishedWorkloads.Insert(wlKey)
	reportLQFinishedWorkloads(m, q)

	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return
	}
	cq.finishedWorkloads.Insert(wlKey)
	reportCQFinishedWorkloads(cq, m.roleTracker)
}

func (m *Manager) deleteFinishedWorkloadWithoutLock(wlKey workload.Reference) {
	qKey, ok := m.finishedWorkloads[wlKey]
	if !ok {
		return
	}

	delete(m.finishedWorkloads, wlKey)

	q := m.localQueues[qKey]
	if q == nil {
		return
	}

	q.finishedWorkloads.Delete(wlKey)
	reportLQFinishedWorkloads(m, q)

	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return
	}
	cq.finishedWorkloads.Delete(wlKey)
	reportCQFinishedWorkloads(cq, m.roleTracker)
}

func (m *Manager) AddOrUpdateCohort(ctx context.Context, cohort *kueue.Cohort) {
	m.Lock()
	defer m.Unlock()
	cohortName := kueue.CohortReference(cohort.Name)

	m.hm.AddCohort(cohortName)
	m.hm.UpdateCohortEdge(cohortName, cohort.Spec.ParentName)
	if requeueWorkloadsCohort(ctx, m, m.hm.Cohort(cohortName)) {
		m.Broadcast()
	}
}

func (m *Manager) DeleteCohort(cohortName kueue.CohortReference) {
	m.Lock()
	defer m.Unlock()
	m.hm.DeleteCohort(cohortName)
}

func (m *Manager) AddClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	m.Lock()
	defer m.Unlock()

	if cq := m.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name)); cq != nil {
		return errClusterQueueAlreadyExists
	}

	var afsEntryPenalties *queueafs.AfsEntryPenalties
	var afsConsumedResources *queueafs.AfsConsumedResources
	if afs.Enabled(m.admissionFairSharingConfig) {
		afsEntryPenalties = m.AfsEntryPenalties
		afsConsumedResources = m.AfsConsumedResources
	}
	cqImpl, err := newClusterQueue(ctx, m.client, cq, m.workloadOrdering, m.admissionFairSharingConfig, afsEntryPenalties, afsConsumedResources)
	if err != nil {
		return err
	}
	m.hm.AddClusterQueue(cqImpl)
	m.hm.UpdateClusterQueueEdge(kueue.ClusterQueueReference(cq.Name), cq.Spec.CohortName)

	// Iterate through existing queues, as queues corresponding to this cluster
	// queue might have been added earlier.
	var queues kueue.LocalQueueList
	if err := m.client.List(ctx, &queues, client.MatchingFields{utilindexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing queues pointing to the cluster queue: %w", err)
	}
	addedWorkloads := false
	for _, q := range queues.Items {
		qImpl := m.localQueues[queue.Key(&q)]
		if qImpl != nil {
			added := cqImpl.AddFromLocalQueue(qImpl, m.roleTracker)
			addedWorkloads = addedWorkloads || added
			cqImpl.addLocalQueue(queue.Key(&q))
		}
	}

	queued := requeueWorkloadsCQ(ctx, m, cqImpl)
	reportPendingWorkloads(m, kueue.ClusterQueueReference(cq.Name))

	if queued || addedWorkloads {
		m.Broadcast()
	}
	return nil
}

func (m *Manager) UpdateClusterQueue(ctx context.Context, cq *kueue.ClusterQueue, specUpdated bool) error {
	m.Lock()
	defer m.Unlock()
	cqName := kueue.ClusterQueueReference(cq.Name)

	cqImpl := m.hm.ClusterQueue(cqName)
	if cqImpl == nil {
		return ErrClusterQueueDoesNotExist
	}

	oldActive := cqImpl.Active()
	// TODO(#8): recreate heap based on a change of queueing policy.
	if err := cqImpl.Update(cq); err != nil {
		return err
	}
	m.hm.UpdateClusterQueueEdge(cqName, cq.Spec.CohortName)

	// TODO(#8): Selectively move workloads based on the exact event.
	// If any workload becomes admissible or the queue becomes active.
	if (specUpdated && requeueWorkloadsCQ(ctx, m, cqImpl)) || (!oldActive && cqImpl.Active()) {
		reportPendingWorkloads(m, cqName)
		m.Broadcast()
	}
	return nil
}

func (m *Manager) RebuildClusterQueue(cq *kueue.ClusterQueue, lqName string) error {
	m.Lock()
	defer m.Unlock()
	cqImpl := m.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name))
	if cqImpl == nil {
		return ErrClusterQueueDoesNotExist
	}
	cqImpl.RebuildLocalQueue(lqName)
	return nil
}

func (m *Manager) DeleteClusterQueue(cq *kueue.ClusterQueue) {
	m.Lock()
	defer m.Unlock()
	cqImpl := m.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name))
	if cqImpl == nil {
		return
	}
	cqName := kueue.ClusterQueueReference(cq.Name)
	m.hm.DeleteClusterQueue(cqName)
	clearCQMetrics(cqName)
}

func (m *Manager) DefaultLocalQueueExist(namespace string) bool {
	m.RLock()
	defer m.RUnlock()

	_, ok := m.localQueues[queue.DefaultQueueKey(namespace)]
	return ok
}

func (m *Manager) AddLocalQueue(ctx context.Context, q *kueue.LocalQueue) error {
	m.Lock()
	defer m.Unlock()

	key := queue.Key(q)
	if _, ok := m.localQueues[key]; ok {
		return fmt.Errorf("queue %q already exists", q.Name)
	}
	qImpl := newLocalQueue(q)
	m.localQueues[key] = qImpl

	cq := m.hm.ClusterQueue(qImpl.ClusterQueue)
	if cq != nil {
		cq.addLocalQueue(key)
	}

	// Iterate through existing workloads, as workloads corresponding to this
	// queue might have been added earlier.
	var workloads kueue.WorkloadList
	if err := m.client.List(ctx, &workloads, client.MatchingFields{utilindexer.WorkloadQueueKey: q.Name}, client.InNamespace(q.Namespace)); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for _, w := range workloads.Items {
		m.assignWorkload(workload.Key(&w), qImpl.Key)

		if workload.IsFinished(&w) {
			m.addFinishedWorkloadWithoutLock(&w)
		}

		if !workload.IsAdmissible(&w) {
			continue
		}

		if features.Enabled(features.DynamicResourceAllocation) && workload.HasDRA(&w) {
			if m.draReconcileChannel != nil {
				m.draReconcileChannel <- event.TypedGenericEvent[*kueue.Workload]{Object: &w}
				log := ctrl.LoggerFrom(ctx).WithValues("workload", klog.KObj(&w))
				log.V(4).Info("Sent DRA workload to reconcile channel due to LocalQueue creation")
			}
			continue
		}

		workload.AdjustResources(ctx, m.client, &w)
		qImpl.AddOrUpdate(workload.NewInfo(&w, m.workloadInfoOptions...))
	}

	if cq != nil && cq.AddFromLocalQueue(qImpl, m.roleTracker) {
		m.Broadcast()
	}
	return nil
}

func (m *Manager) UpdateLocalQueue(log logr.Logger, q *kueue.LocalQueue) error {
	m.Lock()
	defer m.Unlock()
	qImpl, ok := m.localQueues[queue.Key(q)]
	if !ok {
		return ErrLocalQueueDoesNotExistOrInactive
	}
	if qImpl.ClusterQueue != q.Spec.ClusterQueue {
		oldCQ := m.hm.ClusterQueue(qImpl.ClusterQueue)
		if oldCQ != nil {
			oldCQ.DeleteFromLocalQueue(log, qImpl, m.roleTracker)
			oldCQ.deleteLocalQueue(queue.Key(q))
		}
		newCQ := m.hm.ClusterQueue(q.Spec.ClusterQueue)
		if newCQ != nil {
			newCQ.AddFromLocalQueue(qImpl, m.roleTracker)
			newCQ.addLocalQueue(queue.Key(q))
			m.Broadcast()
		}
	}
	qImpl.update(q)
	return nil
}

func (m *Manager) DeleteLocalQueue(log logr.Logger, q *kueue.LocalQueue) {
	m.Lock()
	defer m.Unlock()
	key := queue.Key(q)
	qImpl := m.localQueues[key]
	if qImpl == nil {
		return
	}
	cq := m.hm.ClusterQueue(qImpl.ClusterQueue)
	if cq != nil {
		cq.DeleteFromLocalQueue(log, qImpl, m.roleTracker)
		cq.deleteLocalQueue(key)
	}
	clearLQMetrics(key)
	delete(m.localQueues, key)
}

func (m *Manager) PendingWorkloads(q *kueue.LocalQueue) (int32, error) {
	m.RLock()
	defer m.RUnlock()

	qImpl, ok := m.localQueues[queue.Key(q)]
	if !ok {
		return 0, ErrLocalQueueDoesNotExistOrInactive
	}

	return int32(len(qImpl.items)), nil
}

func (m *Manager) Pending(cq *kueue.ClusterQueue) (int, error) {
	m.RLock()
	defer m.RUnlock()

	cqImpl := m.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name))
	if cqImpl == nil {
		return 0, ErrClusterQueueDoesNotExist
	}

	return cqImpl.PendingTotal(), nil
}

func (m *Manager) QueueForWorkloadExists(wl *kueue.Workload) bool {
	m.RLock()
	defer m.RUnlock()
	_, ok := m.localQueues[queue.KeyFromWorkload(wl)]
	return ok
}

// ClusterQueueForWorkload returns the name of the ClusterQueue where the
// workload should be queued and whether it exists.
// Returns empty string if the queue doesn't exist.
func (m *Manager) ClusterQueueForWorkload(wl *kueue.Workload) (kueue.ClusterQueueReference, bool) {
	m.RLock()
	defer m.RUnlock()
	q, ok := m.localQueues[queue.KeyFromWorkload(wl)]
	if !ok {
		return "", false
	}
	ok = m.hm.ClusterQueue(q.ClusterQueue) != nil
	return q.ClusterQueue, ok
}

// AddOrUpdateWorkload adds or updates workload to the corresponding queue.
// Returns whether the queue existed.
func (m *Manager) AddOrUpdateWorkload(log logr.Logger, w *kueue.Workload, opts ...workload.InfoOption) error {
	m.Lock()
	defer m.Unlock()
	return m.AddOrUpdateWorkloadWithoutLock(log, w, opts...)
}

func (m *Manager) AddOrUpdateWorkloadWithoutLock(log logr.Logger, w *kueue.Workload, opts ...workload.InfoOption) error {
	if !workload.IsAdmissible(w) {
		return errWorkloadIsInadmissible
	}

	wlKey := workload.Key(w)
	qKey := queue.KeyFromWorkload(w)

	assignedQueue, ok := m.workloadAssignedQueues[wlKey]
	if ok && assignedQueue != qKey {
		m.deleteAndForgetWorkloadWithoutLock(log, wlKey)
	}

	q := m.localQueues[qKey]
	if q == nil {
		return ErrLocalQueueDoesNotExistOrInactive
	}
	allOptions := append(m.workloadInfoOptions, opts...)
	wInfo := workload.NewInfo(w, allOptions...)
	m.addWorkload(wInfo, q)

	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return ErrClusterQueueDoesNotExist
	}
	cq.PushOrUpdate(wInfo)
	reportLQPendingWorkloads(m, q)
	reportCQPendingWorkloads(m, cq)
	m.Broadcast()
	log.V(5).Info("Added/updated workload in queues; Broadcast successful.")
	return nil
}

// RequeueWorkload requeues the workload ensuring that the queue and the
// workload still exist in the client cache and not admitted. It won't
// requeue if the workload is already in the queue (possible if the workload was updated).
func (m *Manager) RequeueWorkload(ctx context.Context, info *workload.Info, reason RequeueReason) bool {
	m.Lock()
	defer m.Unlock()

	var w kueue.Workload
	// Always get the newest workload to avoid requeuing the out-of-date obj.
	err := m.client.Get(ctx, client.ObjectKeyFromObject(info.Obj), &w)
	// Since the client is cached, the only possible error is NotFound
	if apierrors.IsNotFound(err) || workload.HasQuotaReservation(&w) {
		return false
	}

	qKey := queue.KeyFromWorkload(&w)

	q := m.localQueues[qKey]
	if q == nil {
		return false
	}
	info.Update(&w)
	m.addWorkload(info, q)

	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return false
	}

	added := cq.RequeueIfNotPresent(ctx, info, reason)
	reportCQPendingWorkloads(m, cq)
	reportLQPendingWorkloads(m, q)
	if added {
		m.Broadcast()
	}
	return added
}

// Delete the workload from queue or cluster queue.
// Does not remove the queue assignment caching.
func (m *Manager) DeleteWorkload(log logr.Logger, wlKey workload.Reference) {
	m.Lock()
	defer m.Unlock()
	m.deleteWorkloadWithoutLock(log, wlKey)
}

// Deletes the workload from assigned queue and purges the assigment caching.
// Uses a lock to ensure operation safety.
func (m *Manager) DeleteAndForgetWorkload(log logr.Logger, wlKey workload.Reference) {
	m.Lock()
	defer m.Unlock()
	m.deleteAndForgetWorkloadWithoutLock(log, wlKey)
}

// Deletes the workload from local/cluster queue and purges queue assignment caching.
func (m *Manager) deleteAndForgetWorkloadWithoutLock(log logr.Logger, wlKey workload.Reference) {
	m.deleteWorkloadWithoutLock(log, wlKey)
	delete(m.workloadAssignedQueues, wlKey)
	m.deleteFinishedWorkloadWithoutLock(wlKey)
}

func (m *Manager) addWorkload(wlInfo *workload.Info, q *LocalQueue) {
	m.assignWorkload(workload.Key(wlInfo.Obj), q.Key)
	q.AddOrUpdate(wlInfo)
}

func (m *Manager) assignWorkload(wlKey workload.Reference, qKey queue.LocalQueueReference) {
	m.workloadAssignedQueues[wlKey] = qKey
}

func (m *Manager) deleteWorkloadWithoutLock(log logr.Logger, wlKey workload.Reference) {
	qKey := m.workloadAssignedQueues[wlKey]
	q := m.localQueues[qKey]
	if q == nil {
		return
	}
	delete(q.items, wlKey)

	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq != nil {
		cq.Delete(log, wlKey)
		reportCQPendingWorkloads(m, cq)
	}
	reportLQPendingWorkloads(m, q)

	m.DeleteSecondPassWithoutLock(wlKey)
}

// QueueAssociatedInadmissibleWorkloadsAfter requeues into the heaps all
// previously inadmissible workloads in the same ClusterQueue and cohort (if
// they exist) as the provided admitted workload to the heaps.
// An optional action can be executed at the beginning of the function,
// while holding the lock, to provide atomicity with the operations in the
// queues.
func (m *Manager) QueueAssociatedInadmissibleWorkloadsAfter(ctx context.Context, wlKey workload.Reference, action func()) {
	m.Lock()
	defer m.Unlock()
	if action != nil {
		action()
	}

	qKey, ok := m.workloadAssignedQueues[wlKey]
	if !ok {
		return
	}
	q := m.localQueues[qKey]
	if q == nil {
		return
	}
	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return
	}

	if requeueWorkloadsCQ(ctx, m, cq) {
		m.Broadcast()
	}
}

// UpdateWorkload updates the workload to the corresponding queue or adds it if
// it didn't exist. Returns whether the queue existed.
func (m *Manager) UpdateWorkload(log logr.Logger, w *kueue.Workload, opts ...workload.InfoOption) error {
	m.Lock()
	defer m.Unlock()
	return m.AddOrUpdateWorkloadWithoutLock(log, w, opts...)
}

// CleanUpOnContext tracks the context. When closed, it wakes routines waiting
// on elements to be available. It should be called before doing any calls to
// Heads.
func (m *Manager) CleanUpOnContext(ctx context.Context) {
	<-ctx.Done()
	// Hold the same lock used by cond.Wait to avoid lost wakeups between
	// checking ctx.Done() in Heads and entering Wait.
	m.Lock()
	defer m.Unlock()
	m.Broadcast()
}

// Heads returns the heads of the queues, along with their associated ClusterQueue.
// It blocks if the queues empty until they have elements or the context terminates.
func (m *Manager) Heads(ctx context.Context) []workload.Info {
	m.Lock()
	defer m.Unlock()
	log := ctrl.LoggerFrom(ctx)
	for {
		workloads := m.heads()
		log.V(3).Info("Obtained ClusterQueue heads", "count", len(workloads))
		if len(workloads) != 0 {
			return workloads
		}
		select {
		case <-ctx.Done():
			return nil
		default:
			m.cond.Wait()
		}
	}
}

func (m *Manager) GetWorkloadFromCache(wlKey workload.Reference) *kueue.Workload {
	m.RLock()
	defer m.RUnlock()

	lqRef, ok := m.workloadAssignedQueues[wlKey]
	if !ok {
		return nil
	}

	lq := m.localQueues[lqRef]
	if lq == nil {
		return nil
	}

	if wlInfo, ok := lq.items[wlKey]; ok {
		return wlInfo.Obj
	}

	cq := m.hm.ClusterQueue(lq.ClusterQueue)
	if cq == nil {
		return nil
	}

	if wlInfo := cq.heap.GetByKey(wlKey); wlInfo != nil {
		return wlInfo.Obj
	}

	return nil
}

func (m *Manager) heads() []workload.Info {
	workloads := m.secondPassQueue.takeAllReady()
	for cqName, cq := range m.hm.ClusterQueues() {
		// Cache might be nil in tests, if cache is nil, we'll skip the check.
		if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(cqName) {
			continue
		}
		wl := cq.Pop()
		reportCQPendingWorkloads(m, cq)
		if wl == nil {
			continue
		}
		wlKey := workload.Key(wl.Obj)
		wlCopy := *wl
		wlCopy.ClusterQueue = cqName
		workloads = append(workloads, wlCopy)

		qKey := m.workloadAssignedQueues[wlKey]
		q := m.localQueues[qKey]
		delete(q.items, wlKey)

		reportLQPendingWorkloads(m, q)
	}
	return workloads
}

func (m *Manager) Broadcast() {
	m.cond.Broadcast()
}

func (m *Manager) GetClusterQueueNames() []kueue.ClusterQueueReference {
	m.RLock()
	defer m.RUnlock()
	return m.hm.ClusterQueuesNames()
}

func (m *Manager) getClusterQueue(cqName kueue.ClusterQueueReference) *ClusterQueue {
	m.RLock()
	defer m.RUnlock()
	return m.getClusterQueueLockless(cqName)
}

func (m *Manager) getClusterQueueLockless(cqName kueue.ClusterQueueReference) *ClusterQueue {
	return m.hm.ClusterQueue(cqName)
}

func (m *Manager) PendingWorkloadsInfo(cqName kueue.ClusterQueueReference) []*workload.Info {
	cq := m.getClusterQueue(cqName)
	if cq == nil {
		return nil
	}
	return cq.Snapshot()
}

// ClusterQueueFromLocalQueue returns ClusterQueue name and whether it's found,
// given a QueueKey(namespace/localQueueName) as the parameter
func (m *Manager) ClusterQueueFromLocalQueue(localQueueKey queue.LocalQueueReference) (kueue.ClusterQueueReference, bool) {
	m.RLock()
	defer m.RUnlock()
	if lq, ok := m.localQueues[localQueueKey]; ok {
		return lq.ClusterQueue, true
	}
	return "", false
}

// DeleteSecondPassWithoutLock deletes the pending workload from the second
// pass queue.
func (m *Manager) DeleteSecondPassWithoutLock(wlKey workload.Reference) {
	m.secondPassQueue.deleteByKey(wlKey)
}

// QueueSecondPassIfNeeded queues for the second pass of scheduling with exponential
// delay.
func (m *Manager) QueueSecondPassIfNeeded(ctx context.Context, w *kueue.Workload, iteration int) bool {
	log := ctrl.LoggerFrom(ctx)
	if workload.NeedsSecondPass(w) {
		iteration++
		delay := m.secondPassQueue.nextDelay(iteration)
		log.V(3).Info("Workload pre-queued for second pass (with backoff)", "workload", workload.Key(w), "delay", delay)
		m.secondPassQueue.prequeue(w)
		m.clock.AfterFunc(delay, func() {
			m.queueSecondPass(ctx, w, iteration)
		})
		return true
	} else if iteration > 0 {
		// Remove the workload from the second-pass queue only after at least one
		// retry iteration, to avoid canceling the initial backoff window.
		// See #8357.
		log.V(3).Info("Workload removed from second pass queue", "workload", workload.Key(w))
		m.secondPassQueue.deleteByKey(workload.Key(w))
	}
	return false
}

func (m *Manager) queueSecondPass(ctx context.Context, w *kueue.Workload, iteration int) {
	m.Lock()
	defer m.Unlock()

	log := ctrl.LoggerFrom(ctx)
	wInfo := workload.NewInfo(w, m.workloadInfoOptions...)
	wInfo.SecondPassIteration = iteration
	if m.secondPassQueue.queue(wInfo) {
		log.V(3).Info("Workload queued for second pass of scheduling", "workload", workload.Key(w))
		m.Broadcast()
	}
}

type WorkloadUpdateWatcher interface {
	NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload)
}

func (m *Manager) NotifyWorkloadUpdateWatchers(oldWorkload, newWorkload *kueue.Workload) {
	for _, watcher := range m.workloadUpdateWatchers {
		watcher.NotifyWorkloadUpdate(oldWorkload, newWorkload)
	}
}

func (m *Manager) AddWorkloadUpdateWatcher(watcher WorkloadUpdateWatcher) {
	m.workloadUpdateWatchers = append(m.workloadUpdateWatchers, watcher)
}
