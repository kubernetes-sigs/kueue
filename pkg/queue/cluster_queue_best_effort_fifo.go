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
	"k8s.io/apimachinery/pkg/api/equality"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueBestEffortFIFO is the implementation for the ClusterQueue for
// BestEffortFIFO.
type ClusterQueueBestEffortFIFO struct {
	*ClusterQueueImpl

	// inadmissibleWorkloads are workloads that have been tried at least once and couldn't be admitted.
	inadmissibleWorkloads map[string]*workload.Info
}

var _ ClusterQueue = &ClusterQueueBestEffortFIFO{}

const BestEffortFIFO = kueue.BestEffortFIFO

func newClusterQueueBestEffortFIFO(cq *kueue.ClusterQueue) (ClusterQueue, error) {
	cqImpl := ClusterQueueImpl{
		heap: heapImpl{
			less:  byCreationTime,
			items: make(map[string]*heapItem),
		},
	}

	cqBE := &ClusterQueueBestEffortFIFO{
		ClusterQueueImpl:      &cqImpl,
		inadmissibleWorkloads: make(map[string]*workload.Info),
	}

	cqBE.Update(cq)
	return cqBE, nil
}

func (cq *ClusterQueueBestEffortFIFO) PushOrUpdate(w *kueue.Workload) {
	key := workload.Key(w)
	oldInfo := cq.inadmissibleWorkloads[key]
	if oldInfo != nil {
		// update in place if the workload was inadmissible and didn't change
		// to potentially become admissible.
		if equality.Semantic.DeepEqual(oldInfo.Obj.Spec, w.Spec) {
			cq.inadmissibleWorkloads[key] = workload.NewInfo(w)
			return
		}
		// otherwise move or update in place in the queue.
		delete(cq.inadmissibleWorkloads, key)
	}

	cq.ClusterQueueImpl.PushOrUpdate(w)
}

func (cq *ClusterQueueBestEffortFIFO) Delete(w *kueue.Workload) {
	delete(cq.inadmissibleWorkloads, workload.Key(w))
	cq.ClusterQueueImpl.Delete(w)
}

// RequeueIfNotPresent inserts a workload that cannot be admitted into
// ClusterQueue, unless it is already in the queue. If inadmissible is true,
// the workload will be put into the inadmissibleWorkloads. If not,
// the workload will be pushed back to heap directly.
func (cq *ClusterQueueBestEffortFIFO) RequeueIfNotPresent(wInfo *workload.Info, inadmissible bool) bool {
	if !inadmissible {
		return cq.ClusterQueueImpl.pushIfNotPresent(wInfo)
	}

	key := workload.Key(wInfo.Obj)
	if cq.inadmissibleWorkloads[key] != nil {
		return false
	}

	if item := cq.heap.items[key]; item != nil {
		return false
	}

	cq.inadmissibleWorkloads[key] = wInfo

	return true
}

// QueueInadmissibleWorkloads moves all workloads from inadmissibleWorkloads to heap.
// If at least one workload is moved, returns true. Otherwise returns false.
func (cq *ClusterQueueBestEffortFIFO) QueueInadmissibleWorkloads() bool {
	if len(cq.inadmissibleWorkloads) == 0 {
		return false
	}

	for _, wInfo := range cq.inadmissibleWorkloads {
		cq.ClusterQueueImpl.pushIfNotPresent(wInfo)
	}

	cq.inadmissibleWorkloads = make(map[string]*workload.Info)
	return true
}
