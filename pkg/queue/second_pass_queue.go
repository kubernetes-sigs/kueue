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

	"sigs.k8s.io/kueue/pkg/workload"
)

type secondPassQueue struct {
	sync.RWMutex

	state map[string]*workload.Info
}

func newSecondPassQueue() *secondPassQueue {
	return &secondPassQueue{
		state: make(map[string]*workload.Info),
	}
}

func (q *secondPassQueue) takeAllReady() []workload.Info {
	q.Lock()
	defer q.Unlock()

	var result []workload.Info
	for _, v := range q.state {
		result = append(result, *v)
	}
	q.state = make(map[string]*workload.Info)
	return result
}

func (q *secondPassQueue) offer(w *workload.Info) {
	q.Lock()
	defer q.Unlock()

	q.state[workload.Key(w.Obj)] = w
}

func (q *secondPassQueue) deleteByKey(key string) {
	q.Lock()
	defer q.Unlock()

	delete(q.state, key)
}
