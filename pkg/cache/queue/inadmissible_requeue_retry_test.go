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

package queue

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Exercise the real worker and delaying queue: a second notification must not
// be necessary to recover, and a later failure must start with fresh backoff.
func TestInadmissibleRequeueWorkerRetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, _ := utiltesting.ContextWithLog(t)
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		var reads atomic.Int32
		cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
			WithInterceptorFuncs(interceptor.Funcs{
				Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
					if _, ok := obj.(*corev1.Namespace); ok {
						read := reads.Add(1)
						if read <= 2 || read == 4 {
							return apierrors.NewServiceUnavailable("injected transient failure")
						}
					}
					return delegate.Get(ctx, key, obj, opts...)
				},
			}).Build()
		r := NewRequeuer()
		defer r.queue.ShutDown()
		m := NewManager(cl, nil, r)
		addInadmissibleTestQueue(t, ctx, m, "cq", "lq", "ns", "")
		first := addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq", "ns", "first")
		done := make(chan error, 1)
		go func() { done <- r.Start(ctx) }()
		defer func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("worker shutdown: %v", err)
			}
		}()

		req := requeueRequest{ClusterQueue: "cq"}
		r.notifyClusterQueue("cq")
		time.Sleep(requeueBatchPeriod)
		synctest.Wait()
		if reads.Load() != 1 || r.queue.NumRequeues(req) != 1 {
			t.Fatalf("initial failure: reads=%d, retries=%d", reads.Load(), r.queue.NumRequeues(req))
		}
		time.Sleep(5*time.Second - time.Nanosecond)
		synctest.Wait()
		if reads.Load() != 1 {
			t.Fatalf("retry ran before its backoff expired: reads=%d", reads.Load())
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if reads.Load() != 2 || r.queue.NumRequeues(req) != 2 {
			t.Fatalf("first retry: reads=%d, retries=%d", reads.Load(), r.queue.NumRequeues(req))
		}
		time.Sleep(10*time.Second - time.Nanosecond)
		synctest.Wait()
		if reads.Load() != 2 {
			t.Fatalf("second retry did not back off exponentially: reads=%d", reads.Load())
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if reads.Load() != 3 || r.queue.NumRequeues(req) != 0 {
			t.Fatalf("recovery: reads=%d, retries=%d", reads.Load(), r.queue.NumRequeues(req))
		}
		if m.getClusterQueue("cq").workloads.GetActive(workload.Key(first)) == nil {
			t.Fatal("worker did not recover without a second notification")
		}

		second := addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq", "ns", "second")
		r.notifyClusterQueue("cq")
		time.Sleep(requeueBatchPeriod)
		synctest.Wait()
		if reads.Load() != 4 || r.queue.NumRequeues(req) != 1 {
			t.Fatalf("new failure did not reset backoff: reads=%d, retries=%d", reads.Load(), r.queue.NumRequeues(req))
		}
		time.Sleep(5 * time.Second)
		synctest.Wait()
		if reads.Load() != 5 || r.queue.NumRequeues(req) != 0 || m.getClusterQueue("cq").workloads.GetActive(workload.Key(second)) == nil {
			t.Fatal("new failure did not recover after the initial retry delay")
		}
	})
}

func TestInadmissibleRequeueRevalidatesAfterNamespaceRead(t *testing.T) {
	for _, action := range []string{"delete Workload", "delete ClusterQueue", "change Namespace selector"} {
		t.Run(action, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			var m *Manager
			var wl *kueue.Workload
			cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if _, ok := obj.(*corev1.Namespace); ok {
							switch action {
							case "delete Workload":
								m.DeleteWorkload(log, workload.Key(wl))
							case "delete ClusterQueue":
								m.DeleteClusterQueue(log, utiltestingapi.MakeClusterQueue("cq").Obj())
							case "change Namespace selector":
								cq := utiltestingapi.MakeClusterQueue("cq").NamespaceSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"eligible": "true"}}).Obj()
								if err := m.UpdateClusterQueue(cq, false); err != nil {
									return err
								}
							}
						}
						return delegate.Get(ctx, key, obj, opts...)
					},
				}).Build()
			m = NewManagerForUnitTests(cl, nil)
			addInadmissibleTestQueue(t, ctx, m, "cq", "lq", "ns", "")
			wl = addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq", "ns", "wl")
			moved, retry := requeueWorkloadsCQWithRetry(ctx, m, "cq")
			if moved != 0 || retry {
				t.Errorf("moved=%d, retry=%t; want 0, false", moved, retry)
			}
			if cq := m.getClusterQueue("cq"); cq != nil && cq.workloads.GetActive(workload.Key(wl)) != nil {
				t.Error("Workload was moved using stale queue state")
			}
		})
	}
}

