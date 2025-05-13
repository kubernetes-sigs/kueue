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

	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	ErrLocalQueueDoesNotExistOrInactive = errors.New("localQueue doesn't exist or inactive")
	ErrClusterQueueDoesNotExist         = errors.New("clusterQueue doesn't exist")
	errClusterQueueAlreadyExists        = errors.New("clusterQueue already exists")
)

type options struct {
	podsReadyRequeuingTimestamp config.RequeuingTimestamp
	workloadInfoOptions         []workload.InfoOption
	admissionFairSharing        *config.AdmissionFairSharing
}

// Option configures the manager.
type Option func(*options)

var defaultOptions = options{
	podsReadyRequeuingTimestamp: config.EvictionTimestamp,
	workloadInfoOptions:         []workload.InfoOption{},
}

func WithAdmissionFairSharing(cfg *config.AdmissionFairSharing) Option {
	return func(o *options) {
		if features.Enabled(features.AdmissionFairSharing) {
			o.admissionFairSharing = cfg
		}
	}
}

// WithPodsReadyRequeuingTimestamp sets the timestamp that is used for ordering
// workloads that have been requeued due to the PodsReady condition.
func WithPodsReadyRequeuingTimestamp(ts config.RequeuingTimestamp) Option {
	return func(o *options) {
		o.podsReadyRequeuingTimestamp = ts
	}
}

// WithExcludedResourcePrefixes sets the list of excluded resource prefixes
func WithExcludedResourcePrefixes(excludedPrefixes []string) Option {
	return func(o *options) {
		o.workloadInfoOptions = append(o.workloadInfoOptions, workload.WithExcludedResourcePrefixes(excludedPrefixes))
	}
}

// WithResourceTransformations sets the resource transformations.
func WithResourceTransformations(transforms []config.ResourceTransformation) Option {
	return func(o *options) {
		o.workloadInfoOptions = append(o.workloadInfoOptions, workload.WithResourceTransformations(transforms))
	}
}

type TopologyUpdateWatcher interface {
	NotifyTopologyUpdate(oldTopology, newTopology *kueuealpha.Topology)
}

type Manager struct {
	sync.RWMutex
	cond sync.Cond

	client        client.Client
	statusChecker StatusChecker
	localQueues   map[LocalQueueReference]*LocalQueue

	snapshotsMutex sync.RWMutex
	snapshots      map[kueue.ClusterQueueReference][]kueue.ClusterQueuePendingWorkload

	workloadOrdering workload.Ordering

	workloadInfoOptions []workload.InfoOption

	hm hierarchy.Manager[*ClusterQueue, *cohort]

	topologyUpdateWatchers []TopologyUpdateWatcher

	admissionFairSharingConfig *config.AdmissionFairSharing
}

func NewManager(client client.Client, checker StatusChecker, opts ...Option) *Manager {
	options := defaultOptions
	for _, opt := range opts {
		opt(&options)
	}
	m := &Manager{
		client:         client,
		statusChecker:  checker,
		localQueues:    make(map[LocalQueueReference]*LocalQueue),
		snapshotsMutex: sync.RWMutex{},
		snapshots:      make(map[kueue.ClusterQueueReference][]kueue.ClusterQueuePendingWorkload, 0),
		workloadOrdering: workload.Ordering{
			PodsReadyRequeuingTimestamp: options.podsReadyRequeuingTimestamp,
		},
		workloadInfoOptions: options.workloadInfoOptions,
		hm:                  hierarchy.NewManager[*ClusterQueue, *cohort](newCohort),

		topologyUpdateWatchers:     make([]TopologyUpdateWatcher, 0),
		admissionFairSharingConfig: options.admissionFairSharing,
	}
	m.cond.L = &m.RWMutex
	return m
}

func (m *Manager) AddTopologyUpdateWatcher(watcher TopologyUpdateWatcher) {
	m.topologyUpdateWatchers = append(m.topologyUpdateWatchers, watcher)
}

