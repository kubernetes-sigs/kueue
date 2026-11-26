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
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

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
						if read == 1 {
							return apierrors.NewNotFound(corev1.Resource("namespaces"), key.Name)
						}
						if read == 2 || read == 4 {
							return apierrors.NewServiceUnavailable("injected transient failure")
						}
					}
					return delegate.Get(ctx, key, obj, opts...)
				},
			}).Build()
		r := NewRequeuer()
		defer r.queue.ShutDown()
		m := NewManager(cl, nil, r)
		addInadmissibleTestQueue(ctx, t, m, "cq", "lq", "ns", "")
		first := addInadmissibleTestWorkload(ctx, t, m, cl, "cq", "lq", "ns", "first")
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
		time.Sleep(configapi.DefaultInadmissibleWorkloadRequeuerBatchPeriod)
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

		second := addInadmissibleTestWorkload(ctx, t, m, cl, "cq", "lq", "ns", "second")
		r.notifyClusterQueue("cq")
		time.Sleep(configapi.DefaultInadmissibleWorkloadRequeuerBatchPeriod)
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

func addInadmissibleTestWorkload(ctx context.Context, t *testing.T, m *Manager, cl client.Client, cqName, lqName, namespace, name string) *kueue.Workload {
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

func addInadmissibleTestQueue(ctx context.Context, t *testing.T, m *Manager, cqName, lqName, namespace, cohortName string) {
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
	addInadmissibleTestQueue(ctx, t, m, "cq-a", "lq-a", "ns-a", "shared")
	addInadmissibleTestQueue(ctx, t, m, "cq-b", "lq-b", "ns-b", "shared")
	wlA := addInadmissibleTestWorkload(ctx, t, m, cl, "cq-a", "lq-a", "ns-a", "wl-a")
	wlB := addInadmissibleTestWorkload(ctx, t, m, cl, "cq-b", "lq-b", "ns-b", "wl-b")

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

// A Namespace read failure should not retry a Workload whose own backoff has not expired.
func TestInadmissibleRequeueBackoffDoesNotRetry(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	reads := 0
	cl := utiltesting.NewClientBuilder().WithObjects(utiltesting.MakeNamespace("ns")).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, delegate client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*corev1.Namespace); ok {
					reads++
					return apierrors.NewServiceUnavailable("injected transient failure")
				}
				return delegate.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := NewRequeuer()
	defer r.queue.ShutDown()
	m := NewManager(cl, nil, r)
	addInadmissibleTestQueue(ctx, t, m, "cq", "lq", "ns", "")
	wl := addInadmissibleTestWorkload(ctx, t, m, cl, "cq", "lq", "ns", "in-backoff")
	cq := m.getClusterQueue("cq")
	cq.rwm.Lock()
	pending := cq.workloads.GetInadmissible(workload.Key(wl))
	workload.SetRequeuedCondition(pending.Obj, "Backoff", "waiting", false)
	inBackoff := !cq.backoffWaitingTimeExpired(pending)
	cq.rwm.Unlock()
	if !inBackoff {
		t.Fatal("Workload should be in backoff")
	}
	req := requeueRequest{ClusterQueue: "cq"}
	r.reconcile(ctx, req)
	if reads != 0 || r.queue.NumRequeues(req) != 0 {
		t.Errorf("ineligible Workload should not trigger Namespace Get/retry: reads=%d, retries=%d", reads, r.queue.NumRequeues(req))
	}
	if cq.workloads.GetInadmissible(workload.Key(wl)) == nil {
		t.Error("Workload in backoff must remain inadmissible")
	}
}

// A cache miss should not consume the only requeue signal. The fake client
// models an informer cache that learns about the Namespace after the first pass.
func TestInadmissibleRequeueMissingNamespaceRetries(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewFakeClient()
	r := NewRequeuer()
	defer r.queue.ShutDown()
	m := NewManager(cl, nil, r)
	addInadmissibleTestQueue(ctx, t, m, "cq", "lq", "ns", "")
	wl := addInadmissibleTestWorkload(ctx, t, m, cl, "cq", "lq", "ns", "wl")
	req := requeueRequest{ClusterQueue: "cq"}

	r.reconcile(ctx, req)
	if got := r.queue.NumRequeues(req); got != 1 {
		t.Fatalf("Namespace cache miss: NumRequeues = %d, want 1", got)
	}
	if got := m.getClusterQueue("cq").workloads.GetInadmissible(workload.Key(wl)); got == nil {
		t.Fatal("Workload must remain inadmissible until its Namespace is visible")
	}

	if err := cl.Create(ctx, utiltesting.MakeNamespace("ns")); err != nil {
		t.Fatalf("create Namespace in cache: %v", err)
	}
	r.reconcile(ctx, req)
	if got := r.queue.NumRequeues(req); got != 0 {
		t.Errorf("successful cache recovery: NumRequeues = %d, want 0", got)
	}
	if got := m.getClusterQueue("cq").workloads.GetActive(workload.Key(wl)); got == nil {
		t.Error("Workload was not activated after Namespace became visible")
	}
}
