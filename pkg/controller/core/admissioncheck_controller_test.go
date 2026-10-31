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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestAdmissionCheckReconcile(t *testing.T) {
	errUpdate := errors.New("update failed")
	now := time.Now().Truncate(time.Second)
	cases := map[string]struct {
		ac                 *kueue.AdmissionCheck
		clusterQueues      []*kueue.ClusterQueue
		updateErr          error
		wantAdmissionCheck *kueue.AdmissionCheck
		wantUpdates        int
		wantErr            error
	}{
		"adds finalizer when missing": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Obj(),
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			wantUpdates:        1,
		},
		"no-op when finalizer already present": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).Obj(),
		},
		"keeps finalizer when deleted but still in use": {
			ac: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("check").Obj(),
			},
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
		},
		"removes finalizer when deleted and not in use": {
			ac: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("unrelated").AdmissionChecks("other").Obj(),
			},
			wantUpdates: 1,
		},
		"removes only the Kueue finalizer when another finalizer is present": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName, "example.com/finalizer").DeletionTimestamp(now).Obj(),
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers("example.com/finalizer").DeletionTimestamp(now).Obj(),
			wantUpdates:        1,
		},
		"ignores not-found AdmissionCheck": {},
		"returns error when Update fails while adding finalizer": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Obj(),
			updateErr:          errUpdate,
			wantErr:            errUpdate,
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Obj(),
			wantUpdates:        1,
		},
		"returns error when Update fails while removing finalizer": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			updateErr:          errUpdate,
			wantErr:            errUpdate,
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			wantUpdates:        1,
		},
		"ignores NotFound from Update while adding finalizer": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Obj(),
			updateErr:          apierrors.NewNotFound(schema.GroupResource{Resource: "admissionchecks"}, "check"),
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Obj(),
			wantUpdates:        1,
		},
		"ignores NotFound from Update while removing finalizer": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			updateErr:          apierrors.NewNotFound(schema.GroupResource{Resource: "admissionchecks"}, "check"),
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers(kueue.ResourceInUseFinalizerName).DeletionTimestamp(now).Obj(),
			wantUpdates:        1,
		},
		"no-op when deleted without the Kueue finalizer": {
			ac:                 utiltestingapi.MakeAdmissionCheck("check").Finalizers("example.com/finalizer").DeletionTimestamp(now).Obj(),
			wantAdmissionCheck: utiltestingapi.MakeAdmissionCheck("check").Finalizers("example.com/finalizer").DeletionTimestamp(now).Obj(),
		},
	}

	req := reconcile.Request{Name: "check"}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			var gotUpdates int
			builder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
				Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
					gotUpdates++
					if tc.updateErr != nil {
						return tc.updateErr
					}
					return c.Update(ctx, obj, opts...)
				},
			})

			if tc.ac != nil {
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

			res, err := reconciler.Reconcile(ctx, req)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(reconcile.Result{}, res); diff != "" {
				t.Errorf("unexpected reconcile result (-want +got):\n%s", diff)
			}

			if gotUpdates != tc.wantUpdates {
				t.Errorf("update called %d times, want %d", gotUpdates, tc.wantUpdates)
			}

			var got *kueue.AdmissionCheck
			fetched := &kueue.AdmissionCheck{}
			if err := cl.Get(ctx, req.NamespacedName, fetched); err == nil {
				got = fetched
			} else if !apierrors.IsNotFound(err) {
				t.Fatalf("failed to get AdmissionCheck: %v", err)
			}

			if diff := cmp.Diff(tc.wantAdmissionCheck, got, cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected AdmissionCheck (-want +got):\n%s", diff)
			}
		})
	}
}
