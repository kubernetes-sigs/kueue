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
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/wait"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	initialBackoff = time.Second
	backoffFactor  = 2
	maxBackoff     = 30 * time.Second
)

type secondPassPending struct {
	uid           types.UID
	prequeueIndex uint64
}

type secondPassQueue struct {
	sync.RWMutex

	prequeued         map[workload.Reference]secondPassPending
	lastPrequeueIndex uint64
	queued            map[workload.Reference]*workload.Info

	backoff wait.Backoff
}

func newSecondPassQueue() *secondPassQueue {
	return &secondPassQueue{
		prequeued: make(map[workload.Reference]secondPassPending),
		queued:    make(map[workload.Reference]*workload.Info),
		backoff:   wait.NewBackoff(initialBackoff, maxBackoff, backoffFactor, 0),
	}
}

// takeAllReady removes and returns all workloads currently queued for the second pass.
func (q *secondPassQueue) takeAllReady() []workload.Info {
	q.Lock()
	defer q.Unlock()
	result := make([]workload.Info, 0, len(q.queued))

	for _, v := range q.queued {
		result = append(result, *v)
	}

	q.queued = make(map[workload.Reference]*workload.Info)
	return result
}

func (q *secondPassQueue) prequeue(obj *kueue.Workload) secondPassPending {
	q.Lock()
	defer q.Unlock()

	key := workload.Key(obj)
	q.lastPrequeueIndex++
	pending := secondPassPending{uid: obj.UID, prequeueIndex: q.lastPrequeueIndex}
	q.prequeued[key] = pending
	delete(q.queued, key)
	return pending
}

func (q *secondPassQueue) isPending(key workload.Reference, pending secondPassPending) bool {
	q.RLock()
	defer q.RUnlock()

	current, found := q.prequeued[key]
	return found && current == pending
}

// queue consumes pending only if its UID and index still match the current request
// and w has the same UID. Superseded callbacks leave both queues unchanged.
// A matching request is removed even if the refreshed workload no longer needs a
// second pass. It returns true only when the workload is added to the ready queue.
func (q *secondPassQueue) queue(w *workload.Info, pending secondPassPending) bool {
	q.Lock()
	defer q.Unlock()

	key := workload.Key(w.Obj)
	current, prequeued := q.prequeued[key]
	matchesPrequeued := prequeued && current == pending && pending.uid == w.Obj.UID
	enqueued := matchesPrequeued && workload.NeedsSecondPass(w.Obj)
	if enqueued {
		q.queued[key] = w
	}
	if matchesPrequeued {
		delete(q.prequeued, key)
	}
	return enqueued
}

func (q *secondPassQueue) deletePending(key workload.Reference, pending secondPassPending) bool {
	q.Lock()
	defer q.Unlock()

	if current, found := q.prequeued[key]; !found || current != pending {
		return false
	}
	delete(q.prequeued, key)
	return true
}

func (q *secondPassQueue) deleteByKey(key workload.Reference) {
	q.Lock()
	defer q.Unlock()

	delete(q.queued, key)
	delete(q.prequeued, key)
}

func (q *secondPassQueue) deleteByKeyIfUID(key workload.Reference, uid types.UID) {
	q.Lock()
	defer q.Unlock()

	if queued, found := q.queued[key]; found && queued.Obj.UID == uid {
		delete(q.queued, key)
	}
	if pending, found := q.prequeued[key]; found && pending.uid == uid {
		delete(q.prequeued, key)
	}
}

func (q *secondPassQueue) nextDelay(iteration int) time.Duration {
	return q.backoff.WaitTime(iteration)
}
