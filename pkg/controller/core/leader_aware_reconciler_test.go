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
	"testing"

	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

// Fake Reconciler that records which method was invoked
type fakeObserverReconciler struct {
	reconcileCalled bool
	observeCalled   bool
}

func (f *fakeObserverReconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	f.reconcileCalled = true
	return reconcile.Result{}, nil
}

func (f *fakeObserverReconciler) Observe(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	f.observeCalled = true
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
