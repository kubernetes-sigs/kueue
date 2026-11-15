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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestClusterQueueReconcileRecoversFailedInitialBackfill(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Generation(1).Obj()
	cq.Finalizers = []string{kueue.ResourceInUseFinalizerName}
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	errList := errors.New("injected LocalQueue List failure")
	failOnce := false
	failedLists := 0

	cl := utiltesting.NewClientBuilder().
		WithObjects(cq, lq, wl).
		WithStatusSubresource(cq, lq).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.LocalQueueList); ok && failOnce {
					failOnce = false
					failedLists++
					return errList
				}
				return delegate.List(ctx, list, opts...)
			},
		}).
		Build()
	cache := schdcache.New(cl)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to scheduler cache: %v", err)
	}
	manager := qcache.NewManagerForUnitTests(cl, cache)
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("adding existing LocalQueue: %v", err)
	}
	r := NewClusterQueueReconciler(cl, manager, cache)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(cq)}
	failOnce = true

	if _, err := r.Reconcile(ctx, req); !errors.Is(err, errList) {
		t.Fatalf("first Reconcile error = %v, want %v", err, errList)
	}
	if manager.ClusterQueueExists("cq") {
		t.Fatal("failed registration left a ClusterQueue in the queue manager")
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retrying Reconcile: %v", err)
	}
	if !manager.ClusterQueueExists("cq") {
		t.Fatal("ClusterQueue was not registered after recovery")
	}
	if pending, err := manager.Pending(cq); err != nil || pending != 1 {
		t.Fatalf("recovered pending Workloads = (%d, %v), want (1, nil)", pending, err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile should be idempotent after recovery: %v", err)
	}
	if failedLists != 1 {
		t.Fatalf("injected failures = %d, want 1", failedLists)
	}
}

func TestLocalQueueReconcileRecoversFailedInitialBackfill(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	errList := errors.New("injected Workload List failure")
	failOnce := false
	failedLists := 0

	cl := utiltesting.NewClientBuilder().
		WithObjects(cq, lq, wl).
		WithStatusSubresource(cq, lq).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.WorkloadList); ok && failOnce {
					failOnce = false
					failedLists++
					return errList
				}
				return delegate.List(ctx, list, opts...)
			},
		}).
		Build()
	cache := schdcache.New(cl)
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to scheduler cache: %v", err)
	}
	manager := qcache.NewManagerForUnitTests(cl, cache)
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue to queue manager: %v", err)
	}
	r := NewLocalQueueReconciler(cl, manager, cache)
	req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(lq)}
	failOnce = true

	if _, err := r.Reconcile(ctx, req); !errors.Is(err, errList) {
		t.Fatalf("first Reconcile error = %v, want %v", err, errList)
	}
	if manager.LocalQueueExists(utilqueue.Key(lq)) {
		t.Fatal("failed registration left a LocalQueue in the queue manager")
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retrying Reconcile: %v", err)
	}
	if !manager.LocalQueueExists(utilqueue.Key(lq)) {
		t.Fatal("LocalQueue was not registered after recovery")
	}
	if pending, err := manager.PendingWorkloads(lq); err != nil || pending != 1 {
		t.Fatalf("recovered pending Workloads = (%d, %v), want (1, nil)", pending, err)
	}
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile should be idempotent after recovery: %v", err)
	}
	if failedLists != 1 {
		t.Fatalf("injected failures = %d, want 1", failedLists)
	}
}
