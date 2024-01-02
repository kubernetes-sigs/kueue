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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueBestEffortFIFO is the implementation for the ClusterQueue for
// BestEffortFIFO.
type ClusterQueueBestEffortFIFO struct {
	*clusterQueueBase
}

var _ ClusterQueue = &ClusterQueueBestEffortFIFO{}

func newClusterQueueBestEffortFIFO(cq *kueue.ClusterQueue, wo workload.Ordering) (ClusterQueue, error) {
	cqImpl := newClusterQueueImpl(keyFunc, queueOrderingFunc(wo))
	cqBE := &ClusterQueueBestEffortFIFO{
		clusterQueueBase: cqImpl,
	}

	err := cqBE.Update(cq)
	return cqBE, err
}

func (cq *ClusterQueueBestEffortFIFO) RequeueIfNotPresent(wInfo *workload.Info, reason RequeueReason) bool {
	return cq.requeueIfNotPresent(wInfo, reason == RequeueReasonFailedAfterNomination || reason == RequeueReasonPendingPreemption)
}
