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

package core

import (
	"fmt"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

func TestWorkloadRunDurationReconcile(t *testing.T) {
	start := time.Unix(1000, 0)
	now := start.Add(60 * time.Second)
	cases := map[string]struct {
		override                *int32
		defaultSecs             *int32
		deadline                *metav1.Time
		gateOff                 bool
		evicted                 bool
		deactivate              bool
		unadmitted              bool
		maximumExecutionSeconds *int32
		wantEvicted             bool
		wantAfter               time.Duration
		wantSeconds             *int32
	}{
		"default expires": {defaultSecs: new(int32(60)), wantEvicted: true, wantSeconds: new(int32(60))},
		"readiness transition does not pause the run": {defaultSecs: new(int32(60)), unadmitted: true, wantEvicted: true, wantSeconds: new(int32(60))},
		"extension is from original start":            {defaultSecs: new(int32(60)), override: new(int32(90)), wantAfter: 30 * time.Second, wantSeconds: new(int32(90))},
		"shortening past elapsed expires":             {defaultSecs: new(int32(120)), override: new(int32(30)), wantEvicted: true, wantSeconds: new(int32(30))},
		"removing override restores captured default": {
			defaultSecs: new(int32(120)), deadline: new(metav1.NewTime(start.Add(30 * time.Second))),
			wantAfter: 60 * time.Second, wantSeconds: new(int32(120)),
		},
		"removing override restores unlimited":    {deadline: new(metav1.NewTime(start.Add(30 * time.Second)))},
		"gate off does not expire":                {defaultSecs: new(int32(60)), gateOff: true, wantSeconds: new(int32(60))},
		"late extension does not cancel eviction": {defaultSecs: new(int32(60)), override: new(int32(120)), evicted: true, wantEvicted: true, wantSeconds: new(int32(60))},
		"deactivation takes precedence":           {defaultSecs: new(int32(60)), deactivate: true, wantSeconds: new(int32(60))},
		"simultaneous cumulative expiry takes precedence": {
			defaultSecs: new(int32(60)), maximumExecutionSeconds: new(int32(60)), wantSeconds: new(int32(60)),
		},
	}
	for name, tc := range cases {
		for _, useMergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/mergePatch=%t", name, useMergePatch), func(t *testing.T) {
				if !useMergePatch && tc.deadline != nil && tc.wantSeconds == nil {
					t.Skip("The strategic-merge fake cannot model SSA field removal; covered by the API lifecycle suite.")
				}
				features.SetFeatureGateDuringTest(t, features.WorkloadRunDuration, !tc.gateOff)
				features.SetFeatureGateDuringTest(t, features.FinishOrphanedWorkloads, false)
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, useMergePatch)
				wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").SimpleReserveQuota("cq", "default", start).
					AdmittedAt(true, start).RunDuration(tc.defaultSecs, start).Obj()
				wl.Spec.MaximumRunDurationSeconds = tc.override
				wl.Spec.MaximumExecutionTimeSeconds = tc.maximumExecutionSeconds
				if tc.unadmitted {
					apimeta.SetStatusCondition(&wl.Status.Conditions, metav1.Condition{
						Type: kueue.WorkloadAdmitted, Status: metav1.ConditionFalse, Reason: "ReadinessRecovery",
						LastTransitionTime: metav1.NewTime(now),
					})
				}
				if tc.deadline != nil {
					wl.Status.RunDuration.Deadline = tc.deadline
				}
				if tc.evicted {
					workloadevict.SetEvictedCondition(wl, start.Add(30*time.Second), kueue.WorkloadEvictedByRunDuration, "already expired")
				}
				if tc.deactivate {
					workload.SetDeactivationTarget(wl, kueue.WorkloadMaximumExecutionTimeExceeded, "maximum execution time")
				}
				lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
				cq := utiltestingapi.MakeClusterQueue("cq").Obj()
				cq.Spec.MaximumRunDurationSeconds = new(int32(5))
				cl := utiltesting.NewClientBuilder().WithObjects(wl, lq, cq).
					WithStatusSubresource(wl, cq, lq).WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
				}).Build()
				cache := schdcache.New(cl)
				queues := qcache.NewManagerForUnitTests(cl, cache)
				ctx, _ := utiltesting.ContextWithLog(t)
				if err := queues.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
				if err := queues.AddLocalQueue(ctx, lq); err != nil {
					t.Fatal(err)
				}
				r := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{})
				r.clock = testingclock.NewFakeClock(now)
				result, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(wl)})
				if err != nil {
					t.Fatal(err)
				}
				if result.RequeueAfter != tc.wantAfter {
					t.Errorf("requeueAfter = %v, want %v", result.RequeueAfter, tc.wantAfter)
				}
				if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), wl); err != nil {
					t.Fatal(err)
				}
				if !workload.HasQuotaReservation(wl) || wl.Status.Admission == nil {
					t.Fatal("timer released quota before job drain")
				}
				if got := workloadevict.IsEvicted(wl); got != tc.wantEvicted {
					t.Errorf("evicted = %v, want %v; conditions=%v", got, tc.wantEvicted, wl.Status.Conditions)
				}
				if tc.maximumExecutionSeconds != nil && !apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadDeactivationTarget) {
					t.Fatal("cumulative expiry did not request deactivation")
				}
				if tc.wantEvicted {
					if got := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted).Reason; got != kueue.WorkloadEvictedByRunDuration {
						t.Errorf("eviction reason = %s", got)
					}
				}
				if tc.wantSeconds == nil {
					if wl.Status.RunDuration.Deadline != nil {
						t.Errorf("deadline = %v, want unlimited", wl.Status.RunDuration.Deadline)
					}
				} else if want := start.Add(time.Duration(*tc.wantSeconds) * time.Second); !wl.Status.RunDuration.Deadline.Time.Equal(want) {
					t.Errorf("deadline = %v, want %v", wl.Status.RunDuration.Deadline, want)
				}
			})
		}
	}
}