func addInadmissibleTestWorkload(t *testing.T, ctx context.Context, m *Manager, cl client.Client, cqName, lqName, namespace, name string) *kueue.Workload {
	t.Helper()
	wl := utiltestingapi.MakeWorkload(name, namespace).Queue(kueue.LocalQueueName(lqName)).Obj()
	if err := cl.Create(ctx, wl); err != nil {
		t.Fatalf("create Workload: %v", err)
	}
	// An evaluation cycle already happened; the failed Workload goes to inadmissible.
	m.getClusterQueue(kueue.ClusterQueueReference(cqName)).popCycle++
	_, log := utiltesting.ContextWithLog(t)
	m.RequeueWorkload(ctx, workload.NewInfo(log, wl), RequeueReasonGeneric, "")
	if got := m.getClusterQueue(kueue.ClusterQueueReference(cqName)).workloads.GetInadmissible(workload.Key(wl)); got == nil {
		t.Fatalf("Workload %s was not placed in inadmissible", name)
	}
	return wl
}

func addInadmissibleTestQueue(t *testing.T, ctx context.Context, m *Manager, cqName, lqName, namespace, cohortName string) {
	t.Helper()
	cqBuilder := utiltestingapi.MakeClusterQueue(cqName)
	if cohortName != "" {
		cqBuilder = cqBuilder.Cohort(kueue.CohortReference(cohortName))
	}
	if err := m.AddClusterQueue(ctx, cqBuilder.Obj()); err != nil {
		t.Fatalf("add ClusterQueue: %v", err)
	}
	if err := m.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue(lqName, namespace).ClusterQueue(cqName).Obj()); err != nil {
		t.Fatalf("add LocalQueue: %v", err)
	}
}

func TestInadmissibleRequeueTransientNamespaceRetry(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	namespace := utiltesting.MakeNamespace("ns-a")
	failed := false
	reads := 0
	var m *Manager
	cl := utiltesting.NewClientBuilder().WithObjects(namespace).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if _, ok := obj.(*corev1.Namespace); ok && key.Name == "ns-a" {
				reads++
				// A slow API read here must not block all queue-manager writes.
				if m != nil {
					if !m.TryLock() {
						t.Error("Namespace Get ran while queue manager held its write lock")
					} else {
						m.Unlock()
					}
				}
				if !failed {
					failed = true
					return apierrors.NewServiceUnavailable("injected transient failure")
				}
			}
			return delegate.Get(ctx, key, obj, opts...)
		},
	}).Build()
	r := NewRequeuer()
	defer r.queue.ShutDown()
	m = NewManager(cl, nil, r)
	addInadmissibleTestQueue(t, ctx, m, "cq", "lq", "ns-a", "")
	wl := addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq", "ns-a", "retry-me")

	req := requeueRequest{ClusterQueue: "cq"}
	r.reconcile(ctx, req)
	if got := r.queue.NumRequeues(req); got != 1 {
		t.Fatalf("transient failure: NumRequeues = %d, want 1", got)
	}
	if got := m.getClusterQueue("cq").workloads.GetInadmissible(workload.Key(wl)); got == nil {
		t.Fatal("transiently unreadable Workload should remain inadmissible")
	}
	// No second notification is sent: only the queued retry is required.
	r.reconcile(ctx, req)
	if got := r.queue.NumRequeues(req); got != 0 {
		t.Errorf("successful retry: NumRequeues = %d, want 0", got)
	}
	if got := m.getClusterQueue("cq").workloads.GetActive(workload.Key(wl)); got == nil {
		t.Error("Workload did not enter active heap when Namespace read recovered")
	}
	if reads != 2 {
		t.Errorf("Namespace reads = %d, want 2", reads)
	}
}

func TestInadmissibleRequeueCohortPartialProgress(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	reads := map[string]int{}
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns-a"), utiltesting.MakeNamespace("ns-b")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					reads[key.Name]++
					if key.Name == "ns-a" && reads[key.Name] == 1 {
						return apierrors.NewServiceUnavailable("injected failure in first Cohort child")
					}
				}
				return delegate.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := NewRequeuer()
	defer r.queue.ShutDown()
	m := NewManager(cl, nil, r)
	addInadmissibleTestQueue(t, ctx, m, "cq-a", "lq-a", "ns-a", "shared")
	addInadmissibleTestQueue(t, ctx, m, "cq-b", "lq-b", "ns-b", "shared")
	wlA := addInadmissibleTestWorkload(t, ctx, m, cl, "cq-a", "lq-a", "ns-a", "wl-a")
	wlB := addInadmissibleTestWorkload(t, ctx, m, cl, "cq-b", "lq-b", "ns-b", "wl-b")

	req := requeueRequest{Cohort: "shared"}
	r.reconcile(ctx, req)
	if got := m.getClusterQueue("cq-b").workloads.GetActive(workload.Key(wlB)); got == nil {
		t.Error("successful Cohort child must advance despite a failing neighbor")
	}
	if got := m.getClusterQueue("cq-a").workloads.GetInadmissible(workload.Key(wlA)); got == nil {
		t.Error("failed child should remain inadmissible until retry")
	}
	if got := r.queue.NumRequeues(req); got != 1 {
		t.Errorf("partially failed Cohort: NumRequeues = %d, want 1", got)
	}
	r.reconcile(ctx, req)
	if got := m.getClusterQueue("cq-a").workloads.GetActive(workload.Key(wlA)); got == nil {
		t.Error("failed Cohort child was not retried")
	}
	if got := r.queue.NumRequeues(req); got != 0 {
		t.Errorf("successful Cohort retry: NumRequeues = %d, want 0", got)
	}
	if reads["ns-a"] != 2 || reads["ns-b"] != 1 {
		t.Errorf("Namespace reads = %v, want ns-a=2 and ns-b=1", reads)
	}
}

