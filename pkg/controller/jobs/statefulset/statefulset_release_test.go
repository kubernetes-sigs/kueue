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
		workload                *kueue.Workload
		wantQuotaReservedStatus metav1.ConditionStatus
		wantQuotaReservedReason string
		wantAdmissionNil        bool
		wantOnHold              bool
	}{
		"releases admitted workload": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
			wantQuotaReservedStatus: metav1.ConditionFalse,
			wantQuotaReservedReason: kueue.WorkloadOnHold,
			wantAdmissionNil:        true,
			wantOnHold:              true,
		},
		"ignores workload without active reservation": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				Obj(),
			wantQuotaReservedStatus: "",
			wantAdmissionNil:        true,
			wantOnHold:              false,
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
			wantQuotaReservedStatus: metav1.ConditionTrue,
			wantQuotaReservedReason: "AdmittedByTest",
			wantAdmissionNil:        false,
			wantOnHold:              false,
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

			if tc.wantQuotaReservedStatus != "" {
				cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadQuotaReserved)
				if cond == nil || cond.Status != tc.wantQuotaReservedStatus || cond.Reason != tc.wantQuotaReservedReason {
					t.Fatalf("unexpected quota reserved condition: %+v", cond)
				}
			} else {
				cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadQuotaReserved)
				if cond != nil {
					t.Fatalf("expected no QuotaReserved condition, got %+v", cond)
				}
			}
			if tc.wantAdmissionNil {
				if got.Status.Admission != nil {
					t.Fatalf("expected admission to be nil, got %+v", got.Status.Admission)
				}
			} else if got.Status.Admission == nil {
				t.Fatalf("expected admission to not be nil, but it was nil")
			}
			// When on hold, QuotaReserved=False with reason OnHold
			onHoldCond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadQuotaReserved)
			if tc.wantOnHold {
				if onHoldCond == nil || onHoldCond.Status != metav1.ConditionFalse || onHoldCond.Reason != kueue.WorkloadOnHold {
					t.Fatalf("expected QuotaReserved=False with reason OnHold, got %+v", onHoldCond)
				}
			} else if onHoldCond != nil && onHoldCond.Status == metav1.ConditionFalse && onHoldCond.Reason == kueue.WorkloadOnHold {
				t.Fatalf("expected workload not to be on hold, but QuotaReserved=False with reason OnHold")
			}
		})
	}
}
