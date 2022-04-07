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
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestClusterQueueBestEffortFIFO(t *testing.T) {
	clusterQueue := utiltesting.MakeClusterQueue("cq").QueueingStrategy(
		kueue.BestEffortFIFO).Obj()
	var workloads = []*kueue.Workload{
		utiltesting.MakeWorkload("w1", "ns1").Queue("q1").Obj(),
		utiltesting.MakeWorkload("w2", "ns2").Queue("q2").Obj(),
	}
	var updatedWorkloads = make([]*kueue.Workload, len(workloads))

	updatedWorkloads[0] = workloads[0].DeepCopy()
	updatedWorkloads[0].Spec.QueueName = "q2"
	updatedWorkloads[1] = workloads[1].DeepCopy()
	updatedWorkloads[1].Spec.QueueName = "q1"

	tests := []struct {
		name                       string
		workloadsToAdd             []*kueue.Workload
		inadmissibleWorkloadsToAdd []*workload.Info
		workloadsToUpdate          []*kueue.Workload
		workloadsToDelete          []*kueue.Workload
		queueInadmissibleWorkloads bool
		wantWorkloads              sets.String
	}{
		{
			name:                       "add, update, delete workload",
			workloadsToAdd:             []*kueue.Workload{workloads[0], workloads[1]},
			inadmissibleWorkloadsToAdd: []*workload.Info{},
			workloadsToUpdate:          []*kueue.Workload{updatedWorkloads[0]},
			workloadsToDelete:          []*kueue.Workload{workloads[0]},
			queueInadmissibleWorkloads: false,
			wantWorkloads:              sets.NewString(workloads[1].Name),
		},
		{
			name:                       "update inadmissible workload",
			workloadsToAdd:             []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToAdd: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:          []*kueue.Workload{updatedWorkloads[1]},
			workloadsToDelete:          []*kueue.Workload{},
			queueInadmissibleWorkloads: false,
			wantWorkloads:              sets.NewString(workloads[0].Name, workloads[1].Name),
		},
		{
			name:                       "re-queue inadmissible workload",
			workloadsToAdd:             []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToAdd: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:          []*kueue.Workload{},
			workloadsToDelete:          []*kueue.Workload{},
			queueInadmissibleWorkloads: true,
			wantWorkloads:              sets.NewString(workloads[0].Name, workloads[1].Name),
		},
		{
			name:                       "delete inadmissible workload",
			workloadsToAdd:             []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToAdd: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:          []*kueue.Workload{},
			workloadsToDelete:          []*kueue.Workload{workloads[1]},
			queueInadmissibleWorkloads: true,
			wantWorkloads:              sets.NewString(workloads[0].Name),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cq, err := newClusterQueueBestEffortFIFO(clusterQueue)
			if err != nil {
				t.Fatalf("Failed creating ClusterQueue %v", err)
			}

			for _, w := range test.workloadsToAdd {
				cq.PushOrUpdate(w)
			}

			for _, w := range test.inadmissibleWorkloadsToAdd {
				cq.RequeueIfNotPresent(w, true)
			}

			for _, w := range test.workloadsToUpdate {
				cq.PushOrUpdate(w)
			}

			for _, w := range test.workloadsToDelete {
				cq.Delete(w)
			}

			if test.queueInadmissibleWorkloads {
				cq.QueueInadmissibleWorkloads()
			}

			gotWorkloads, _ := cq.Dump()
			if diff := cmp.Diff(test.wantWorkloads, gotWorkloads); diff != "" {
				t.Errorf("Unexpected items in cluster foo (-want,+got):\n%s", diff)
			}
		})
	}
}
