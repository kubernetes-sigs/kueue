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
	"k8s.io/client-go/util/workqueue"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

type workloadEventWatcherFunc func(*kueue.Workload, *kueue.Workload)

func (f workloadEventWatcherFunc) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	f(oldWl, newWl)
}

func TestWorkloadEventHandlerUpdatesQueuesBeforeReconcile(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, false)
	cases := map[string]struct {
		preload           bool
		wantPending       int
		wantCPU           int64
		wantReconciles    int
		wantNotifications int
	}{
		"create":  {wantPending: 1, wantCPU: 1000, wantReconciles: 1, wantNotifications: 1},
		"update":  {preload: true, wantPending: 1, wantCPU: 2000, wantReconciles: 1, wantNotifications: 1},
		"delete":  {preload: true, wantReconciles: 1, wantNotifications: 1},
		"generic": {preload: true, wantPending: 1, wantCPU: 1000},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().Build()
			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			if err := queues.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
			if tc.preload {
				if err := queues.AddOrUpdateWorkload(ctx, log, wl); err != nil {
					t.Fatal(err)
				}
			}
			r := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{}, WithPreemptionExpectations(preemptexpectations.New()))
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			notifications := 0
			checkPending := func() {
				t.Helper()
				infos := queues.PendingWorkloadsInfo("cq")
				if len(infos) != tc.wantPending {
					t.Fatalf("pending workloads = %d, want %d", len(infos), tc.wantPending)
				}
				if tc.wantPending > 0 && infos[0].TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU) != tc.wantCPU {
					t.Errorf("pending CPU = %d, want %d", infos[0].TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU), tc.wantCPU)
				}
			}
			r.watchers = []WorkloadUpdateWatcher{workloadEventWatcherFunc(func(_, _ *kueue.Workload) {
				notifications++
				checkPending()
				if q.Len() != 0 {
					t.Error("reconcile was enqueued before watcher notification")
				}
			})}
			h := &workloadEventHandler{r: r, ctx: ctx}
			switch name {
			case "create":
				h.Create(ctx, event.TypedCreateEvent[*kueue.Workload]{Object: wl}, q)
			case "update":
				updated := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Request(corev1.ResourceCPU, "2").Obj()
				h.Update(ctx, event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: wl, ObjectNew: updated}, q)
			case "delete":
				h.Delete(ctx, event.TypedDeleteEvent[*kueue.Workload]{Object: wl}, q)
			case "generic":
				h.Generic(ctx, event.TypedGenericEvent[*kueue.Workload]{Object: wl}, q)
			}
			checkPending()
			if notifications != tc.wantNotifications {
				t.Errorf("notifications = %d, want %d", notifications, tc.wantNotifications)
			}
			if q.Len() != tc.wantReconciles {
				t.Fatalf("reconcile requests = %d, want %d", q.Len(), tc.wantReconciles)
			}
			if tc.wantReconciles > 0 {
				request, _ := q.Get()
				defer q.Done(request)
				if request.NamespacedName != client.ObjectKeyFromObject(wl) {
					t.Errorf("reconcile request = %v, want %v", request.NamespacedName, client.ObjectKeyFromObject(wl))
				}
			}
		})
	}
}

func TestWorkloadEventHandlerResourceLookupsRespectCancellation(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, false)
	for name, tc := range map[string]struct{ create, reserved bool }{
		"create pending":  {create: true},
		"update pending":  {},
		"create reserved": {create: true, reserved: true},
		"update reserved": {reserved: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			var gotRuntimeClass, listedLimitRanges bool
			cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(lookupCtx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*nodev1.RuntimeClass); !ok {
						return cl.Get(lookupCtx, key, obj, opts...)
					}
					gotRuntimeClass = true
					// Cancel while the event is being handled, as controller shutdown would.
					cancel()
					if !errors.Is(lookupCtx.Err(), context.Canceled) {
						t.Errorf("RuntimeClass lookup lost event cancellation: %v", lookupCtx.Err())
					}
					return context.Canceled
				},
				List: func(lookupCtx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*corev1.LimitRangeList); !ok {
						return cl.List(lookupCtx, list, opts...)
					}
					listedLimitRanges = true
					if !errors.Is(lookupCtx.Err(), context.Canceled) {
						t.Errorf("LimitRange lookup lost event cancellation: %v", lookupCtx.Err())
					}
					return context.Canceled
				},
			}).Build()
			cache := schdcache.New(cl)
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithPreemptionExpectations(preemptexpectations.New()))
			if err := queues.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			wlBuilder := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").PodSets(*utiltestingapi.MakePodSet("main", 1).RuntimeClass("runtime").Obj())
			if tc.reserved {
				wlBuilder.ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), time.Now())
			}
			wl := wlBuilder.Obj()
			r := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{})
			h := &workloadEventHandler{r: r, ctx: ctx}
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			if tc.create {
				h.Create(ctx, event.TypedCreateEvent[*kueue.Workload]{Object: wl}, q)
			} else {
				h.Update(ctx, event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: wl.DeepCopy(), ObjectNew: wl}, q)
			}
			if !gotRuntimeClass || !listedLimitRanges {
				t.Fatalf("Expected both resource lookups: RuntimeClass=%t, LimitRange=%t", gotRuntimeClass, listedLimitRanges)
			}
		})
	}
}

