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

package evict

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestIsEvictedByDeactivation(t *testing.T) {
	cases := map[string]struct {
		workload *kueue.Workload
		want     bool
	}{
		"evicted condition doesn't exist": {
			workload: utiltestingapi.MakeWorkload("test", "test").Obj(),
		},
		"evicted condition with false status": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadDeactivated,
					Status: metav1.ConditionFalse,
				}).
				Obj(),
		},
		"evicted condition with PodsReadyTimeout reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"evicted condition with Deactivated reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadDeactivated,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := IsEvictedByDeactivation(tc.workload)
			if tc.want != got {
				t.Errorf("Unexpected result from IsEvictedByDeactivation\nwant:%v\ngot:%v\n", tc.want, got)
			}
		})
	}
}

func TestIsEvictedByPodsReadyTimeout(t *testing.T) {
	cases := map[string]struct {
		workload             *kueue.Workload
		wantEvictedByTimeout bool
		wantCondition        *metav1.Condition
	}{
		"evicted condition doesn't exist": {
			workload: utiltestingapi.MakeWorkload("test", "test").Obj(),
		},
		"evicted condition with false status": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionFalse,
				}).
				Obj(),
		},
		"evicted condition with Preempted reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPreemption,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
		},
		"evicted condition with PodsReadyTimeout reason": {
			workload: utiltestingapi.MakeWorkload("test", "test").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadEvicted,
					Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			wantEvictedByTimeout: true,
			wantCondition: &metav1.Condition{
				Type:   kueue.WorkloadEvicted,
				Reason: kueue.WorkloadEvictedByPodsReadyTimeout,
				Status: metav1.ConditionTrue,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotCondition, gotEvictedByTimeout := IsEvictedByPodsReadyTimeout(tc.workload)
			if tc.wantEvictedByTimeout != gotEvictedByTimeout {
				t.Errorf("Unexpected evictedByTimeout from IsEvictedByPodsReadyTimeout\nwant:%v\ngot:%v\n",
					tc.wantEvictedByTimeout, gotEvictedByTimeout)
			}
			if diff := cmp.Diff(tc.wantCondition, gotCondition,
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); len(diff) != 0 {
				t.Errorf("Unexpected condition from IsEvictedByPodsReadyTimeout: (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestEvict(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cases := map[string]struct {
		// releaseQuota releases the quota reservation, instead of changing another
		// condition, before the first status patch fails with a conflict.
		releaseQuota bool
		wantEvicted  bool
	}{
		"conflict retry while the quota is reserved": {
			wantEvicted: true,
		},
		"conflict retry after the quota was released": {
			releaseQuota: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			metrics.ClearClusterQueueMetrics("cq")
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj()
			var patches int
			cl := utiltesting.NewClientBuilder().
				WithObjects(wl).
				WithStatusSubresource(wl).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						patches++
						if patches > 1 {
							return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
						}
						var stored kueue.Workload
						if err := c.Get(ctx, client.ObjectKeyFromObject(obj), &stored); err != nil {
							return err
						}
						if tc.releaseQuota {
							stored.Status.Admission = nil
							apimeta.SetStatusCondition(&stored.Status.Conditions, metav1.Condition{Type: kueue.WorkloadQuotaReserved, Status: metav1.ConditionFalse, Reason: "Pending"})
						} else {
							apimeta.SetStatusCondition(&stored.Status.Conditions, metav1.Condition{Type: kueue.WorkloadPodsReady, Status: metav1.ConditionFalse, Reason: "PodsReady"})
						}
						if err := c.Status().Update(ctx, &stored); err != nil {
							return err
						}
						return apierrors.NewConflict(kueue.Resource("workloads"), stored.Name, errors.New("the object has been modified"))
					},
				}).
				Build()

			recorder := &utiltesting.EventRecorder{}
			if err := Evict(ctx, cl, recorder, wl, kueue.WorkloadEvictedByPreemption, "Preempted", "", testingclock.NewFakeClock(now), false, nil, nil, WithRetryOnConflict()); err != nil {
				t.Fatalf("Evict() error = %v", err)
			}
			var stored kueue.Workload
			if err := cl.Get(ctx, client.ObjectKeyFromObject(wl), &stored); err != nil {
				t.Fatalf("Failed to get the Workload: %v", err)
			}
			if got := IsEvicted(&stored); got != tc.wantEvicted {
				t.Errorf("Workload evicted = %v, want %v", got, tc.wantEvicted)
			}
			wantReports := 0
			if tc.wantEvicted {
				wantReports = 1
			}
			got := testingmetrics.CollectFilteredGaugeVec(metrics.EvictedWorkloadsTotal, map[string]string{"cluster_queue": "cq"})
			if len(got) != wantReports {
				t.Errorf("ClusterQueue evicted workloads series = %v, want %d", got, wantReports)
			} else if wantReports == 1 && got[0].Value != 1 {
				t.Errorf("ClusterQueue evicted workloads value = %v, want 1", got[0].Value)
			}
			if got := len(recorder.RecordedEvents); got != wantReports {
				t.Errorf("Recorded %d events, want %d: %v", got, wantReports, recorder.RecordedEvents)
			}
		})
	}
}
