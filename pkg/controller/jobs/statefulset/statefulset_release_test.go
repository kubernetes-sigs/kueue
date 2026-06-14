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
	"context"
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
	}{
		"releases admitted workload": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
			wantQuotaReservedStatus: metav1.ConditionFalse,
			wantQuotaReservedReason: "StatefulSetScaledDown",
			wantAdmissionNil:        true,
		},
		"ignores workload without active reservation": {
			workload: utiltestingapi.MakeWorkload(GetWorkloadName("sts-uid", "sts"), "ns").
				Queue("lq").
				Obj(),
			wantQuotaReservedStatus: "",
			wantAdmissionNil:        true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
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
			}
			if tc.wantAdmissionNil && got.Status.Admission != nil {
				t.Fatalf("expected admission to be nil, got %+v", got.Status.Admission)
			}
		})
	}
}
