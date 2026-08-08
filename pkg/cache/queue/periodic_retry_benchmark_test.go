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
	"fmt"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func BenchmarkNotifyRetryAllClusterQueues(b *testing.B) {
	for _, clusterQueueCount := range []int{10, 100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("clusterqueues-%d", clusterQueueCount), func(b *testing.B) {
			manager, requeuer := NewManagerForUnitTestsWithRequeuer(nil, nil)
			for i := range clusterQueueCount {
				cq := newClusterQueueImpl(b.Context(), nil, workload.Ordering{}, realClock)
				cq.name = kueue.ClusterQueueReference(fmt.Sprintf("cq-%d", i))
				manager.hm.AddClusterQueue(cq)
			}

			b.ResetTimer()
			for b.Loop() {
				NotifyRetryInadmissible(
					manager,
					sets.New(manager.GetClusterQueueNames()...),
				)
				requeuer.cqs.Clear()
			}
		})
	}
}