func (m *Manager) NotifyTopologyUpdateWatchers(oldTopology, newTopology *kueuealpha.Topology) {
	for _, watcher := range m.topologyUpdateWatchers {
		watcher.NotifyTopologyUpdate(oldTopology, newTopology)
	}
}

func (m *Manager) AddOrUpdateCohort(ctx context.Context, cohort *kueuealpha.Cohort) {
	m.Lock()
	defer m.Unlock()
	cohortName := kueue.CohortReference(cohort.Name)

	m.hm.AddCohort(cohortName)
	m.hm.UpdateCohortEdge(cohortName, cohort.Spec.Parent)
	if m.requeueWorkloadsCohort(ctx, m.hm.Cohort(cohortName)) {
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

	cqImpl, err := newClusterQueue(ctx, m.client, cq, m.workloadOrdering, m.admissionFairSharingConfig)
	if err != nil {
		return err
	}
	m.hm.AddClusterQueue(cqImpl)
	m.hm.UpdateClusterQueueEdge(kueue.ClusterQueueReference(cq.Name), cq.Spec.Cohort)

	// Iterate through existing queues, as queues corresponding to this cluster
	// queue might have been added earlier.
	var queues kueue.LocalQueueList
	if err := m.client.List(ctx, &queues, client.MatchingFields{utilindexer.QueueClusterQueueKey: cq.Name}); err != nil {
		return fmt.Errorf("listing queues pointing to the cluster queue: %w", err)
	}
	addedWorkloads := false
	for _, q := range queues.Items {
		qImpl := m.localQueues[Key(&q)]
		if qImpl != nil {
			added := cqImpl.AddFromLocalQueue(qImpl)
			addedWorkloads = addedWorkloads || added
		}
	}

	queued := m.requeueWorkloadsCQ(ctx, cqImpl)
	m.reportPendingWorkloads(kueue.ClusterQueueReference(cq.Name), cqImpl)

	// needs to be iterated over again here incase inadmissible workloads were added by requeueWorkloadsCQ
	if features.Enabled(features.LocalQueueMetrics) {
		for _, q := range queues.Items {
			qImpl := m.localQueues[Key(&q)]
			if qImpl != nil && metrics.ShouldReportLocalMetrics(q.Labels) {
				m.reportLQPendingWorkloads(qImpl)
			}
		}
	}

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
	m.hm.UpdateClusterQueueEdge(cqName, cq.Spec.Cohort)

	// TODO(#8): Selectively move workloads based on the exact event.
	// If any workload becomes admissible or the queue becomes active.
	if (specUpdated && m.requeueWorkloadsCQ(ctx, cqImpl)) || (!oldActive && cqImpl.Active()) {
		m.reportPendingWorkloads(cqName, cqImpl)
		if features.Enabled(features.LocalQueueMetrics) {
			for _, q := range m.localQueues {
				if q.ClusterQueue == cqName && metrics.ShouldReportLocalMetrics(q.Labels) {
					m.reportLQPendingWorkloads(q)
				}
			}
		}
		m.Broadcast()
	}
	return nil
}

func (m *Manager) HeapifyClusterQueue(cq *kueue.ClusterQueue, lqName string) error {
	m.Lock()
	defer m.Unlock()
	cqImpl := m.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name))
	if cqImpl == nil {
		return ErrClusterQueueDoesNotExist
	}
	cqImpl.Heapify(lqName)
	return nil
}

func (m *Manager) DeleteClusterQueue(cq *kueue.ClusterQueue) {
	m.Lock()
	defer m.Unlock()
	cqImpl := m.hm.ClusterQueue(kueue.ClusterQueueReference(cq.Name))
	if cqImpl == nil {
		return
	}
	m.hm.DeleteClusterQueue(kueue.ClusterQueueReference(cq.Name))
	metrics.ClearClusterQueueMetrics(cq.Name)
}

