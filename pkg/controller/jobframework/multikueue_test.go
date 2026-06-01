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

package jobframework_test

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestDeleteRemoteObjectIfOwnedByMultiKueue(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding batch scheme: %v", err)
	}

	const (
		ns   = "default"
		name = "the-job"
	)
	key := types.NamespacedName{Namespace: ns, Name: name}

	makeJob := func(labels map[string]string) *batchv1.Job {
		return &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: ns,
				Name:      name,
				Labels:    labels,
			},
		}
	}

	cases := map[string]struct {
		existing      *batchv1.Job
		wantErr       bool
		wantDeleted   bool
		wantRemaining bool
	}{
		"remote object missing: no error, nothing to do": {
			existing:      nil,
			wantDeleted:   false,
			wantRemaining: false,
		},
		"remote object exists without origin label: skip deletion": {
			existing:      makeJob(map[string]string{"unrelated": "label"}),
			wantDeleted:   false,
			wantRemaining: true,
		},
		"remote object exists with different origin: skip deletion": {
			existing:      makeJob(map[string]string{kueue.MultiKueueOriginLabel: "manager-2"}),
			wantDeleted:   false,
			wantRemaining: true,
		},
		"remote object exists with nil labels: skip deletion": {
			existing:      makeJob(nil),
			wantDeleted:   false,
			wantRemaining: true,
		},
		"remote object owned by MultiKueue: deleted": {
			existing:      makeJob(map[string]string{kueue.MultiKueueOriginLabel: "manager-1"}),
			wantDeleted:   true,
			wantRemaining: false,
		},
	}

	for tcName, tc := range cases {
		t.Run(tcName, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if tc.existing != nil {
				builder = builder.WithObjects(tc.existing)
			}
			c := builder.Build()

			err := jobframework.DeleteRemoteObjectIfOwnedByMultiKueue(
				ctx, c, key, "manager-1", &batchv1.Job{},
				client.PropagationPolicy(metav1.DeletePropagationBackground),
			)
			if (err != nil) != tc.wantErr {
				t.Fatalf("unexpected error: got %v, wantErr=%v", err, tc.wantErr)
			}

			got := &batchv1.Job{}
			getErr := c.Get(ctx, key, got)
			switch {
			case tc.wantRemaining && getErr != nil:
				t.Fatalf("expected job to remain, but Get returned error: %v", getErr)
			case !tc.wantRemaining && getErr == nil:
				t.Fatalf("expected job to be deleted or absent, but Get succeeded")
			case !tc.wantRemaining && !apierrors.IsNotFound(getErr):
				t.Fatalf("expected NotFound, got: %v", getErr)
			}
		})
	}
}

func TestDeleteRemoteObjectIfOwnedByMultiKueue_UIDPrecondition(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding batch scheme: %v", err)
	}

	const (
		ns   = "default"
		name = "the-job"
		uid  = types.UID("uid-1")
	)

	existing := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			UID:       uid,
			Labels:    map[string]string{kueue.MultiKueueOriginLabel: "manager-1"},
		},
	}

	var gotPreconditions *metav1.Preconditions
	base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build()
	c := interceptor.NewClient(base, interceptor.Funcs{
		Delete: func(ctx context.Context, cl client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
			do := &client.DeleteOptions{}
			do.ApplyOptions(opts)
			gotPreconditions = do.Preconditions
			return cl.Delete(ctx, obj, opts...)
		},
	})

	ctx, _ := utiltesting.ContextWithLog(t)
	if err := jobframework.DeleteRemoteObjectIfOwnedByMultiKueue(
		ctx, c, types.NamespacedName{Namespace: ns, Name: name}, "manager-1", &batchv1.Job{},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotPreconditions == nil || gotPreconditions.UID == nil {
		t.Fatalf("expected UID precondition to be set, got %+v", gotPreconditions)
	}
	if *gotPreconditions.UID != uid {
		t.Fatalf("expected UID precondition %q, got %q", uid, *gotPreconditions.UID)
	}
}

func TestValidateRemoteObjectOwnership(t *testing.T) {
	makeJob := func(labels map[string]string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", Labels: labels}}
	}

	tests := map[string]struct {
		obj     *batchv1.Job
		origin  string
		wantErr bool
	}{
		"origin not set skips validation": {
			obj:     makeJob(nil),
			origin:  "",
			wantErr: false,
		},
		"origin set and label missing": {
			obj:     makeJob(nil),
			origin:  "origin-1",
			wantErr: true,
		},
		"origin set and label present": {
			obj:     makeJob(map[string]string{kueue.MultiKueueOriginLabel: "origin-1"}),
			origin:  "origin-1",
			wantErr: false,
		},
		"origin set and label mismatched": {
			obj:     makeJob(map[string]string{kueue.MultiKueueOriginLabel: "origin-2"}),
			origin:  "origin-1",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			err := jobframework.ValidateRemoteObjectOwnership(tc.obj, tc.origin)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateRemoteObjectOwnership() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
