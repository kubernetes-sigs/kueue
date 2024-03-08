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
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	realClock = clock.RealClock{}
)

// ClusterQueueStrictFIFO is the implementation for the ClusterQueue for
// StrictFIFO.
type ClusterQueueStrictFIFO struct {
	*clusterQueueBase
}

var _ ClusterQueue = &ClusterQueueStrictFIFO{}

func newClusterQueueStrictFIFO(cq *kueue.ClusterQueue, wo workload.Ordering) (ClusterQueue, error) {
	cqImpl := newClusterQueueImpl(queueOrderingFunc(wo), realClock)
	cqStrict := &ClusterQueueStrictFIFO{
		clusterQueueBase: cqImpl,
	}

	err := cqStrict.Update(cq)
	return cqStrict, err
}

// queueOrderingFunc returns a function used by the clusterQueue heap algorithm
// to sort workloads. The function sorts workloads based on their priority.
// When priorities are equal, it uses the workload's creation or eviction
// time.
func queueOrderingFunc(wo workload.Ordering) func(a, b *workload.Info) bool {
	return func(a, b *workload.Info) bool {
		p1 := utilpriority.Priority(a.Obj)
		p2 := utilpriority.Priority(b.Obj)

		if p1 != p2 {
			return p1 > p2
		}

		tA := wo.GetQueueOrderTimestamp(a.Obj)
		tB := wo.GetQueueOrderTimestamp(b.Obj)
		return !tB.Before(tA)
	}
}

// RequeueIfNotPresent requeues if the workload is not present.
// If the reason for requeue is that the workload doesn't match the CQ's
// namespace selector, then the requeue is not immediate.
func (cq *ClusterQueueStrictFIFO) RequeueIfNotPresent(wInfo *workload.Info, reason RequeueReason) bool {
	return cq.requeueIfNotPresent(wInfo, reason != RequeueReasonNamespaceMismatch)
}
