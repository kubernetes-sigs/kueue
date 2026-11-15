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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

// LocalQueue is the internal implementation of kueue.LocalQueue.
type LocalQueue struct {
	Key          queue.LocalQueueReference
	ClusterQueue kueue.ClusterQueueReference

	items map[workload.Reference]*workload.Info
	// variantsPerParent counts the ConcurrentAdmission Variants in items per
	// Parent UID, so the pending count can count each Parent once.
	variantsPerParent map[types.UID]int

	finishedWorkloads sets.Set[workload.Reference]

	labels map[string]string
}

func newLocalQueue(q *kueue.LocalQueue) *LocalQueue {
	qImpl := &LocalQueue{
		Key:               queue.Key(q),
		items:             make(map[workload.Reference]*workload.Info),
		variantsPerParent: make(map[types.UID]int),
		finishedWorkloads: sets.New[workload.Reference](),
		labels:            q.GetLabels(),
	}
	qImpl.update(q)
	return qImpl
}

func (q *LocalQueue) update(apiQueue *kueue.LocalQueue) {
	q.ClusterQueue = apiQueue.Spec.ClusterQueue
	q.labels = apiQueue.GetLabels()
}

func (q *LocalQueue) AddOrUpdate(info *workload.Info) {
	key := workload.Key(info.Obj)
	if old, ok := q.items[key]; ok {
		removeVariant(q.variantsPerParent, old)
	}
	q.items[key] = info
	addVariant(q.variantsPerParent, info)
}

func (q *LocalQueue) delete(key workload.Reference) {
	if info, ok := q.items[key]; ok {
		removeVariant(q.variantsPerParent, info)
		delete(q.items, key)
	}
}

// pendingCount returns the number of workloads in the LocalQueue, counting
// the Variants of each ConcurrentAdmission Parent once.
func (q *LocalQueue) pendingCount() int {
	excess, _ := variantExcess(q.variantsPerParent, nil, nil)
	return len(q.items) - excess
}
