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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	lq := metrics.LocalQueueReference{Name: "lq", Namespace: "ns"}
	lqLabels := map[string]string{"name": "lq", "namespace": "ns"}
	lqB := metrics.LocalQueueReference{Name: "lq-b", Namespace: "ns"}
	cases := map[string]struct {
		lqRecorder               bool
		deleteDuringStatusUpdate bool
		// moveDuringStatusUpdate moves the stored Workload to lq-b and clears lq-b's
		// metrics before the first status write.
		moveDuringStatusUpdate bool
		evictOptions           []Option
		wantLocalQueueSeries   int
	}{
		"without a LocalQueue recorder": {},
		"with a LocalQueue recorder": {
			lqRecorder:           true,
			wantLocalQueueSeries: 1,
		},
		"LocalQueue deleted during the status update": {
			lqRecorder:               true,
			deleteDuringStatusUpdate: true,
		},
		"Workload moved to another LocalQueue during the status update": {
			lqRecorder:             true,
			moveDuringStatusUpdate: true,
			// The scheduler's options, with which the status write returns the
			// Workload as another writer changed it.
			evictOptions: []Option{WithLooseOnApply(), WithRetryOnConflict()},
		},
	}
	for name, tc := range cases {
		for _, mergePatch := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s, WorkloadRequestUseMergePatch=%t", name, mergePatch), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, mergePatch)
				ctx, _ := utiltesting.ContextWithLog(t)
				metrics.ClearClusterQueueMetrics("cq")
				metrics.ClearLocalQueueMetrics(lq)
				metrics.ClearLocalQueueMetrics(lqB)
				var lqRecorder *metrics.LocalQueueRecorder
				if tc.lqRecorder {
					lqRecorder = metrics.NewLocalQueueRecorder(lq)
				}
				wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).Obj()
				var moved bool
				// The status write goes through Patch or Apply, depending on the feature gate.
				beforeStatusWrite := func(ctx context.Context, c client.Client) error {
					if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueEvictedWorkloadsTotal, lqLabels); len(got) != 0 {
						t.Errorf("LocalQueue evicted workloads series before the status update = %v, want none", got)
					}
					if tc.deleteDuringStatusUpdate {
						lqRecorder.Delete()
					}
					if tc.moveDuringStatusUpdate && !moved {
						moved = true
						return moveWorkload(ctx, c, client.ObjectKeyFromObject(wl), lqB)
					}
					return nil
				}
				cl := utiltesting.NewClientBuilder().
					WithObjects(wl).
					WithStatusSubresource(wl).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if err := beforeStatusWrite(ctx, c); err != nil {
								return err
							}
							return c.SubResource(subResourceName).Patch(ctx, obj, patch, opts...)
						},
						SubResourceApply: func(ctx context.Context, c client.Client, subResourceName string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
							if err := beforeStatusWrite(ctx, c); err != nil {
								return err
							}
							return utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration(ctx, c, subResourceName, applyConf, opts...)
						},
					}).
					Build()

				recorder := &utiltesting.EventRecorder{}
				if err := Evict(ctx, cl, recorder, wl, kueue.WorkloadEvictedByPreemption, "Preempted", "", testingclock.NewFakeClock(now), lqRecorder, nil, nil, tc.evictOptions...); err != nil {
					t.Fatalf("Evict() error = %v", err)
				}
				if got := testingmetrics.CollectFilteredGaugeVec(metrics.EvictedWorkloadsTotal, map[string]string{"cluster_queue": "cq"}); len(got) != 1 {
					t.Errorf("ClusterQueue evicted workloads series = %v, want 1", got)
				}
				wantEvents := []utiltesting.EventRecord{{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToPreempted",
					Message:   "Preempted",
				}}
				if diff := cmp.Diff(wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("Unexpected events (-want,+got):\n%s", diff)
				}
				if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueEvictedWorkloadsTotal, lqLabels); len(got) != tc.wantLocalQueueSeries {
					t.Errorf("LocalQueue evicted workloads series = %v, want %d", got, tc.wantLocalQueueSeries)
				}
				if got := testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueEvictedWorkloadsTotal, map[string]string{"name": "lq-b", "namespace": "ns"}); len(got) != 0 {
					t.Errorf("Evicted workloads series of the deleted lq-b = %v, want none", got)
				}
			})
		}
	}
}

// moveWorkload changes the stored Workload's queue to lq and clears lq's metrics,
// standing in for another writer that requeues the Workload in lq right before lq
// is deleted.
func moveWorkload(ctx context.Context, c client.Client, key client.ObjectKey, lq metrics.LocalQueueReference) error {
	var wl kueue.Workload
	if err := c.Get(ctx, key, &wl); err != nil {
		return err
	}
	wl.Spec.QueueName = lq.Name
	if err := c.Update(ctx, &wl); err != nil {
		return err
	}
	metrics.ClearLocalQueueMetrics(lq)
	return nil
}
