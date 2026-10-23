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
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestAdmissionCheckReconcile(t *testing.T) {
	cases := map[string]struct {
		ac            *kueue.AdmissionCheck
		clusterQueues []*kueue.ClusterQueue
		deleted       bool
		wantFinalizer bool
		wantDeleted   bool
		skipStore     bool
	}{
		"adds finalizer when missing": {
			ac:            utiltestingapi.MakeAdmissionCheck("check").Obj(),
			wantFinalizer: true,
		},
		"no-op when finalizer already present": {
			ac:            utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			wantFinalizer: true,
		},
		"keeps finalizer when deleted but still in use": {
			ac: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("check").Obj(),
			},
			deleted:       true,
			wantFinalizer: true,
		},
		"removes finalizer when deleted and not in use": {
			ac:          utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			deleted:     true,
			wantDeleted: true,
		},
		"ignores not-found AdmissionCheck": {
			ac:        utiltestingapi.MakeAdmissionCheck("check").Obj(),
			skipStore: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			builder := utiltesting.NewClientBuilder()
			if !tc.skipStore {
				builder = builder.WithObjects(tc.ac)
			}
			cl := builder.Build()
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, nil)
			tracker := roletracker.NewFakeRoleTracker(roletracker.RoleLeader)
			reconciler := NewAdmissionCheckReconciler(cl, qManager, cqCache, tracker)

			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("failed to add ClusterQueue to cache: %v", err)
				}
			}

			if tc.deleted {
				if err := cl.Delete(ctx, tc.ac); err != nil {
					t.Fatalf("failed to delete AdmissionCheck: %v", err)
				}
			}

			req := reconcile.Request{Name: tc.ac.Name}
			_, err := reconciler.Reconcile(ctx, req)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tc.skipStore {
				return
			}

			got := &kueue.AdmissionCheck{}
			getErr := cl.Get(ctx, types.NamespacedName{Name: tc.ac.Name}, got)
			if tc.wantDeleted {
				if !apierrors.IsNotFound(getErr) {
					t.Errorf("expected AdmissionCheck to be deleted, got: %v", getErr)
				}
				return
			}

			if getErr != nil {
				t.Fatalf("failed to get AdmissionCheck: %v", getErr)
			}

			gotFinalizer := controllerutil.ContainsFinalizer(got, kueue.ResourceInUseFinalizerName)
			if gotFinalizer != tc.wantFinalizer {
				t.Errorf("finalizer present = %v, want %v", gotFinalizer, tc.wantFinalizer)
			}
		})
	}
}
