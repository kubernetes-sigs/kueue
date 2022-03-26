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

	"k8s.io/apimachinery/pkg/util/sets"
	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueue is an interface for a cluster queue to store workloads waiting
// to be scheduled.
type ClusterQueue interface {
	// Update updates the properties of this ClusterQueue.
	Update(*kueue.ClusterQueue)

	// AddFromQueue pushes all workloads belonging to this queue to
	// the ClusterQueue. If at least one workload is added, returns true.
	// Otherwise returns false.
	AddFromQueue(*Queue) bool
	// DeleteFromQueue removes all workloads belonging to this queue from
	// the ClusterQueue.
	DeleteFromQueue(*Queue)

	// PushIfNotPresent pushes the workload to ClusterQueue.
	// If the workload is already present, returns false. Otherwise returns true.
	PushIfNotPresent(*workload.Info) bool
	// PushOrUpdate pushes the workload to ClusterQueue.
	// If the workload is already present, updates with the new one.
	PushOrUpdate(*kueue.QueuedWorkload)
	// Delete removes the workload from ClusterQueue.
	Delete(*kueue.QueuedWorkload)
	// Pop removes the head of the queue and returns it. It returns nil if the
	// queue is empty.
	Pop() *workload.Info

	// Pending returns the number of pending workloads.
	Pending() int32
	// Dump produces a dump of the current workloads in the heap of
	// this ClusterQueue. It returns false if the queue is empty.
	// Otherwise returns true.
	Dump() (sets.String, bool)
}

var registry = map[kueue.QueueingStrategy]func(cq *kueue.ClusterQueue) (ClusterQueue, error){
	StrictFIFO: newClusterQueueStrictFIFO,
}

func newClusterQueue(cq *kueue.ClusterQueue) (ClusterQueue, error) {
	strategy := cq.Spec.QueueingStrategy
	f, exist := registry[strategy]
	if !exist {
		return nil, fmt.Errorf("invalid QueueingStrategy %q", cq.Spec.QueueingStrategy)
	}
	return f(cq)
}
