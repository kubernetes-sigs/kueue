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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestQueueEventHandlersEnqueue(t *testing.T) {
	cases := map[string]func(context.Context, *testing.T, client.Client, *schdcache.Cache, *qcache.Manager){
		"LocalQueue": func(ctx context.Context, t *testing.T, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager) {
			h := &localQueueEventHandler{r: NewLocalQueueReconciler(cl, manager, cache)}
			obj := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
			testQueueEventHandlerEnqueue(ctx, t, h, obj, obj.DeepCopy(), true)
		},
		"ClusterQueue": func(ctx context.Context, t *testing.T, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager) {
			h := &clusterQueueEventHandler{r: NewClusterQueueReconciler(cl, manager, cache)}
			obj := utiltestingapi.MakeClusterQueue("cq").Obj()
			testQueueEventHandlerEnqueue(ctx, t, h, obj, obj.DeepCopy(), true)
		},
		"Cohort": func(ctx context.Context, t *testing.T, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager) {
			h := &cohortEventHandler{r: NewCohortReconciler(cl, cache, manager)}
			obj := utiltestingapi.MakeCohort("cohort").Obj()
			updated := utiltestingapi.MakeCohort("cohort").Parent("parent").Obj()
			testQueueEventHandlerEnqueue(ctx, t, h, obj, updated, false)
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return nil },
			}).Build()
			cache := schdcache.New(cl)
			manager := qcache.NewManagerForUnitTests(cl, cache)
			tc(ctx, t, cl, cache, manager)
		})
	}
}

func testQueueEventHandlerEnqueue[T client.Object](ctx context.Context, t *testing.T, h handler.TypedEventHandler[T, reconcile.Request], obj, updated T, enqueueUnchanged bool) {
	t.Helper()
	q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
	defer q.ShutDown()
	wantRequest := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(obj)}
	checkQueue := func(want int) {
		t.Helper()
		if q.Len() != want {
			t.Fatalf("Queued requests = %d, want %d", q.Len(), want)
		}
		if want == 1 {
			got, _ := q.Get()
			q.Done(got)
			if got != wantRequest {
				t.Errorf("Request = %v, want %v", got, wantRequest)
			}
		}
	}
	h.Create(ctx, event.TypedCreateEvent[T]{Object: obj}, q)
	checkQueue(1)
	h.Update(ctx, event.TypedUpdateEvent[T]{ObjectOld: obj, ObjectNew: obj}, q)
	if enqueueUnchanged {
		checkQueue(1)
	} else {
		checkQueue(0)
	}
	h.Update(ctx, event.TypedUpdateEvent[T]{ObjectOld: obj, ObjectNew: updated}, q)
	checkQueue(1)
	h.Generic(ctx, event.TypedGenericEvent[T]{Object: updated}, q)
	checkQueue(1)
	h.Delete(ctx, event.TypedDeleteEvent[T]{Object: updated}, q)
	checkQueue(1)
}