func (m *Manager) DefaultLocalQueueExist(namespace string) bool {
	m.Lock()
	defer m.Unlock()

	_, ok := m.localQueues[DefaultQueueKey(namespace)]
	return ok
}

func (m *Manager) AddLocalQueue(ctx context.Context, q *kueue.LocalQueue) error {
	m.Lock()
	defer m.Unlock()

	key := Key(q)
	if _, ok := m.localQueues[key]; ok {
		return fmt.Errorf("queue %q already exists", q.Name)
	}
	qImpl := newLocalQueue(q)
	m.localQueues[key] = qImpl
	// Iterate through existing workloads, as workloads corresponding to this
	// queue might have been added earlier.
	var workloads kueue.WorkloadList
	if err := m.client.List(ctx, &workloads, client.MatchingFields{utilindexer.WorkloadQueueKey: q.Name}, client.InNamespace(q.Namespace)); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	for _, w := range workloads.Items {
		if workload.HasQuotaReservation(&w) {
			continue
		}
		workload.AdjustResources(ctx, m.client, &w)
		qImpl.AddOrUpdate(workload.NewInfo(&w, m.workloadInfoOptions...))
	}
	cq := m.hm.ClusterQueue(qImpl.ClusterQueue)
	if cq != nil && cq.AddFromLocalQueue(qImpl) {
		m.Broadcast()
	}
	return nil
}

func (m *Manager) UpdateLocalQueue(q *kueue.LocalQueue) error {
	m.Lock()
	defer m.Unlock()
	qImpl, ok := m.localQueues[Key(q)]
	if !ok {
		return ErrLocalQueueDoesNotExistOrInactive
	}
	if qImpl.ClusterQueue != q.Spec.ClusterQueue {
		oldCQ := m.hm.ClusterQueue(qImpl.ClusterQueue)
		if oldCQ != nil {
			oldCQ.DeleteFromLocalQueue(qImpl)
		}
		newCQ := m.hm.ClusterQueue(q.Spec.ClusterQueue)
		if newCQ != nil && newCQ.AddFromLocalQueue(qImpl) {
			m.Broadcast()
		}
	}
	qImpl.update(q)
	return nil
}

func (m *Manager) DeleteLocalQueue(q *kueue.LocalQueue) {
	m.Lock()
	defer m.Unlock()
	key := Key(q)
	qImpl := m.localQueues[key]
	if qImpl == nil {
		return
	}
	cq := m.hm.ClusterQueue(qImpl.ClusterQueue)
	if cq != nil {
		cq.DeleteFromLocalQueue(qImpl)
	}
	if features.Enabled(features.LocalQueueMetrics) {
		namespace, lqName := MustParseLocalQueueReference(key)
		metrics.ClearLocalQueueMetrics(metrics.LocalQueueReference{
			Name:      lqName,
			Namespace: namespace,
		})
	}
	delete(m.localQueues, key)
}

func (m *Manager) PendingWorkloads(q *kueue.LocalQueue) (int32, error) {
	m.RLock()
	defer m.RUnlock()

	qImpl, ok := m.localQueues[Key(q)]
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

	return cqImpl.Pending(), nil
}

func (m *Manager) QueueForWorkloadExists(wl *kueue.Workload) bool {
	m.RLock()
	defer m.RUnlock()
	_, ok := m.localQueues[KeyFromWorkload(wl)]
	return ok
}

// ClusterQueueForWorkload returns the name of the ClusterQueue where the
// workload should be queued and whether it exists.
// Returns empty string if the queue doesn't exist.
func (m *Manager) ClusterQueueForWorkload(wl *kueue.Workload) (kueue.ClusterQueueReference, bool) {
	m.RLock()
	defer m.RUnlock()
	q, ok := m.localQueues[KeyFromWorkload(wl)]
	if !ok {
		return "", false
	}
	ok = m.hm.ClusterQueue(q.ClusterQueue) != nil
	return q.ClusterQueue, ok
}

