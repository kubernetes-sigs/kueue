/*
Copyright 2022 The Kubernetes Authors.

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/podtemplate"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	errQueueDoesNotExist         = errors.New("queue doesn't exist")
	errClusterQueueDoesNotExist  = errors.New("clusterQueue doesn't exist")
	errClusterQueueAlreadyExists = errors.New("clusterQueue already exists")
)

type Manager struct {
	sync.RWMutex
	cond sync.Cond

	client        client.Client
	statusChecker StatusChecker
	clusterQueues map[string]ClusterQueue
	localQueues   map[string]*LocalQueue

	snapshotsMutex sync.RWMutex
	snapshots      map[string][]kueue.ClusterQueuePendingWorkload

	// Key is cohort's name. Value is a set of associated ClusterQueue names.
	cohorts map[string]sets.Set[string]
}

func NewManager(client client.Client, checker StatusChecker) *Manager {
	m := &Manager{
		client:         client,
		statusChecker:  checker,
		localQueues:    make(map[string]*LocalQueue),
		clusterQueues:  make(map[string]ClusterQueue),
		cohorts:        make(map[string]sets.Set[string]),
		snapshotsMutex: sync.RWMutex{},
		snapshots:      make(map[string][]kueue.ClusterQueuePendingWorkload, 0),
	}
	m.cond.L = &m.RWMutex
	return m
}

func (m *Manager) AddClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	m.Lock()
	defer m.Unlock()

	if _, ok := m.clusterQueues[cq.Name]; ok {
		return errClusterQueueAlreadyExists
	}

	cqImpl, err := newClusterQueue(cq)
	if err != nil {
		return err
	}
	m.clusterQueues[cq.Name] = cqImpl

	cohort := cq.Spec.Cohort
	if cohort != "" {
		m.addCohort(cohort, cq.Name)
	}

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

	queued := m.queueAllInadmissibleWorkloadsInCohort(ctx, cqImpl)
	m.reportPendingWorkloads(cq.Name, cqImpl)
	if queued || addedWorkloads {
		m.Broadcast()
	}
	return nil
}

func (m *Manager) UpdateClusterQueue(ctx context.Context, cq *kueue.ClusterQueue) error {
	m.Lock()
	defer m.Unlock()
	cqImpl, ok := m.clusterQueues[cq.Name]
	if !ok {
		return errClusterQueueDoesNotExist
	}

	oldCohort := cqImpl.Cohort()
	// TODO(#8): recreate heap based on a change of queueing policy.
	if err := cqImpl.Update(cq); err != nil {
		return err
	}
	newCohort := cqImpl.Cohort()
	if oldCohort != newCohort {
		m.updateCohort(oldCohort, newCohort, cq.Name)
	}

	// TODO(#8): Selectively move workloads based on the exact event.
	if m.queueAllInadmissibleWorkloadsInCohort(ctx, cqImpl) {
		m.reportPendingWorkloads(cq.Name, cqImpl)
		m.Broadcast()
	}

	return nil
}

func (m *Manager) DeleteClusterQueue(cq *kueue.ClusterQueue) {
	m.Lock()
	defer m.Unlock()
	cqImpl := m.clusterQueues[cq.Name]
	if cqImpl == nil {
		return
	}
	delete(m.clusterQueues, cq.Name)
	metrics.ClearQueueSystemMetrics(cq.Name)

	cohort := cq.Spec.Cohort
	m.deleteCohort(cohort, cq.Name)
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
		w := w
		if workload.HasQuotaReservation(&w) {
			continue
		}
		pts, err := podtemplate.ExtractByWorkloadLabel(ctx, m.client, &w)
		if err != nil {
			continue
		}
		qImpl.AddOrUpdate(workload.NewInfo(&w, pts))
	}
	cq := m.clusterQueues[qImpl.ClusterQueue]
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
		return errQueueDoesNotExist
	}
	if qImpl.ClusterQueue != string(q.Spec.ClusterQueue) {
		oldCQ := m.clusterQueues[qImpl.ClusterQueue]
		if oldCQ != nil {
			oldCQ.DeleteFromLocalQueue(qImpl)
		}
		newCQ := m.clusterQueues[string(q.Spec.ClusterQueue)]
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
	cq := m.clusterQueues[qImpl.ClusterQueue]
	if cq != nil {
		cq.DeleteFromLocalQueue(qImpl)
	}
	delete(m.localQueues, key)
}

func (m *Manager) PendingWorkloads(q *kueue.LocalQueue) (int32, error) {
	m.RLock()
	defer m.RUnlock()

	qImpl, ok := m.localQueues[Key(q)]
	if !ok {
		return 0, errQueueDoesNotExist
	}

	return int32(len(qImpl.items)), nil
}

func (m *Manager) Pending(cq *kueue.ClusterQueue) int {
	m.RLock()
	defer m.RUnlock()
	return m.clusterQueues[cq.Name].Pending()
}

func (m *Manager) QueueForWorkloadExists(wl *kueue.Workload) bool {
	m.RLock()
	defer m.RUnlock()
	_, ok := m.localQueues[workload.QueueKey(wl)]
	return ok
}

// ClusterQueueForWorkload returns the name of the ClusterQueue where the
// workload should be queued and whether it exists.
// Returns empty string if the queue doesn't exist.
func (m *Manager) ClusterQueueForWorkload(wl *kueue.Workload) (string, bool) {
	m.RLock()
	defer m.RUnlock()
	q, ok := m.localQueues[workload.QueueKey(wl)]
	if !ok {
		return "", false
	}
	_, ok = m.clusterQueues[q.ClusterQueue]
	return q.ClusterQueue, ok
}

// AddOrUpdateWorkload adds or updates workload to the corresponding queue.
// Returns whether the queue existed.
func (m *Manager) AddOrUpdateWorkload(w *kueue.Workload, pts map[string]*corev1.PodTemplateSpec) bool {
	m.Lock()
	defer m.Unlock()
	return m.addOrUpdateWorkload(w, pts)
}

func (m *Manager) addOrUpdateWorkload(w *kueue.Workload, pts map[string]*corev1.PodTemplateSpec) bool {
	qKey := workload.QueueKey(w)
	q := m.localQueues[qKey]
	if q == nil {
		return false
	}
	wInfo := workload.NewInfo(w, pts)
	q.AddOrUpdate(wInfo)
	cq := m.clusterQueues[q.ClusterQueue]
	if cq == nil {
		return false
	}
	cq.PushOrUpdate(wInfo)
	m.reportPendingWorkloads(q.ClusterQueue, cq)
	m.Broadcast()
	return true
}

// RequeueWorkload requeues the workload ensuring that the queue and the
// workload still exist in the client cache and it's not admitted. It won't
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

	q := m.localQueues[workload.QueueKey(&w)]
	if q == nil {
		return false
	}
	pts, err := podtemplate.ExtractByWorkloadLabel(ctx, m.client, &w)
	if err != nil {
		return false
	}
	info.Update(&w, pts)
	q.AddOrUpdate(info)
	cq := m.clusterQueues[q.ClusterQueue]
	if cq == nil {
		return false
	}

	added := cq.RequeueIfNotPresent(info, reason)
	m.reportPendingWorkloads(q.ClusterQueue, cq)
	if added {
		m.Broadcast()
	}
	return added
}

func (m *Manager) DeleteWorkload(w *kueue.Workload) {
	m.Lock()
	m.deleteWorkloadFromQueueAndClusterQueue(w, workload.QueueKey(w))
	m.Unlock()
}

func (m *Manager) deleteWorkloadFromQueueAndClusterQueue(w *kueue.Workload, qKey string) {
	q := m.localQueues[qKey]
	if q == nil {
		return
	}
	delete(q.items, workload.Key(w))
	cq := m.clusterQueues[q.ClusterQueue]
	if cq != nil {
		cq.Delete(w)
		m.reportPendingWorkloads(q.ClusterQueue, cq)
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

	q := m.localQueues[workload.QueueKey(w)]
	if q == nil {
		return
	}
	cq := m.clusterQueues[q.ClusterQueue]
	if cq == nil {
		return
	}

	if m.queueAllInadmissibleWorkloadsInCohort(ctx, cq) {
		m.Broadcast()
	}
}

// QueueInadmissibleWorkloads moves all inadmissibleWorkloads in
// corresponding ClusterQueues to heap. If at least one workload queued,
// we will broadcast the event.
func (m *Manager) QueueInadmissibleWorkloads(ctx context.Context, cqNames sets.Set[string]) {
	m.Lock()
	defer m.Unlock()
	if len(cqNames) == 0 {
		return
	}

	var queued bool
	for name := range cqNames {
		cq, exists := m.clusterQueues[name]
		if !exists {
			continue
		}
		if m.queueAllInadmissibleWorkloadsInCohort(ctx, cq) {
			queued = true
		}
	}

	if queued {
		m.Broadcast()
	}
}

// queueAllInadmissibleWorkloadsInCohort moves all workloads in the same
// cohort with this ClusterQueue from inadmissibleWorkloads to heap. If the
// cohort of this ClusterQueue is empty, it just moves all workloads in this
// ClusterQueue. If at least one workload is moved, returns true. Otherwise
// returns false.
// The events listed below could make workloads in the same cohort admissible.
// Then queueAllInadmissibleWorkloadsInCohort need to be invoked.
// 1. delete events for any admitted workload in the cohort.
// 2. add events of any cluster queue in the cohort.
// 3. update events of any cluster queue in the cohort.
func (m *Manager) queueAllInadmissibleWorkloadsInCohort(ctx context.Context, cq ClusterQueue) bool {
	cohort := cq.Cohort()
	if cohort == "" {
		return cq.QueueInadmissibleWorkloads(ctx, m.client)
	}

	queued := false
	for cqName := range m.cohorts[cohort] {
		if clusterQueue, ok := m.clusterQueues[cqName]; ok {
			queued = clusterQueue.QueueInadmissibleWorkloads(ctx, m.client) || queued
		}
	}
	return queued
}

// UpdateWorkload updates the workload to the corresponding queue or adds it if
// it didn't exist. Returns whether the queue existed.
func (m *Manager) UpdateWorkload(oldW, w *kueue.Workload, pts map[string]*corev1.PodTemplateSpec) bool {
	m.Lock()
	defer m.Unlock()
	if oldW.Spec.QueueName != w.Spec.QueueName {
		m.deleteWorkloadFromQueueAndClusterQueue(w, workload.QueueKey(oldW))
	}
	return m.addOrUpdateWorkload(w, pts)
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

// Dump is a dump of the queues and it's elements (unordered).
// Only use for testing purposes.
func (m *Manager) Dump() map[string]sets.Set[string] {
	m.Lock()
	defer m.Unlock()
	if len(m.clusterQueues) == 0 {
		return nil
	}
	dump := make(map[string]sets.Set[string], len(m.clusterQueues))
	for key, cq := range m.clusterQueues {
		if elements, ok := cq.Dump(); ok {
			dump[key] = elements
		}
	}
	if len(dump) == 0 {
		return nil
	}
	return dump
}

// DumpInadmissible is a dump of the inadmissible workloads list.
// Only use for testing purposes.
func (m *Manager) DumpInadmissible() map[string]sets.Set[string] {
	m.Lock()
	defer m.Unlock()
	if len(m.clusterQueues) == 0 {
		return nil
	}
	dump := make(map[string]sets.Set[string], len(m.clusterQueues))
	for key, cq := range m.clusterQueues {
		if elements, ok := cq.DumpInadmissible(); ok {
			dump[key] = elements
		}
	}
	if len(dump) == 0 {
		return nil
	}
	return dump
}

func (m *Manager) heads() []workload.Info {
	var workloads []workload.Info
	for cqName, cq := range m.clusterQueues {
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
		q := m.localQueues[workload.QueueKey(wl.Obj)]
		delete(q.items, workload.Key(wl.Obj))
	}
	return workloads
}

func (m *Manager) addCohort(cohort string, cqName string) {
	if m.cohorts[cohort] == nil {
		m.cohorts[cohort] = make(sets.Set[string])
	}
	m.cohorts[cohort].Insert(cqName)
}

func (m *Manager) deleteCohort(cohort string, cqName string) {
	if cohort == "" {
		return
	}
	if m.cohorts[cohort] != nil {
		m.cohorts[cohort].Delete(cqName)
		if len(m.cohorts[cohort]) == 0 {
			delete(m.cohorts, cohort)
		}
	}
}

func (m *Manager) updateCohort(oldCohort string, newCohort string, cqName string) {
	m.deleteCohort(oldCohort, cqName)
	m.addCohort(newCohort, cqName)
}

func (m *Manager) Broadcast() {
	m.cond.Broadcast()
}

func (m *Manager) reportPendingWorkloads(cqName string, cq ClusterQueue) {
	active := cq.PendingActive()
	inadmissible := cq.PendingInadmissible()
	if m.statusChecker != nil && !m.statusChecker.ClusterQueueActive(cqName) {
		inadmissible += active
		active = 0
	}
	metrics.ReportPendingWorkloads(cqName, active, inadmissible)
}

func (m *Manager) GetClusterQueueNames() []string {
	m.RLock()
	defer m.RUnlock()
	clusterQueueNames := make([]string, 0, len(m.clusterQueues))
	for k := range m.clusterQueues {
		clusterQueueNames = append(clusterQueueNames, k)
	}
	return clusterQueueNames
}

func (m *Manager) getClusterQueue(cqName string) ClusterQueue {
	m.RLock()
	defer m.RUnlock()
	return m.clusterQueues[cqName]
}

// UpdateSnapshot computes the new snapshot and replaces if it differs from the
// previous version. It returns true if the snapshot was actually updated.
func (m *Manager) UpdateSnapshot(cqName string, maxCount int32) bool {
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

func (m *Manager) setSnapshot(cqName string, workloads []kueue.ClusterQueuePendingWorkload) {
	m.snapshotsMutex.Lock()
	defer m.snapshotsMutex.Unlock()
	m.snapshots[cqName] = workloads
}

func (m *Manager) GetSnapshot(cqName string) []kueue.ClusterQueuePendingWorkload {
	m.snapshotsMutex.RLock()
	defer m.snapshotsMutex.RUnlock()
	return m.snapshots[cqName]
}

func (m *Manager) DeleteSnapshot(cq *kueue.ClusterQueue) {
	m.snapshotsMutex.Lock()
	defer m.snapshotsMutex.Unlock()
	delete(m.snapshots, cq.Name)
}
