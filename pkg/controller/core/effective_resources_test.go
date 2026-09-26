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
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestWorkloadReconcilerPreservesDRAResourceSnapshotWhenQueueing verifies the
// handoff from DRA preprocessing to the queue, both in handleDRA and when
// Reconcile queues the workload again after backoff. The queued PodSpec and
// translated quota must describe the same resource snapshot, even if defaults
// change between preprocessing and queue insertion; the raw Workload stays unchanged.
// A fake client makes that intervening change deterministic without depending
// on informer timing or running the scheduler.
func TestWorkloadReconcilerPreservesDRAResourceSnapshotWhenQueueing(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
	cases := map[string]struct {
		requeueAfterBackoff bool
		preprocessingRead   int
	}{
		// Reconcile first reads defaults in needsDRAReconcile; handleDRA
		// takes the preprocessing snapshot on the second read.
		"initial DRA queue insertion":       {preprocessingRead: 2},
		"DRA queue insertion after backoff": {requeueAfterBackoff: true, preprocessingRead: 2},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			wlBuilder := utiltestingapi.MakeWorkload("wl", "ns").Queue("queue")
			if tc.requeueAfterBackoff {
				wlBuilder.RequeueState(nil, &metav1.Time{Time: now.Add(-time.Minute)})
			}
			wl := wlBuilder.Obj()
			const gpu corev1.ResourceName = "example.com/gpu"
			lr := utiltesting.MakeLimitRange("defaults", "ns").WithValue("DefaultRequest", gpu, "1").Obj()
			dc := testingdra.MakeDeviceClass("gpu.example.com").ExtendedResourceName(string(gpu)).Obj()
			reads := 0
			cl := utiltesting.NewClientBuilder().WithObjects(lr, dc).
				WithStatusSubresource(wl).
				WithIndex(&resourcev1.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex, indexer.IndexDeviceClassExtendedResourceName).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if err := c.List(ctx, list, opts...); err != nil {
							return err
						}
						if _, ok := list.(*corev1.LimitRangeList); ok {
							reads++
							if reads == tc.preprocessingRead {
								// The returned list still contains 1 GPU. Update the
								// stored default so rebuilding Info during either queue
								// insertion would incorrectly pair 2 GPUs with quota for 1.
								lr.Spec.Limits[0].DefaultRequest[gpu] = resource.MustParse("2")
								return c.Update(ctx, lr)
							}
						}
						return nil
					}}).Build()
			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
			if err := cl.Create(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			lq := utiltestingapi.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj()
			if err := cl.Create(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}
			if err := cl.Create(ctx, wl); err != nil {
				t.Fatal(err)
			}
			mapper := dra.NewResourceMapper()
			if err := mapper.PopulateFromConfiguration([]configapi.DeviceClassMapping{{Name: "logical-gpu", DeviceClassNames: []corev1.ResourceName{"gpu.example.com"}}}); err != nil {
				t.Fatal(err)
			}
			backedResources := dra.NewExtendedResourceCache()
			backedResources.Add(gpu, dc.Name)
			reconciler := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{}, WithDRAMapper(mapper), WithDRABackedResources(backedResources))
			reconciler.clock = testingclock.NewFakeClock(now)
			if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}); err != nil {
				t.Fatal(err)
			}
			infos := queues.PendingWorkloadsInfo("cq")
			if len(infos) != 1 {
				t.Fatalf("queued Infos = %d, want 1", len(infos))
			}
			qty := infos[0].PodSpec(0).Containers[0].Resources.Requests[gpu]
			if qty.Cmp(resource.MustParse("1")) != 0 {
				t.Errorf("queued view uses %s GPU, but DRA processed 1", qty.String())
			}
			if got := infos[0].TotalRequests[0].Requests.ResourceValue("logical-gpu"); got != 1 {
				t.Errorf("logical quota = %d, want 1", got)
			}
			if got := infos[0].TotalRequests[0].Requests.ResourceValue(gpu); got != 0 {
				t.Errorf("replaced extended resource quota = %d, want 0", got)
			}
			if len(infos[0].Obj.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Requests) != 0 {
				t.Fatal("raw Workload contains defaults")
			}
		})
	}
}