// AddOrUpdateWorkload adds or updates workload to the corresponding queue.
// Returns whether the queue existed.
func (m *Manager) AddOrUpdateWorkload(w *kueue.Workload) error {
	m.Lock()
	defer m.Unlock()
	return m.AddOrUpdateWorkloadWithoutLock(w)
}

func (m *Manager) AddOrUpdateWorkloadWithoutLock(w *kueue.Workload) error {
	qKey := KeyFromWorkload(w)
	q := m.localQueues[qKey]
	if q == nil {
		return ErrLocalQueueDoesNotExistOrInactive
	}
	wInfo := workload.NewInfo(w, m.workloadInfoOptions...)
	q.AddOrUpdate(wInfo)
	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return ErrClusterQueueDoesNotExist
	}
	cq.PushOrUpdate(wInfo)
	if metrics.ShouldReportLocalMetrics(q.Labels) {
		m.reportLQPendingWorkloads(q)
	}
	m.reportPendingWorkloads(q.ClusterQueue, cq)
	m.Broadcast()
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

	q := m.localQueues[KeyFromWorkload(&w)]
	if q == nil {
		return false
	}
	info.Update(&w)
	q.AddOrUpdate(info)
	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return false
	}

	added := cq.RequeueIfNotPresent(info, reason)
	m.reportPendingWorkloads(q.ClusterQueue, cq)
	if metrics.ShouldReportLocalMetrics(q.Labels) {
		m.reportLQPendingWorkloads(q)
	}
	if added {
		m.Broadcast()
	}
	return added
}

func (m *Manager) DeleteWorkload(w *kueue.Workload) {
	m.Lock()
	m.deleteWorkloadFromQueueAndClusterQueue(w, KeyFromWorkload(w))
	m.Unlock()
}

func (m *Manager) deleteWorkloadFromQueueAndClusterQueue(w *kueue.Workload, qKey LocalQueueReference) {
	q := m.localQueues[qKey]
	if q == nil {
		return
	}
	delete(q.items, workload.Key(w))
	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq != nil {
		cq.Delete(w)
		m.reportPendingWorkloads(q.ClusterQueue, cq)
	}
	if metrics.ShouldReportLocalMetrics(q.Labels) {
		m.reportLQPendingWorkloads(q)
	}
}

// QueueAssociatedInadmissibleWorkloadsAfter requeues into the heaps all
// previously inadmissible workloads in the same ClusterQueue and cohort (if
// they exist) as the provided admitted workload to the heaps.
// An optional action can be executed at the beginning of the function,
// while holding the lock, to provide atomicity with the operations in the
// queues.
func (m *Manager) QueueAssociatedInadmissibleWorkloadsAfter(ctx context.Context, w *kueue.Workload, action func()) {
	m.Lock()
	defer m.Unlock()
	if action != nil {
		action()
	}

	q := m.localQueues[KeyFromWorkload(w)]
	if q == nil {
		return
	}
	cq := m.hm.ClusterQueue(q.ClusterQueue)
	if cq == nil {
		return
	}

	if m.requeueWorkloadsCQ(ctx, cq) {
		m.Broadcast()
	}
}

// QueueInadmissibleWorkloads moves all inadmissibleWorkloads in
// corresponding ClusterQueues to heap. If at least one workload queued,
// we will broadcast the event.
func (m *Manager) QueueInadmissibleWorkloads(ctx context.Context, cqNames sets.Set[kueue.ClusterQueueReference]) {
	m.Lock()
	defer m.Unlock()
	if len(cqNames) == 0 {
		return
	}

	var queued bool
	for name := range cqNames {
		cq := m.hm.ClusterQueue(name)
		if cq == nil {
			continue
		}
		if m.requeueWorkloadsCQ(ctx, cq) {
			queued = true
		}
	}

	if queued {
		m.Broadcast()
	}
}

