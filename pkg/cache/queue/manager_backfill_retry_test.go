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
	"errors"
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// TestAddLocalQueueListFailureDoesNotPublishPartialState verifies that a failed
// initial workload backfill does not make a LocalQueue appear registered.
func TestAddLocalQueueListFailureDoesNotPublishPartialState(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	errList := errors.New("injected workload List failure")
	failOnce := true
	cl := utiltesting.NewClientBuilder().
		WithObjects(wl).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.WorkloadList); ok && failOnce {
					failOnce = false
					return errList
				}
				return delegate.List(ctx, list, opts...)
			},
		}).
		Build()
	manager := NewManagerForUnitTests(cl, nil)
	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}

	if err := manager.AddLocalQueue(ctx, lq); !errors.Is(err, errList) {
		t.Fatalf("AddLocalQueue() error = %v, want %v", err, errList)
	}
	if _, exists := manager.localQueues[utilqueue.Key(lq)]; exists {
		t.Fatal("failed LocalQueue registration left a queue in the manager")
	}
	if _, exists := manager.hm.ClusterQueue("cq").lqWeights[utilqueue.Key(lq)]; exists {
		t.Fatal("failed LocalQueue registration left a ClusterQueue weight")
	}

	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("retrying AddLocalQueue: %v", err)
	}
	pending, err := manager.PendingWorkloads(lq)
	if err != nil || pending != 1 {
		t.Fatalf("PendingWorkloads() = (%d, %v), want (1, nil)", pending, err)
	}
	cqPending, err := manager.Pending(cq)
	if err != nil || cqPending != 1 {
		t.Fatalf("Pending() = (%d, %v), want (1, nil)", cqPending, err)
	}
	if err := manager.AddLocalQueue(ctx, lq); err == nil {
		t.Fatal("fully registered LocalQueue accepted a duplicate registration")
	}
}

// TestAddClusterQueueListFailureDoesNotPublishPartialState verifies that a
// failed initial LocalQueue backfill neither publishes the ClusterQueue nor
// leaves a Cohort relationship, and that a retry recovers existing workloads.
func TestAddClusterQueueListFailureDoesNotPublishPartialState(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq := utiltestingapi.MakeClusterQueue("cq").Cohort("cohort").Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Obj()
	errList := errors.New("injected LocalQueue List failure")
	failOnce := true
	cl := utiltesting.NewClientBuilder().
		WithObjects(lq, wl).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, delegate client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueue.LocalQueueList); ok && failOnce {
					failOnce = false
					return errList
				}
				return delegate.List(ctx, list, opts...)
			},
		}).
		Build()
	manager := NewManagerForUnitTests(cl, nil)
	if err := manager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("adding LocalQueue: %v", err)
	}

	if err := manager.AddClusterQueue(ctx, cq); !errors.Is(err, errList) {
		t.Fatalf("AddClusterQueue() error = %v, want %v", err, errList)
	}
	if manager.hm.ClusterQueue("cq") != nil {
		t.Fatal("failed ClusterQueue registration left a queue in the hierarchy")
	}
	if manager.hm.Cohort("cohort") != nil {
		t.Fatal("failed ClusterQueue registration left an implicit Cohort")
	}

	if err := manager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("retrying AddClusterQueue: %v", err)
	}
	pending, err := manager.Pending(cq)
	if err != nil || pending != 1 {
		t.Fatalf("Pending() = (%d, %v), want (1, nil)", pending, err)
	}
	if manager.hm.Cohort("cohort") == nil {
		t.Fatal("successful ClusterQueue registration did not create the Cohort edge")
	}
	if err := manager.AddClusterQueue(ctx, cq); err == nil {
		t.Fatal("fully registered ClusterQueue accepted a duplicate registration")
	}
}
