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
	"fmt"
	"strings"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/workload"
)

// LocalQueueReference is the full reference to LocalQueue formed as <namespace>/< kueue.LocalQueueName >.
type LocalQueueReference string

func NewLocalQueueReference(namespace string, name kueue.LocalQueueName) LocalQueueReference {
	return LocalQueueReference(namespace + "/" + string(name))
}

func ParseLocalQueueReference(ref LocalQueueReference) (string, kueue.LocalQueueName, error) {
	parts := strings.Split(string(ref), "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid LocalQueueReference %s", ref)
	}
	return parts[0], kueue.LocalQueueName(parts[1]), nil
}

func MustParseLocalQueueReference(ref LocalQueueReference) (string, kueue.LocalQueueName) {
	namespace, name, err := ParseLocalQueueReference(ref)
	if err != nil {
		panic(err)
	}
	return namespace, name
}

// Key is the key used to index the queue.
func Key(q *kueue.LocalQueue) LocalQueueReference {
	return NewLocalQueueReference(q.Namespace, kueue.LocalQueueName(q.Name))
}

func KeyFromWorkload(w *kueue.Workload) LocalQueueReference {
	return NewLocalQueueReference(w.Namespace, w.Spec.QueueName)
}

func DefaultQueueKey(namespace string) LocalQueueReference {
	return NewLocalQueueReference(namespace, constants.DefaultLocalQueueName)
}

// LocalQueue is the internal implementation of kueue.LocalQueue.
type LocalQueue struct {
	Key          LocalQueueReference
	ClusterQueue kueue.ClusterQueueReference

	items map[workload.Reference]*workload.Info
}

func newLocalQueue(q *kueue.LocalQueue) *LocalQueue {
	qImpl := &LocalQueue{
		Key:   Key(q),
		items: make(map[workload.Reference]*workload.Info),
	}
	qImpl.update(q)
	return qImpl
}

func (q *LocalQueue) update(apiQueue *kueue.LocalQueue) {
	q.ClusterQueue = apiQueue.Spec.ClusterQueue
}

func (q *LocalQueue) AddOrUpdate(info *workload.Info) {
	key := workload.Key(info.Obj)
	q.items[key] = info
}

func (m *Manager) PendingActiveInLocalQueue(lq *LocalQueue) int {
	c, ok := m.getClusterQueueLockless(lq.ClusterQueue)
	result := 0
	if !ok {
		return 0
	}
	for _, wl := range c.heap.List() {
		wlLqKey := KeyFromWorkload(wl.Obj)
		if wlLqKey == lq.Key {
			result++
		}
	}
	if c.inflight != nil && string(workloadKey(c.inflight)) == string(lq.Key) {
		result++
	}
	return result
}

func (m *Manager) PendingInadmissibleInLocalQueue(lq *LocalQueue) int {
	c, ok := m.getClusterQueueLockless(lq.ClusterQueue)
	if !ok {
		return 0
	}
	result := 0
	for _, wl := range c.inadmissibleWorkloads {
		wlLqKey := KeyFromWorkload(wl.Obj)
		if wlLqKey == lq.Key {
			result++
		}
	}
	return result
}
