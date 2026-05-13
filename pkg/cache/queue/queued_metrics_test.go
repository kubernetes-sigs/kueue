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
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestWorkloadQueueReason(t *testing.T) {
	cases := []struct {
		name       string
		conditions []metav1.Condition
		want       string
	}{
		{
			name:       "no conditions → Pending",
			conditions: nil,
			want:       "Pending",
		},
		{
			name: "QuotaReserved=True → Pending (admitted, should not be here)",
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
					Reason: "QuotaReserved",
				},
			},
			want: "Pending",
		},
		{
			name: "QuotaReserved=False Reason=Inadmissible",
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: "Inadmissible",
				},
			},
			want: "Inadmissible",
		},
		{
			name: "QuotaReserved=False Reason=Waiting",
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: "Waiting",
				},
			},
			want: "Waiting",
		},
		{
			name: "QuotaReserved=False Reason=FlavorMismatch",
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: "FlavorMismatch",
				},
			},
			want: "FlavorMismatch",
		},
		{
			name: "QuotaReserved=False empty Reason → Pending",
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: "",
				},
			},
			want: "Pending",
		},
		{
			name: "future unknown reason is passed through",
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: "SomeFutureReason",
				},
			},
			want: "SomeFutureReason",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wl := utiltestingapi.MakeWorkload("wl", "ns").Obj()
			wl.Status.Conditions = tc.conditions
			got := workloadQueueReason(wl)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("workloadQueueReason() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLQQueuedReasonCounts(t *testing.T) {
	makeInfo := func(reason string) *workload.Info {
		wl := utiltestingapi.MakeWorkload("wl-"+reason, "ns").Obj()
		if reason != "" {
			wl.Status.Conditions = []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: reason,
				},
			}
		}
		return workload.NewInfo(wl)
	}

	q := &LocalQueue{
		items: map[workload.Reference]*workload.Info{
			"ns/wl-Pending":        makeInfo(""),
			"ns/wl-Inadmissible":   makeInfo("Inadmissible"),
			"ns/wl-FlavorMismatch": makeInfo("FlavorMismatch"),
			"ns/wl-Waiting":        makeInfo("Waiting"),
		},
	}

	want := map[string]int{
		"Pending":        1,
		"Inadmissible":   1,
		"FlavorMismatch": 1,
		"Waiting":        1,
	}

	got := lqQueuedReasonCounts(q)
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("lqQueuedReasonCounts() mismatch (-want +got):\n%s", diff)
	}
}
