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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestBestEffortFIFORequeueIfNotPresent(t *testing.T) {
	tests := map[RequeueReason]struct {
		wantInadmissible bool
	}{
		RequeueReasonFailedAfterNomination: {
			wantInadmissible: false,
		},
		RequeueReasonNamespaceMismatch: {
			wantInadmissible: true,
		},
		RequeueReasonGeneric: {
			wantInadmissible: true,
		},
	}

	for reason, test := range tests {
		t.Run(string(reason), func(t *testing.T) {
			cq, _ := newClusterQueueBestEffortFIFO(&kueue.ClusterQueue{
				Spec: kueue.ClusterQueueSpec{
					QueueingStrategy: kueue.StrictFIFO,
				},
			})
			wl := utiltesting.MakeWorkload("workload-1", defaultNamespace).Obj()
			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), reason); !ok {
				t.Error("failed to requeue nonexistent workload")
			}

			_, gotInadmissible := cq.(*ClusterQueueBestEffortFIFO).inadmissibleWorkloads[workload.Key(wl)]
			if diff := cmp.Diff(test.wantInadmissible, gotInadmissible); diff != "" {
				t.Errorf("Unexpected inadmissible status (-want,+got):\n%s", diff)
			}

			if ok := cq.RequeueIfNotPresent(workload.NewInfo(wl), reason); ok {
				t.Error("Re-queued a workload that was already present")
			}
		})
	}
}