func TestWorkloadReconcilerResourceAdjustmentErrors(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)
	simulatedErr := errors.New("simulated lookup failure")

	cases := map[string]struct {
		runtimeClass    *nodev1.RuntimeClass
		listErr         error
		wantInternalErr bool
		wantCondition   *metav1.Condition
	}{
		"missing RuntimeClass sets QuotaReserved condition with Reason Misconfigured": {
			runtimeClass:    nil,
			wantInternalErr: false,
			wantCondition: &metav1.Condition{
				Type:   kueue.WorkloadQuotaReserved,
				Status: metav1.ConditionFalse,
				Reason: kueue.WorkloadQuotaReservedReasonMisconfigured,
			},
		},
		"internal error during adjustment returns error to trigger reconcile retry without setting Misconfigured": {
			runtimeClass:    utiltesting.MakeRuntimeClass("rc", "handler").Obj(),
			listErr:         simulatedErr,
			wantInternalErr: true,
			wantCondition:   nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)

			wl := utiltestingapi.MakeWorkload("wl", "ns").
				Queue("queue").
				PodSets(*utiltestingapi.MakePodSet("main", 1).RuntimeClass("rc").Obj()).
				Obj()

			objs := []client.Object{
				&corev1.Namespace{Name: "ns"},
				wl,
			}
			if tc.runtimeClass != nil {
				objs = append(objs, tc.runtimeClass)
			}

			cl := utiltesting.NewClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(wl).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*corev1.LimitRangeList); ok && tc.listErr != nil {
							return tc.listErr
						}
						return c.List(ctx, list, opts...)
					},
				}).Build()

			cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
			lq := utiltestingapi.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj()
			if err := cl.Create(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := cl.Create(ctx, lq); err != nil {
				t.Fatal(err)
			}

			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}

			reconciler := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{})
			reconciler.clock = testingclock.NewFakeClock(now)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)})
			if tc.wantInternalErr {
				if err == nil {
					t.Fatal("expected reconcile error, got nil")
				}
				if !errors.Is(err, workload.ErrInternal) {
					t.Fatalf("expected errors.Is(err, workload.ErrInternal), got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			gotWl := &kueue.Workload{}
			if getErr := cl.Get(ctx, client.ObjectKeyFromObject(wl), gotWl); getErr != nil {
				t.Fatal(getErr)
			}

			cond := apimeta.FindStatusCondition(gotWl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if tc.wantCondition != nil {
				if cond == nil {
					t.Fatalf("expected condition %s, got none", tc.wantCondition.Type)
				}
				if cond.Status != tc.wantCondition.Status {
					t.Errorf("condition status = %s, want %s", cond.Status, tc.wantCondition.Status)
				}
				if cond.Reason != tc.wantCondition.Reason {
					t.Errorf("condition reason = %s, want %s", cond.Reason, tc.wantCondition.Reason)
				}
			} else if cond != nil && cond.Reason == kueue.WorkloadQuotaReservedReasonMisconfigured {
				t.Errorf("unexpected Misconfigured condition on internal error: %v", cond)
			}
		})
	}
}

func TestWorkloadReconcilerDRAAdjustmentErrors(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegrationExtendedResource, true)
	simulatedErr := errors.New("simulated lookup failure")

	cases := map[string]struct {
		runtimeClass    *nodev1.RuntimeClass
		listErr         error
		wantInternalErr bool
		wantCondition   *metav1.Condition
	}{
		"missing RuntimeClass in DRA marks workload misconfigured": {
			runtimeClass:    nil,
			wantInternalErr: false,
			wantCondition: &metav1.Condition{
				Type:   kueue.WorkloadQuotaReserved,
				Status: metav1.ConditionFalse,
				Reason: kueue.WorkloadQuotaReservedReasonMisconfigured,
			},
		},
		"internal error during DRA adjustment triggers reconcile retry without setting Misconfigured": {
			runtimeClass:    utiltesting.MakeRuntimeClass("rc", "handler").Obj(),
			listErr:         simulatedErr,
			wantInternalErr: true,
			wantCondition:   nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			const gpu corev1.ResourceName = "example.com/gpu"

			dc := testingdra.MakeDeviceClass("gpu.example.com").ExtendedResourceName(string(gpu)).Obj()
			wl := utiltestingapi.MakeWorkload("wl", "ns").
				Queue("queue").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RuntimeClass("rc").
					Request(gpu, "1").
					Obj()).
				Obj()

			objs := []client.Object{
				&corev1.Namespace{Name: "ns"},
				dc,
				wl,
			}
			if tc.runtimeClass != nil {
				objs = append(objs, tc.runtimeClass)
			}

			cl := utiltesting.NewClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(wl).
				WithIndex(&resourcev1.DeviceClass{}, indexer.DeviceClassExtendedResourceNameIndex, indexer.IndexDeviceClassExtendedResourceName).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
					List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
						if _, ok := list.(*corev1.LimitRangeList); ok && tc.listErr != nil {
							return tc.listErr
						}
						return c.List(ctx, list, opts...)
					},
				}).Build()

			cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
			lq := utiltestingapi.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj()
			if err := cl.Create(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := cl.Create(ctx, lq); err != nil {
				t.Fatal(err)
			}

			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddLocalQueue(ctx, lq); err != nil {
				t.Fatal(err)
			}

			mapper := dra.NewResourceMapper()
			if err := mapper.PopulateFromConfiguration([]configapi.DeviceClassMapping{{Name: "logical-gpu", DeviceClassNames: []corev1.ResourceName{"gpu.example.com"}}}); err != nil {
				t.Fatal(err)
			}
			backedResources := dra.NewExtendedResourceCache()
			backedResources.Add(gpu, dc.Name)

			reconciler := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{}, WithDRAMapper(mapper), WithDRABackedResources(backedResources))
			reconciler.clock = testingclock.NewFakeClock(now)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)})
			if tc.wantInternalErr {
				if err == nil {
					t.Fatal("expected reconcile error, got nil")
				}
				if !errors.Is(err, workload.ErrInternal) {
					t.Fatalf("expected errors.Is(err, workload.ErrInternal), got: %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected reconcile error: %v", err)
			}

			gotWl := &kueue.Workload{}
			if getErr := cl.Get(ctx, client.ObjectKeyFromObject(wl), gotWl); getErr != nil {
				t.Fatal(getErr)
			}

			cond := apimeta.FindStatusCondition(gotWl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if tc.wantCondition != nil {
				if cond == nil {
					t.Fatalf("expected condition %s, got none", tc.wantCondition.Type)
				}
				if cond.Status != tc.wantCondition.Status {
					t.Errorf("condition status = %s, want %s", cond.Status, tc.wantCondition.Status)
				}
				if cond.Reason != tc.wantCondition.Reason {
					t.Errorf("condition reason = %s, want %s", cond.Reason, tc.wantCondition.Reason)
				}
			} else if cond != nil && cond.Reason == kueue.WorkloadQuotaReservedReasonMisconfigured {
				t.Errorf("unexpected Misconfigured condition on internal error: %v", cond)
			}
		})
	}
}