func TestQueueEventHandlersPropagateContext(t *testing.T) {
	type contextKey struct{}
	cases := map[string]struct {
		handle       func(context.Context, client.Client, *schdcache.Cache, *qcache.Manager, workqueue.TypedRateLimitingInterface[reconcile.Request])
		wantRequest  reconcile.Request
		wantMinLists int
	}{
		"LocalQueue create": {
			handle: func(ctx context.Context, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h := localQueueEventHandler{r: NewLocalQueueReconciler(cl, manager, cache)}
				h.Create(ctx, event.TypedCreateEvent[*kueue.LocalQueue]{Object: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()}, q)
			},
			wantRequest:  reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "ns", Name: "lq"}},
			wantMinLists: 1,
		},
		"LocalQueue resume": {
			handle: func(ctx context.Context, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h := localQueueEventHandler{r: NewLocalQueueReconciler(cl, manager, cache)}
				old := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.Hold).Obj()
				updated := old.DeepCopy()
				updated.Spec.StopPolicy = new(kueue.None)
				h.Update(ctx, event.TypedUpdateEvent[*kueue.LocalQueue]{ObjectOld: old, ObjectNew: updated}, q)
			},
			wantRequest:  reconcile.Request{NamespacedName: client.ObjectKey{Namespace: "ns", Name: "lq"}},
			wantMinLists: 1,
		},
		"ClusterQueue create": {
			handle: func(ctx context.Context, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h := clusterQueueEventHandler{r: NewClusterQueueReconciler(cl, manager, cache)}
				h.Create(ctx, event.TypedCreateEvent[*kueue.ClusterQueue]{Object: utiltestingapi.MakeClusterQueue("cq").Obj()}, q)
			},
			wantRequest:  reconcile.Request{NamespacedName: client.ObjectKey{Name: "cq"}},
			wantMinLists: 2,
		},
		"Cohort update": {
			handle: func(ctx context.Context, cl client.Client, cache *schdcache.Cache, manager *qcache.Manager, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
				h := cohortEventHandler{r: NewCohortReconciler(cl, cache, manager)}
				h.Update(ctx, event.TypedUpdateEvent[*kueue.Cohort]{
					ObjectOld: utiltestingapi.MakeCohort("cohort").Obj(),
					ObjectNew: utiltestingapi.MakeCohort("cohort").Parent("parent").Obj(),
				}, q)
			},
			wantRequest: reconcile.Request{NamespacedName: client.ObjectKey{Name: "cohort"}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, cancelled := range []bool{false, true} {
				t.Run(map[bool]string{false: "active", true: "cancelled"}[cancelled], func(t *testing.T) {
					var logs strings.Builder
					logger := funcr.New(func(_, line string) { logs.WriteString(line) }, funcr.Options{Verbosity: 5}).WithValues("trace", "caller-value")
					ctx := ctrl.LoggerInto(t.Context(), logger)
					ctx = context.WithValue(ctx, contextKey{}, "caller-value")
					deadline := time.Now().Add(time.Hour)
					ctx, cancel := context.WithDeadline(ctx, deadline)
					defer cancel()
					if cancelled {
						cancel()
					}
					lists := 0
					cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
						List: func(callCtx context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
							lists++
							if callCtx.Value(contextKey{}) != "caller-value" || callCtx.Done() != ctx.Done() {
								t.Error("List did not receive the caller's values and cancellation")
							}
							if got, ok := callCtx.Deadline(); !ok || !got.Equal(deadline) {
								t.Errorf("List deadline = (%v, %v), want %v", got, ok, deadline)
							}
							if !errors.Is(callCtx.Err(), ctx.Err()) {
								t.Errorf("List context error = %v, want %v", callCtx.Err(), ctx.Err())
							}
							ctrl.LoggerFrom(callCtx).Info("List called")
							return callCtx.Err()
						},
					}).Build()
					cache := schdcache.New(cl)
					manager := qcache.NewManagerForUnitTests(cl, cache)
					q := workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[reconcile.Request]())
					defer q.ShutDown()
					tc.handle(ctx, cl, cache, manager, q)
					if lists < tc.wantMinLists {
						t.Errorf("List calls = %d, want at least %d", lists, tc.wantMinLists)
					}
					if !strings.Contains(logs.String(), "caller-value") {
						t.Errorf("Event handler dropped the caller's logger values: %s", logs.String())
					}
					if q.Len() != 1 {
						t.Fatalf("Queued requests = %d, want 1", q.Len())
					}
					got, _ := q.Get()
					q.Done(got)
					if got != tc.wantRequest {
						t.Errorf("Request = %v, want %v", got, tc.wantRequest)
					}
				})
			}
		})
	}
}

func TestQueueEventLoggingLevels(t *testing.T) {
	for _, verbosity := range []int{2, 3, 4, 5} {
		t.Run(fmt.Sprintf("verbosity-%d", verbosity), func(t *testing.T) {
			var logs strings.Builder
			logger := funcr.New(func(_, line string) { logs.WriteString(line) }, funcr.Options{Verbosity: verbosity})
			ctx := ctrl.LoggerInto(t.Context(), logger)
			cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				List: func(context.Context, client.WithWatch, client.ObjectList, ...client.ListOption) error { return nil },
			}).Build()
			cache := schdcache.New(cl)
			manager := qcache.NewManagerForUnitTests(cl, cache)
			cq := utiltestingapi.MakeClusterQueue("cq").Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
			cohort := utiltestingapi.MakeCohort("cohort").Obj()
			testQueueEventHandlerEnqueue(ctx, t, &clusterQueueEventHandler{r: NewClusterQueueReconciler(cl, manager, cache)}, cq, cq.DeepCopy(), true)
			testQueueEventHandlerEnqueue(ctx, t, &localQueueEventHandler{r: NewLocalQueueReconciler(cl, manager, cache)}, lq, lq.DeepCopy(), true)
			testQueueEventHandlerEnqueue(ctx, t, &cohortEventHandler{r: NewCohortReconciler(cl, cache, manager)}, cohort, utiltestingapi.MakeCohort("cohort").Parent("parent").Obj(), false)
			for message, level := range map[string]int{
				"ClusterQueue create event":      3,
				"ClusterQueue update event":      3,
				"ClusterQueue delete event":      3,
				"Got ClusterQueue generic event": 5,
				"LocalQueue create event":        3,
				"LocalQueue update event":        3,
				"LocalQueue delete event":        3,
				"LocalQueue generic event":       5,
				"Cohort delete event":            3,
			} {
				if got, want := strings.Contains(logs.String(), message), verbosity >= level; got != want {
					t.Errorf("Log %q present = %v, want %v at verbosity %d", message, got, want, verbosity)
				}
			}
		})
	}
}
