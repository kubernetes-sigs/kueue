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

type secondPassQueue struct {
	sync.RWMutex

	prequeued map[workload.Reference]types.UID
	queued    map[workload.Reference]*workload.Info

	backoff wait.Backoff
}

func newSecondPassQueue() *secondPassQueue {
	return &secondPassQueue{
		prequeued: make(map[workload.Reference]types.UID),
		queued:    make(map[workload.Reference]*workload.Info),
		backoff:   wait.NewBackoff(initialBackoff, maxBackoff, backoffFactor, 0),
	}
}

func (q *secondPassQueue) takeAllReady() []Head {
	q.Lock()
	defer q.Unlock()

	var result []Head
	for _, v := range q.queued {
		result = append(result, Head{Info: *v})
	}
	q.queued = make(map[workload.Reference]*workload.Info)
	return result
}

func (q *secondPassQueue) prequeueIfAbsent(obj *kueue.Workload) bool {
	q.Lock()
	defer q.Unlock()

	key := workload.Key(obj)
	if uid, found := q.prequeued[key]; found && uid == obj.UID {
		return false
	}
	if queued, found := q.queued[key]; found && queued.Obj.UID != obj.UID {
		delete(q.queued, key)
	}
	q.prequeued[key] = obj.UID
	return true
}

func (q *secondPassQueue) queue(w *workload.Info) bool {
	q.Lock()
	defer q.Unlock()

	key := workload.Key(w.Obj)
	uid, prequeued := q.prequeued[key]
	matchesPrequeued := prequeued && uid == w.Obj.UID
	enqueued := matchesPrequeued && workload.NeedsSecondPass(w.Obj)
	if enqueued {
		q.queued[key] = w
	}
	if matchesPrequeued {
		delete(q.prequeued, key)
	}
	return enqueued
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
	if prequeuedUID, found := q.prequeued[key]; found && prequeuedUID == uid {
		delete(q.prequeued, key)
	}
}

func (q *secondPassQueue) nextDelay(iteration int) time.Duration {
	return q.backoff.WaitTime(iteration)
}