// The source cancels each event context as soon as the handler returns. Delayed
// second-pass lookups must instead remain alive until the controller stops.
func TestWorkloadEventHandlerSecondPassUsesControllerLifetime(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, false)
	for name, stopController := range map[string]bool{"event completed": false, "controller stopped": true} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			controllerCtx, stop := context.WithCancel(ctx)
			defer stop()
			fakeClock := testingclock.NewFakeClock(time.Now())
			lookups := 0
			workloadReads := 0
			eventFinished := false
			cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Get: func(lookupCtx context.Context, cl client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*kueue.Workload); !ok {
						return cl.Get(lookupCtx, key, obj, opts...)
					}
					workloadReads++
					if stopController {
						if !errors.Is(lookupCtx.Err(), context.Canceled) {
							t.Errorf("second-pass read did not observe controller shutdown: %v", lookupCtx.Err())
						}
						return context.Canceled
					}
					if lookupCtx.Err() != nil {
						t.Errorf("event completion cancelled second-pass read: %v", lookupCtx.Err())
					}
					return cl.Get(lookupCtx, key, obj, opts...)
				},
				List: func(lookupCtx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
					if _, ok := list.(*corev1.LimitRangeList); !ok {
						return cl.List(lookupCtx, list, opts...)
					}
					if !eventFinished {
						return lookupCtx.Err()
					}
					lookups++
					if stopController {
						if !errors.Is(lookupCtx.Err(), context.Canceled) {
							t.Errorf("second-pass lookup did not observe controller shutdown: %v", lookupCtx.Err())
						}
					} else if lookupCtx.Err() != nil {
						t.Errorf("event completion cancelled second-pass lookup: %v", lookupCtx.Err())
					}
					return lookupCtx.Err()
				},
			}).Build()
			cache := schdcache.New(cl)
			queues := qcache.NewManagerForUnitTests(cl, cache, qcache.WithClock(fakeClock), qcache.WithPreemptionExpectations(preemptexpectations.New()))
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			if err := queues.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").
				PodSets(*utiltestingapi.MakePodSet("main", 1).RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "1").Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment("main").Assignment(corev1.ResourceCPU, "rf", "1").DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).Obj()).Obj(), fakeClock.Now()).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady}).Obj()
			if err := cl.Create(ctx, wl); err != nil {
				t.Fatal(err)
			}
			r := NewWorkloadReconciler(cl, queues, cache, &utiltesting.EventRecorder{})
			h := &workloadEventHandler{r: r, ctx: controllerCtx}
			q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
			defer q.ShutDown()
			eventCtx, finishEvent := context.WithCancel(controllerCtx)
			defer finishEvent()
			h.Create(eventCtx, event.TypedCreateEvent[*kueue.Workload]{Object: wl}, q)
			finishEvent()
			eventFinished = true
			if lookups != 0 {
				t.Fatalf("resource lookups before backoff = %d, want 0", lookups)
			}
			if stopController {
				stop()
			}
			fakeClock.Step(time.Second)
			if workloadReads != 1 {
				t.Fatalf("second-pass workload reads = %d, want 1", workloadReads)
			}
			wantLookups := 1
			if stopController {
				wantLookups = 0
			}
			if lookups != wantLookups {
				t.Fatalf("second-pass resource lookups = %d, want %d", lookups, wantLookups)
			}
			if stopController && fakeClock.Waiters() != 0 {
				t.Fatal("controller shutdown scheduled a second-pass retry")
			}
		})
	}
}
