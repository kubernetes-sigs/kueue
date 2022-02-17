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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

const workloadQueueKey = "spec.queueName"

type Manager struct {
	sync.Mutex
	cond sync.Cond

	client client.Client
	queues map[string]*Queue
}

func NewManager(client client.Client) *Manager {
	m := &Manager{
		client: client,
		queues: make(map[string]*Queue),
	}
	m.cond.L = &m.Mutex
	return m
}

func (m *Manager) AddQueue(ctx context.Context, q *kueue.Queue) error {
	m.Lock()
	defer m.Unlock()

	key := Key(q)
	if _, ok := m.queues[key]; ok {
		return fmt.Errorf("queue %q already exists", q.Name)
	}
	qImpl := newQueue()
	qImpl.setProperties(q)
	m.queues[key] = qImpl
	// Iterate through existing workloads, as workloads corresponding to this
	// queue might have been added earlier.
	var workloads kueue.QueuedWorkloadList
	if err := m.client.List(ctx, &workloads, client.MatchingFields{workloadQueueKey: q.Name}, client.InNamespace(q.Namespace)); err != nil {
		return fmt.Errorf("listing workloads that match the queue: %w", err)
	}
	addedWorkloads := false
	for i, w := range workloads.Items {
		// Checking queue name again because the field index is not available in tests.
		if w.Spec.QueueName != q.Name || w.Spec.AssignedCapacity != "" {
			continue
		}
		qImpl.PushOrUpdate(&workloads.Items[i])
		addedWorkloads = true
	}
	if addedWorkloads {
		m.cond.Broadcast()
	}
	return nil
}

func (m *Manager) UpdateQueue(q *kueue.Queue) error {
	m.Lock()
	defer m.Unlock()
	qImpl, ok := m.queues[Key(q)]
	if !ok {
		return errors.New("queue doesn't exist")
	}
	return qImpl.setProperties(q)
}

func (m *Manager) DeleteQueue(q *kueue.Queue) {
	m.Lock()
	defer m.Unlock()
	key := Key(q)
	qImpl := m.queues[key]
	if qImpl == nil {
		return
	}
	delete(m.queues, key)
}

// AddOrUpdateWorkload adds or updates workload to the corresponding queue.
// Returns whether the queue existed.
func (m *Manager) AddOrUpdateWorkload(w *kueue.QueuedWorkload) bool {
	m.Lock()
	defer m.Unlock()
	return m.addOrUpdateWorkload(w)
}

func (m *Manager) addOrUpdateWorkload(w *kueue.QueuedWorkload) bool {
	qKey := keyForWorkload(w)
	if q := m.queues[qKey]; q != nil {
		q.PushOrUpdate(w)
		m.cond.Broadcast()
		return true
	}
	return false
}

// RequeueWorkload requeues the workload ensuring that the queue and the
// workload still exist in the client cache. It won't requeue if the workload
// is already in the queue (possible if the workload was updated).
func (m *Manager) RequeueWorkload(ctx context.Context, info *workload.Info) bool {
	m.Lock()
	defer m.Unlock()

	q := m.queues[keyForWorkload(info.Obj)]
	if q == nil {
		return false
	}

	var w kueue.QueuedWorkload
	err := m.client.Get(ctx, client.ObjectKeyFromObject(info.Obj), &w)
	// Since the client is cached, the only possible error is NotFound
	if apierrors.IsNotFound(err) {
		return false
	}

	return q.PushIfNotPresent(info)
}

func (m *Manager) DeleteWorkload(w *kueue.QueuedWorkload) {
	m.Lock()
	m.deleteWorkloadFromQueue(w, keyForWorkload(w))
	m.Unlock()
}

func (m *Manager) deleteWorkloadFromQueue(w *kueue.QueuedWorkload, qKey string) {
	if q := m.queues[qKey]; q != nil {
		q.Delete(w)
	}
}

// UpdateWorkload updates the workload to the corresponding queue or adds it if
// it didn't exist. Returns whether the queue existed.
func (m *Manager) UpdateWorkload(oldW, w *kueue.QueuedWorkload) bool {
	m.Lock()
	defer m.Unlock()
	if oldW.Spec.QueueName != w.Spec.QueueName {
		m.deleteWorkloadFromQueue(w, keyForWorkload(oldW))
		return m.addOrUpdateWorkload(w)
	}

	if q := m.queues[keyForWorkload(w)]; q != nil {
		q.PushOrUpdate(w)
		return true
	}
	return false
}

// CleanUpOnContext tracks the context. When closed, it wakes routines waiting
// on elements to be available. It should be called before doing any calls to
// Heads.
func (m *Manager) CleanUpOnContext(ctx context.Context) {
	<-ctx.Done()
	m.cond.Broadcast()
}

// Heads returns the heads of the queues, along with their associated capacity.
// It blocks if the queues empty until they have elements or the context terminates.
func (m *Manager) Heads(ctx context.Context) []workload.Info {
	m.Lock()
	defer m.Unlock()
	for {
		workloads := m.heads()
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
func (m *Manager) Dump() map[string]sets.String {
	m.Lock()
	defer m.Unlock()
	if len(m.queues) == 0 {
		return nil
	}
	dump := make(map[string]sets.String, len(m.queues))
	for key, q := range m.queues {
		if len(q.heap.items) == 0 {
			continue
		}
		elements := make(sets.String, len(q.heap.items))
		for _, e := range q.heap.items {
			elements.Insert(e.obj.Obj.Name)
		}
		dump[key] = elements
	}
	if len(dump) == 0 {
		return nil
	}
	return dump
}

func (m *Manager) heads() []workload.Info {
	var workloads []workload.Info
	for _, q := range m.queues {
		wl := q.Pop()
		if wl != nil {
			wlCopy := *wl
			wlCopy.Capacity = q.Capacity
			workloads = append(workloads, wlCopy)
		}
	}
	return workloads
}

func SetupIndexes(indexer client.FieldIndexer) error {
	return indexer.IndexField(context.Background(), &kueue.QueuedWorkload{}, workloadQueueKey, func(o client.Object) []string {
		wl := o.(*kueue.QueuedWorkload)
		return []string{wl.Spec.QueueName}
	})
}
