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
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// The controllers this decorates run with more than one worker, so several requests reach the
// non-leading branch at once. The client writes what it read into the object it is handed, so each
// request needs a destination of its own.
func TestLeaderAwareReconcilerNonLeadingDestinations(t *testing.T) {
	const requests = 2

	var (
		mu           sync.Mutex
		destinations []client.Object
	)
	inGet := make(chan struct{}, requests)
	release := make(chan struct{})

	cl := utiltesting.NewClientBuilder().
		WithObjects(
			utiltestingapi.MakeWorkload("first", "ns").Obj(),
			utiltestingapi.MakeWorkload("second", "ns").Obj(),
		).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				mu.Lock()
				destinations = append(destinations, obj)
				mu.Unlock()
				// Hold both requests inside Get so they overlap the way two workers would.
				inGet <- struct{}{}
				<-release
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	reconciler := &leaderAwareReconciler{
		elected:         make(chan struct{}), // never closed, so the replica never leads
		client:          cl,
		objectPrototype: &kueue.Workload{},
		requeueDuration: time.Second,
	}

	var wg sync.WaitGroup
	for _, name := range []string{"first", "second"} {
		wg.Go(func() {
			if _, err := reconciler.Reconcile(t.Context(), reconcile.Request{
				NamespacedName: types.NamespacedName{Namespace: "ns", Name: name},
			}); err != nil {
				t.Errorf("Reconcile(%q) returned %v", name, err)
			}
		})
	}
	for range requests {
		<-inGet
	}
	close(release)
	wg.Wait()

	if len(destinations) != requests {
		t.Fatalf("Get was called %d times, want %d", len(destinations), requests)
	}
	if destinations[0] == destinations[1] {
		t.Errorf("both requests decoded into %p, want a destination per request", destinations[0])
	}
}

// Fake Reconciler that records which method was invoked
type fakeObserverReconciler struct {
	reconcileCalled bool
	observeCalled   bool
	lastClient      client.Client
}

func (f *fakeObserverReconciler) Reconcile(ctx context.Context, req reconcile.Request, cl client.Client) (reconcile.Result, error) {
	f.reconcileCalled = true
	f.lastClient = cl
	return reconcile.Result{}, nil
}

func (f *fakeObserverReconciler) Observe(ctx context.Context, req reconcile.Request, cl client.Client) (reconcile.Result, error) {
	f.observeCalled = true
	f.lastClient = cl
	return reconcile.Result{}, nil
}

// Table-driven test function
func TestLeaderAwareReconcilerObserver(t *testing.T) {
	cases := map[string]struct {
		isLeader            bool
		wantReconcileCalled bool
		wantObserveCalled   bool
	}{
		"follower replica calls Observe": {
			isLeader:            false,
			wantReconcileCalled: false,
			wantObserveCalled:   true,
		},
		"leader replica calls Reconcile": {
			isLeader:            true,
			wantReconcileCalled: true,
			wantObserveCalled:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			elected := make(chan struct{})
			if tc.isLeader {
				close(elected) // Closed channel = Leader
			}

			fake := &fakeObserverReconciler{}
			wrapper := &leaderAwareReconcilerObserver{
				elected:  elected,
				delegate: fake,
			}

			_, err := wrapper.Reconcile(t.Context(), reconcile.Request{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if fake.reconcileCalled != tc.wantReconcileCalled {
				t.Errorf("got reconcileCalled=%v, want %v", fake.reconcileCalled, tc.wantReconcileCalled)
			}
			if fake.observeCalled != tc.wantObserveCalled {
				t.Errorf("got observeCalled=%v, want %v", fake.observeCalled, tc.wantObserveCalled)
			}
		})
	}
}