func TestInadmissibleRequeueDeduplicatesNamespaceReads(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	reads := 0
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					reads++
				}
				return delegate.Get(ctx, key, obj, opts...)
			},
		}).Build()
	m := NewManagerForUnitTests(cl, nil)
	addInadmissibleTestQueue(t, ctx, m, "cq", "lq", "ns", "")
	var wls []*kueue.Workload
	for i := range 100 {
		wl := addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq", "ns", fmt.Sprintf("wl-%03d", i))
		wls = append(wls, wl)
	}
	moved, retry := requeueWorkloadsCQWithRetry(ctx, m, "cq")
	if moved != len(wls) || retry {
		t.Errorf("moved=%d, retry=%t; want moved=%d, retry=false", moved, retry, len(wls))
	}
	if reads != 1 {
		t.Errorf("Namespace Get calls = %d, want 1 for %d Workloads", reads, len(wls))
	}
}

// A Workload can enter inadmissible while the requeuer is reading Namespaces.
// It must not be processed with an absent Namespace snapshot (or forgotten).
func TestInadmissibleRequeueDetectsWorkloadsAddedDuringNamespaceRead(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	var m *Manager
	injected := false
	var late *kueue.Workload
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns-a"), utiltesting.MakeNamespace("ns-b")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok && key.Name == "ns-a" && !injected {
					injected = true
					late = utiltestingapi.MakeWorkload("late", "ns-b").Queue("lq-b").Obj()
					if err := delegate.Create(ctx, late); err != nil {
						return err
					}
					m.getClusterQueue("cq").popCycle++
					m.RequeueWorkload(ctx, workload.NewInfo(log, late), RequeueReasonGeneric, "")
				}
				return delegate.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := NewRequeuer()
	defer r.queue.ShutDown()
	m = NewManager(cl, nil, r)
	addInadmissibleTestQueue(t, ctx, m, "cq", "lq-a", "ns-a", "")
	if err := m.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq-b", "ns-b").ClusterQueue("cq").Obj()); err != nil {
		t.Fatalf("add second LocalQueue: %v", err)
	}
	first := addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq-a", "ns-a", "first")
	req := requeueRequest{ClusterQueue: "cq"}
	r.reconcile(ctx, req)
	if !injected || late == nil {
		t.Fatal("test did not insert the Workload during Namespace read")
	}
	if got := m.getClusterQueue("cq").workloads.GetActive(workload.Key(first)); got == nil {
		t.Error("original Workload was not moved")
	}
	if got := m.getClusterQueue("cq").workloads.GetInadmissible(workload.Key(late)); got == nil {
		t.Error("new Workload must stay inadmissible until its Namespace has been read")
	}
	if got := r.queue.NumRequeues(req); got != 1 {
		t.Errorf("late Namespace should trigger retry, got %d", got)
	}
	r.reconcile(ctx, req)
	if got := m.getClusterQueue("cq").workloads.GetActive(workload.Key(late)); got == nil {
		t.Error("late Workload not moved on next pass")
	}
}

func TestInadmissibleRequeueDeletedNamespaceDoesNotRetry(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewFakeClient()
	r := NewRequeuer()
	defer r.queue.ShutDown()
	m := NewManager(cl, nil, r)
	addInadmissibleTestQueue(t, ctx, m, "cq", "lq", "ns-deleted", "")
	wl := addInadmissibleTestWorkload(t, ctx, m, cl, "cq", "lq", "ns-deleted", "wl")
	req := requeueRequest{ClusterQueue: "cq"}
	r.reconcile(ctx, req)
	if got := r.queue.NumRequeues(req); got != 0 {
		t.Errorf("deleted Namespace must not be rate-limited for retry, got %d retries", got)
	}
	if got := m.getClusterQueue("cq").workloads.GetInadmissible(workload.Key(wl)); got == nil {
		t.Error("Workload in a deleted Namespace must remain inadmissible")
	}
}
