/*
Copyright 2022 Google LLC.

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
	"fmt"
	"sync"

	"k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
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

	if _, ok := m.queues[q.Name]; ok {
		return fmt.Errorf("queue %q already exists", q.Name)
	}
	qImpl := newQueue()
	qImpl.setProperties(q)
	m.queues[q.Name] = qImpl
	// Iterate through existing workloads, as workloads corresponding to this
	// queue might have been added earlier.
	var workloads kueue.QueuedWorkloadList
	if err := m.client.List(ctx, &workloads, client.MatchingFields{workloadQueueKey: q.Name}); err != nil {
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
	qImpl, ok := m.queues[q.Name]
	if !ok {
		return fmt.Errorf("queue %q doesn't exist", q.Name)
	}
	return qImpl.setProperties(q)
}

func (m *Manager) DeleteQueue(q *kueue.Queue) {
	m.Lock()
	defer m.Unlock()
	qImpl := m.queues[q.Name]
	if qImpl == nil {
		return
	}
	delete(m.queues, q.Name)
}

// AddWorkload adds workload to the corresponding queue. Returns whether the
// queue existed.
func (m *Manager) AddWorkload(w *kueue.QueuedWorkload) bool {
	m.Lock()
	defer m.Unlock()
	return m.addWorkload(w)
}

func (m *Manager) addWorkload(w *kueue.QueuedWorkload) bool {
	qName := w.Spec.QueueName
	if q := m.queues[qName]; q != nil {
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

	qName := info.Obj.Spec.QueueName
	q := m.queues[qName]
	if q == nil {
		return false
	}

	var w kueue.QueuedWorkload
	err := m.client.Get(ctx, client.ObjectKeyFromObject(info.Obj), &w)
	// Since the client is cached, the only possible error is NotFound
	if errors.IsNotFound(err) {
		return false
	}

	return q.PushIfNotPresent(info)
}

func (m *Manager) DeleteWorkload(w *kueue.QueuedWorkload) {
	m.Lock()
	m.deleteWorkloadFromQueue(w, w.Spec.QueueName)
	m.Unlock()
}

func (m *Manager) deleteWorkloadFromQueue(w *kueue.QueuedWorkload, qName string) {
	if q := m.queues[qName]; q != nil {
		q.Delete(w)
	}
}

// UpdateWorkload updates the workload to the corresponding queue or adds it if
// it didn't exist. Returns whether the queue existed.
func (m *Manager) UpdateWorkload(w *kueue.QueuedWorkload, prevQueue string) bool {
	m.Lock()
	defer m.Unlock()
	qName := w.Spec.QueueName
	if prevQueue != qName {
		m.deleteWorkloadFromQueue(w, prevQueue)
		return m.addWorkload(w)
	}

	if q := m.queues[qName]; q != nil {
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
