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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueStrictFIFO is the implementation for the ClusterQueue for
// StrictFIFO.
type ClusterQueueStrictFIFO struct {
	*ClusterQueueImpl
}

var _ ClusterQueue = &ClusterQueueStrictFIFO{}

const StrictFIFO = kueue.StrictFIFO

func newClusterQueueStrictFIFO(cq *kueue.ClusterQueue) (ClusterQueue, error) {
	cqImpl := newClusterQueueImpl(keyFunc, byCreationTime)
	cqStrict := &ClusterQueueStrictFIFO{
		ClusterQueueImpl: cqImpl,
	}

	err := cqStrict.Update(cq)
	return cqStrict, err
}

// byCreationTime is the function used by the clusterQueue heap algorithm to sort
// workloads. It sorts workloads based on their priority.
// When priorities are equal, it uses workloads.creationTimestamp.
func byCreationTime(a, b interface{}) bool {
	objA := a.(*workload.Info)
	objB := b.(*workload.Info)
	p1 := utilpriority.Priority(objA.Obj)
	p2 := utilpriority.Priority(objB.Obj)

	if p1 != p2 {
		return p1 > p2
	}
	return objA.Obj.CreationTimestamp.Before(&objB.Obj.CreationTimestamp)
}
