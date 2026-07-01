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

package statefulset

import (
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReleaseScaleDownReservation(t *testing.T) {
	now := time.Now()

	cases := map[string]struct {
		workload          *kueue.Workload
		wantQuotaReserved *metav1.Condition
		wantAdmissionNil  bool
	}{
		"releases admitted workload": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
			wantQuotaReserved: &metav1.Condition{
				Status: metav1.ConditionFalse,
				Reason: kueue.WorkloadOnHold,
			},
			wantAdmissionNil: true,
		},
		"ignores workload without active reservation": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				Obj(),
			wantAdmissionNil: true,
		},
		"ignores finished workload": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "Succeeded",
					Message: "Job finished successfully",
				}).
				Obj(),
			wantQuotaReserved: &metav1.Condition{
				Status: metav1.ConditionTrue,
				Reason: "AdmittedByTest",
			},
			wantAdmissionNil: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			c := utiltesting.NewFakeClient(tc.workload.DeepCopy())
			r := &Reconciler{client: c}

			if err := r.releaseScaleDownReservation(ctx, tc.workload); err != nil {
				t.Fatalf("releaseScaleDownReservation() error = %v", err)
			}

			got := &kueue.Workload{}
			if err := c.Get(ctx, client.ObjectKeyFromObject(tc.workload), got); err != nil {
				t.Fatalf("failed to get workload: %v", err)
			}

			cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadQuotaReserved)
			switch {
			case tc.wantQuotaReserved == nil && cond != nil:
				t.Fatalf("expected no QuotaReserved condition, got %+v", cond)
			case tc.wantQuotaReserved != nil && (cond == nil || cond.Status != tc.wantQuotaReserved.Status || cond.Reason != tc.wantQuotaReserved.Reason):
				t.Fatalf("unexpected quota reserved condition: %+v", cond)
			}
			if tc.wantAdmissionNil {
				if got.Status.Admission != nil {
					t.Fatalf("expected admission to be nil, got %+v", got.Status.Admission)
				}
			} else if got.Status.Admission == nil {
				t.Fatalf("expected admission to not be nil, but it was nil")
			}
		})
	}
}
