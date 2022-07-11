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
	"fmt"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

func keyFunc(obj interface{}) string {
	i := obj.(*workload.Info)
	return workload.Key(i.Obj)
}

// Key is the key used to index the queue.
func Key(q *kueue.Queue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}

// Queue is the internal implementation of kueue.Queue.
type Queue struct {
	Key          string
	ClusterQueue string

	items map[string]*workload.Info
}

func newQueue(q *kueue.Queue) *Queue {
	qImpl := &Queue{
		Key:   Key(q),
		items: make(map[string]*workload.Info),
	}
	qImpl.update(q)
	return qImpl
}

func (q *Queue) update(apiQueue *kueue.Queue) {
	q.ClusterQueue = string(apiQueue.Spec.ClusterQueue)
}

func (q *Queue) AddOrUpdate(info *workload.Info) {
	key := workload.Key(info.Obj)
	q.items[key] = info
}

func (q *Queue) AddIfNotPresent(w *workload.Info) bool {
	key := workload.Key(w.Obj)
	_, ok := q.items[key]
	if !ok {
		q.items[key] = w
		return true
	}
	return false
}
