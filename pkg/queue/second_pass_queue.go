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

	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/workload"
)

type secondPassQueue struct {
	sync.RWMutex

	prequeued sets.Set[workload.Reference]
	queued    map[workload.Reference]*workload.Info
}

func newSecondPassQueue() *secondPassQueue {
	return &secondPassQueue{
		prequeued: sets.New[workload.Reference](),
		queued:    make(map[workload.Reference]*workload.Info),
	}
}

func (q *secondPassQueue) takeAllReady() []workload.Info {
	q.Lock()
	defer q.Unlock()

	var result []workload.Info
	for _, v := range q.queued {
		result = append(result, *v)
	}
	q.queued = make(map[workload.Reference]*workload.Info)
	return result
}

func (q *secondPassQueue) prequeue(obj *kueue.Workload) {
	q.Lock()
	defer q.Unlock()

	q.prequeued.Insert(workload.Key(obj))
}

func (q *secondPassQueue) queue(w *workload.Info) bool {
	q.Lock()
	defer q.Unlock()

	key := workload.Key(w.Obj)
	enqueued := q.prequeued.Has(key) && workload.NeedsSecondPass(w.Obj)
	if enqueued {
		q.queued[key] = w
	}
	q.prequeued.Delete(key)
	return enqueued
}

func (q *secondPassQueue) deleteByKey(key workload.Reference) {
	q.Lock()
	defer q.Unlock()

	delete(q.queued, key)
	q.prequeued.Delete(key)
}