// requeueWorkloadsCQ moves all workloads in the same
// cohort with this ClusterQueue from inadmissibleWorkloads to heap. If the
// cohort of this ClusterQueue is empty, it just moves all workloads in this
// ClusterQueue. If at least one workload is moved, returns true, otherwise
// returns false.
// The events listed below could make workloads in the same cohort admissible.
// Then requeueWorkloadsCQ need to be invoked.
// 1. delete events for any admitted workload in the cohort.
// 2. add events of any cluster queue in the cohort.
// 3. update events of any cluster queue in the cohort.
// 4. update of cohort.
//
// WARNING: must hold a read-lock on the manager when calling,
// or otherwise risk encountering an infinite loop if a Cohort
// cycle is introduced.
func (m *Manager) requeueWorkloadsCQ(ctx context.Context, cq *ClusterQueue) bool {
	if cq.HasParent() {
		return m.requeueWorkloadsCohort(ctx, cq.Parent())
	}
	return cq.QueueInadmissibleWorkloads(ctx, m.client)
}

// moveWorkloadsCohorts checks for a cycle, the moves all inadmissible
// workloads in the Cohort tree. If a cycle exists, or no workloads were
// moved, it returns false.
//
// WARNING: must hold a read-lock on the manager when calling,
// or otherwise risk encountering an infinite loop if a Cohort
// cycle is introduced.
func (m *Manager) requeueWorkloadsCohort(ctx context.Context, cohort *cohort) bool {
	log := ctrl.LoggerFrom(ctx)

	if hierarchy.HasCycle(cohort) {
		log.V(2).Info("Attempted to move workloads from Cohort which has cycle", "cohort", cohort.GetName())
		return false
	}
	root := cohort.getRootUnsafe()
	log.V(2).Info("Attempting to move workloads", "cohort", cohort.Name, "root", root.Name)
	return requeueWorkloadsCohortSubtree(ctx, m, root)
}

func requeueWorkloadsCohortSubtree(ctx context.Context, m *Manager, cohort *cohort) bool {
	queued := false
	for _, clusterQueue := range cohort.ChildCQs() {
		queued = clusterQueue.QueueInadmissibleWorkloads(ctx, m.client) || queued
	}
	for _, childCohort := range cohort.ChildCohorts() {
		queued = requeueWorkloadsCohortSubtree(ctx, m, childCohort) || queued
	}
	return queued
}

// UpdateWorkload updates the workload to the corresponding queue or adds it if
// it didn't exist. Returns whether the queue existed.
func (m *Manager) UpdateWorkload(oldW, w *kueue.Workload) error {
	m.Lock()
	defer m.Unlock()
	if oldW.Spec.QueueName != w.Spec.QueueName {
		m.deleteWorkloadFromQueueAndClusterQueue(w, KeyFromWorkload(oldW))
	}
	return m.AddOrUpdateWorkloadWithoutLock(w)
}

