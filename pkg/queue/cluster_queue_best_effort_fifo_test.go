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
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestBestEffortFIFORequeueIfNotPresent(t *testing.T) {
	tests := map[string]struct {
		reason           RequeueReason
		lastAssignment   *workload.AssigmentClusterQueueState
		wantInadmissible bool
	}{
		"failure after nomination": {
			reason:           RequeueReasonFailedAfterNomination,
			wantInadmissible: false,
		},
		"namespace doesn't match": {
			reason:           RequeueReasonNamespaceMismatch,
			wantInadmissible: true,
		},
		"didn't fit and no pending flavors": {
			reason: RequeueReasonGeneric,
			lastAssignment: &workload.AssigmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceMemory: -1,
					},
					{
						corev1.ResourceCPU:    -1,
						corev1.ResourceMemory: -1,
					},
				},
			},
			wantInadmissible: true,
		},
		"didn't fit but pending flavors": {
			reason: RequeueReasonGeneric,
			lastAssignment: &workload.AssigmentClusterQueueState{
				LastTriedFlavorIdx: []map[corev1.ResourceName]int{
					{
						corev1.ResourceCPU:    -1,
						corev1.ResourceMemory: 0,
					},
					{
						corev1.ResourceMemory: 1,
					},
				},
			},
			wantInadmissible: false,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			cq, _ := newClusterQueueBestEffortFIFO(&kueue.ClusterQueue{
				Spec: kueue.ClusterQueueSpec{
					QueueingStrategy: kueue.StrictFIFO,
				},
			})
			wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
			info := workload.NewInfo(wl)
			info.LastAssignment = tc.lastAssignment
			if ok := cq.RequeueIfNotPresent(info, tc.reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			_, gotInadmissible := cq.(*ClusterQueueBestEffortFIFO).inadmissibleWorkloads[workload.Key(wl)]
			if diff := cmp.Diff(tc.wantInadmissible, gotInadmissible); diff != "" {
				t.Errorf("Unexpected inadmissible status (-want,+got):\n%s", diff)
			}

			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), tc.reason); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}
