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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestClusterQueueBestEffortFIFO(t *testing.T) {
	clusterQueue := utiltesting.MakeClusterQueue("cq").Obj()
	var workloads = []*kueue.Workload{
		utiltesting.MakeWorkload("w1", "ns1").Queue("q1").Obj(),
		utiltesting.MakeWorkload("w2", "ns2").Queue("q2").Obj(),
	}
	var updatedWorkloads = make([]*kueue.Workload, len(workloads))

	updatedWorkloads[0] = workloads[0].DeepCopy()
	updatedWorkloads[0].Spec.QueueName = "q2"
	updatedWorkloads[1] = workloads[1].DeepCopy()
	updatedWorkloads[1].Spec.QueueName = "q1"

	tests := map[string]struct {
		workloadsToAdd                 []*kueue.Workload
		inadmissibleWorkloadsToRequeue []*workload.Info
		admissibleWorkloadsToRequeue   []*workload.Info
		workloadsToUpdate              []*kueue.Workload
		workloadsToDelete              []*kueue.Workload
		queueInadmissibleWorkloads     bool
		wantActiveWorkloads            sets.String
		wantPending                    int32
	}{
		"add, update, delete workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0], workloads[1]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[0]},
			workloadsToDelete:              []*kueue.Workload{workloads[0]},
			wantActiveWorkloads:            sets.NewString(workloads[1].Name),
			wantPending:                    1,
		},
		"re-queue inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			wantActiveWorkloads:            sets.NewString(workloads[0].Name),
			wantPending:                    2,
		},
		"re-queue admissible workload that was inadmissible": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			admissibleWorkloadsToRequeue:   []*workload.Info{workload.NewInfo(workloads[1])},
			wantActiveWorkloads:            sets.NewString(workloads[0].Name, workloads[1].Name),
			wantPending:                    2,
		},
		"re-queue inadmissible workload and flush": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            sets.NewString(workloads[0].Name, workloads[1].Name),
			wantPending:                    2,
		},
		"update inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToUpdate:              []*kueue.Workload{updatedWorkloads[1]},
			wantActiveWorkloads:            sets.NewString(workloads[0].Name, workloads[1].Name),
			wantPending:                    2,
		},
		"delete inadmissible workload": {
			workloadsToAdd:                 []*kueue.Workload{workloads[0]},
			inadmissibleWorkloadsToRequeue: []*workload.Info{workload.NewInfo(workloads[1])},
			workloadsToDelete:              []*kueue.Workload{workloads[1]},
			queueInadmissibleWorkloads:     true,
			wantActiveWorkloads:            sets.NewString(workloads[0].Name),
			wantPending:                    1,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			cq, err := newClusterQueueBestEffortFIFO(clusterQueue)
			if err != nil {
				t.Fatalf("Failed creating ClusterQueue %v", err)
			}

			for _, w := range test.workloadsToAdd {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.inadmissibleWorkloadsToRequeue {
				cq.RequeueIfNotPresent(w, false)
			}
			for _, w := range test.admissibleWorkloadsToRequeue {
				cq.RequeueIfNotPresent(w, true)
			}

			for _, w := range test.workloadsToUpdate {
				cq.PushOrUpdate(workload.NewInfo(w))
			}

			for _, w := range test.workloadsToDelete {
				cq.Delete(w)
			}

			if test.queueInadmissibleWorkloads {
				cq.QueueInadmissibleWorkloads()
			}

			gotWorkloads, _ := cq.Dump()
			if diff := cmp.Diff(test.wantActiveWorkloads, gotWorkloads); diff != "" {
				t.Errorf("Unexpected items in cluster foo (-want,+got):\n%s", diff)
			}
			if got := cq.Pending(); got != test.wantPending {
				t.Errorf("Got %d pending workloads, want %d", got, test.wantPending)
			}
		})
	}
}

func TestDeleteFromQueue(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("cq").Obj()
	cqImpl, err := newClusterQueueBestEffortFIFO(cq)
	if err != nil {
		t.Fatalf("Failed creating ClusterQueue %v", err)
	}
	q := utiltesting.MakeQueue("foo", "").ClusterQueue(cq.Name).Obj()
	qImpl := newQueue(q)
	wl1 := utiltesting.MakeWorkload("wl1", "").Queue(q.Name).Obj()
	wl2 := utiltesting.MakeWorkload("wl2", "").Queue(q.Name).Obj()
	wl3 := utiltesting.MakeWorkload("wl3", "").Queue(q.Name).Obj()
	wl4 := utiltesting.MakeWorkload("wl4", "").Queue(q.Name).Obj()
	admissibleworkloads := []*kueue.Workload{wl1, wl2}
	inadmissibleWorkloads := []*kueue.Workload{wl3, wl4}

	for _, w := range admissibleworkloads {
		wInfo := workload.NewInfo(w)
		cqImpl.PushOrUpdate(wInfo)
		qImpl.AddOrUpdate(wInfo)
	}

	for _, w := range inadmissibleWorkloads {
		wInfo := workload.NewInfo(w)
		cqImpl.RequeueIfNotPresent(wInfo, false)
		qImpl.AddOrUpdate(wInfo)
	}

	wantPending := len(admissibleworkloads) + len(inadmissibleWorkloads)
	if pending := cqImpl.Pending(); pending != int32(wantPending) {
		t.Errorf("clusterQueue's workload number not right, want %v, got %v", wantPending, pending)
	}
	fifo := cqImpl.(*ClusterQueueBestEffortFIFO)
	if len(fifo.inadmissibleWorkloads) != len(inadmissibleWorkloads) {
		t.Errorf("clusterQueue's workload number in inadmissibleWorkloads not right, want %v, got %v", len(inadmissibleWorkloads), len(fifo.inadmissibleWorkloads))
	}

	cqImpl.DeleteFromQueue(qImpl)
	if cqImpl.Pending() != 0 {
		t.Error("clusterQueue should be empty")
	}
}