// CleanUpOnContext tracks the context. When closed, it wakes routines waiting
// on elements to be available. It should be called before doing any calls to
// Heads.
func (m *Manager) CleanUpOnContext(ctx context.Context) {
	<-ctx.Done()
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

func (m *Manager) heads() []workload.Info {
	var workloads []workload.Info
	for cqName, cq := range m.hm.ClusterQueues() {
		// Cache might be nil in tests, if cache is nil, we'll skip the check.
		if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(cqName) {
			continue
		}
		wl := cq.Pop()
		if wl == nil {
			continue
		}
		m.reportPendingWorkloads(cqName, cq)
		wlCopy := *wl
		wlCopy.ClusterQueue = cqName
		workloads = append(workloads, wlCopy)
		q := m.localQueues[KeyFromWorkload(wl.Obj)]
		delete(q.items, workload.Key(wl.Obj))
		if metrics.ShouldReportLocalMetrics(q.Labels) {
			m.reportLQPendingWorkloads(q)
		}
	}
	return workloads
}

func (m *Manager) Broadcast() {
	m.cond.Broadcast()
}

func (m *Manager) reportLQPendingWorkloads(lq *LocalQueue) {
	active := m.PendingActiveInLocalQueue(lq)
	inadmissible := m.PendingInadmissibleInLocalQueue(lq)
	if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(lq.ClusterQueue) {
		inadmissible += active
		active = 0
	}
	namespace, lqName := MustParseLocalQueueReference(lq.Key)
	metrics.ReportLocalQueuePendingWorkloads(metrics.LocalQueueReference{
		Name:      lqName,
		Namespace: namespace,
	}, active, inadmissible)
}

func (m *Manager) reportPendingWorkloads(cqName kueue.ClusterQueueReference, cq *ClusterQueue) {
	active := cq.PendingActive()
	inadmissible := cq.PendingInadmissible()
	if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(cqName) {
		inadmissible += active
		active = 0
	}
	metrics.ReportPendingWorkloads(cqName, active, inadmissible)
}

func (m *Manager) GetClusterQueueNames() []kueue.ClusterQueueReference {
	m.RLock()
	defer m.RUnlock()
	return m.hm.ClusterQueuesNames()
}

func (m *Manager) getClusterQueue(cqName kueue.ClusterQueueReference) *ClusterQueue {
	m.RLock()
	defer m.RUnlock()
	return m.hm.ClusterQueue(cqName)
}

func (m *Manager) getClusterQueueLockless(cqName kueue.ClusterQueueReference) (val *ClusterQueue, ok bool) {
	val = m.hm.ClusterQueue(cqName)
	return val, val != nil
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
func (m *Manager) ClusterQueueFromLocalQueue(localQueueKey LocalQueueReference) (kueue.ClusterQueueReference, bool) {
	m.RLock()
	defer m.RUnlock()
	if lq, ok := m.localQueues[localQueueKey]; ok {
		return lq.ClusterQueue, true
	}
	return "", false
}

// UpdateSnapshot computes the new snapshot and replaces if it differs from the
// previous version. It returns true if the snapshot was actually updated.
func (m *Manager) UpdateSnapshot(cqName kueue.ClusterQueueReference, maxCount int32) bool {
	cq := m.getClusterQueue(cqName)
	if cq == nil {
		return false
	}
	newSnapshot := make([]kueue.ClusterQueuePendingWorkload, 0)
	for index, info := range cq.Snapshot() {
		if int32(index) >= maxCount {
			break
		}
		if info == nil {
			continue
		}
		newSnapshot = append(newSnapshot, kueue.ClusterQueuePendingWorkload{
			Name:      info.Obj.Name,
			Namespace: info.Obj.Namespace,
		})
	}
	prevSnapshot := m.GetSnapshot(cqName)
	if !equality.Semantic.DeepEqual(prevSnapshot, newSnapshot) {
		m.setSnapshot(cqName, newSnapshot)
		return true
	}
	return false
}

func (m *Manager) setSnapshot(cqName kueue.ClusterQueueReference, workloads []kueue.ClusterQueuePendingWorkload) {
	m.snapshotsMutex.Lock()
	defer m.snapshotsMutex.Unlock()
	m.snapshots[cqName] = workloads
}

func (m *Manager) GetSnapshot(cqName kueue.ClusterQueueReference) []kueue.ClusterQueuePendingWorkload {
	m.snapshotsMutex.RLock()
	defer m.snapshotsMutex.RUnlock()
	return m.snapshots[cqName]
}

func (m *Manager) DeleteSnapshot(cq *kueue.ClusterQueue) {
	m.snapshotsMutex.Lock()
	defer m.snapshotsMutex.Unlock()
	delete(m.snapshots, kueue.ClusterQueueReference(cq.Name))
}
