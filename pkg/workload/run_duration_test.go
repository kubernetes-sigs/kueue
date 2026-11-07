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

package workload

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestRunDurationDeadline(t *testing.T) {
	start := time.Unix(1000, 0)
	cases := map[string]struct {
		defaultSeconds *int32
		override       *int32
		wantSeconds    *int32
	}{
		"unlimited":                {},
		"default":                  {defaultSeconds: new(int32(60)), wantSeconds: new(int32(60))},
		"shorter override":         {defaultSeconds: new(int32(60)), override: new(int32(30)), wantSeconds: new(int32(30))},
		"longer override":          {defaultSeconds: new(int32(60)), override: new(int32(120)), wantSeconds: new(int32(120))},
		"override without default": {override: new(int32(30)), wantSeconds: new(int32(30))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := utiltestingapi.MakeWorkload("wl", "ns").SimpleReserveQuota("cq", "default", start).
				AdmittedAt(true, start).RunDuration(tc.defaultSeconds, start).Obj()
			wl.Spec.MaximumRunDurationSeconds = tc.override
			var want *metav1.Time
			if tc.wantSeconds != nil {
				want = new(metav1.NewTime(start.Add(time.Duration(*tc.wantSeconds) * time.Second)))
			}
			if diff := cmp.Diff(want, RunDurationDeadline(wl)); diff != "" {
				t.Errorf("deadline mismatch (-want,+got):\n%s", diff)
			}
			wl.Spec.MaximumRunDurationSeconds = nil
			var inherited *metav1.Time
			if tc.defaultSeconds != nil {
				inherited = new(metav1.NewTime(start.Add(time.Duration(*tc.defaultSeconds) * time.Second)))
			}
			if diff := cmp.Diff(inherited, RunDurationDeadline(wl)); diff != "" {
				t.Errorf("cleared override mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestRunDurationAdmissionLifecycle(t *testing.T) {
	start := time.Unix(1000, 0)
	wl := utiltestingapi.MakeWorkload("wl", "ns").SimpleReserveQuota("cq", "default", start).
		MaximumRunDurationSeconds(30).Obj()
	wl.Status.Admission.RunDurationPolicy = &kueue.RunDurationPolicy{DefaultSeconds: new(int32(60))}
	if !SyncAdmittedCondition(wl, start) || wl.Status.RunDuration == nil {
		t.Fatal("admission did not initialize the timer")
	}
	if got := wl.Status.RunDuration.Deadline.Time; !got.Equal(start.Add(30 * time.Second)) {
		t.Fatalf("deadline = %v", got)
	}
	wl.Status.AdmissionChecks = []kueue.AdmissionCheckState{{Name: "check", State: kueue.CheckStatePending}}
	SyncAdmittedCondition(wl, start.Add(10*time.Second))
	if IsAdmitted(wl) || wl.Status.RunDuration == nil {
		t.Fatal("a temporary unadmitted state reset this reservation's timer")
	}
	wl.Status.AdmissionChecks[0].State = kueue.CheckStateReady
	SyncAdmittedCondition(wl, start.Add(20*time.Second))
	if !wl.Status.RunDuration.AdmittedAt.Time.Equal(start) ||
		!wl.Status.RunDuration.Deadline.Time.Equal(start.Add(30*time.Second)) {
		t.Fatal("readiness recovery restarted the existing reservation's timer")
	}
	CompleteRunDurationRequeue(wl, start.Add(time.Minute))
	UnsetQuotaReservationWithCondition(wl, "Pending", "drained", start.Add(time.Minute))
	if wl.Status.RunDuration != nil || HasQuotaReservation(wl) {
		t.Fatal("drain retained the old admission timer or quota")
	}
	admission := &kueue.Admission{ClusterQueue: "cq", RunDurationPolicy: &kueue.RunDurationPolicy{DefaultSeconds: new(int32(90))}}
	wl.Status.Admission = admission
	wl.Status.Conditions = nil
	wl.Status.Conditions = append(wl.Status.Conditions, metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionTrue})
	next := start.Add(2 * time.Minute)
	SyncAdmittedCondition(wl, next)
	if wl.Status.RunDuration == nil || !wl.Status.RunDuration.AdmittedAt.Time.Equal(next) ||
		!wl.Status.RunDuration.Deadline.Time.Equal(next.Add(30*time.Second)) {
		t.Fatal("readmission did not start a fresh timer")
	}
}
