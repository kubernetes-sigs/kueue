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

package client

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// newObject creates and returns a new *batchv1.Job initialized with the given
// resourceVersion and default metadata (Namespace="default", Name="test").
//
// The returned Job can be further customized by applying the provided option
// functions. Each option is a function that mutates the Job before it is
// returned, allowing flexible and reusable configuration in tests or setup code.
//
// Used in TestPatch and TestPatchStatus.
func newObject(resourceVersion string, opts ...func(*batchv1.Job)) *batchv1.Job {
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:       "default",
			Name:            "test",
			ResourceVersion: resourceVersion,
		},
	}
	for _, opt := range opts {
		opt(job)
	}
	return job
}

func TestPatch(t *testing.T) {
	type args struct {
		// context: initialized in t.Run().
		// client: initialized in t.Run().
		obj     client.Object
		update  func() (client.Object, bool, error)
		options []PatchOption
	}
	type want struct {
		err bool
		obj client.Object // To assert patched object.
	}
	// clientObject is used to initialize test Client in t.Run().
	clientObject := newObject("2")

	tests := map[string]struct {
		args args
		want want
	}{
		"Strict_OutdatedLocalObject": {
			args: args{
				obj: newObject("1"), // outdated local object results in patch error.
				update: func() (client.Object, bool, error) {
					obj := newObject("1")
					obj.Spec.Suspend = ptr.To(true)
					return obj, true, nil
				},
			},
			want: want{
				err: true,           // object was modified error.
				obj: newObject("2"), // unchanged.
			},
		},
		"Strict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func() (client.Object, bool, error) {
					obj := newObject("2")
					obj.Spec.Suspend = ptr.To(true)
					obj.Status.Active = 1
					return obj, true, nil
				},
			},
			want: want{
				obj: newObject("3", // post-patch incremented resource version.
					func(job *batchv1.Job) {
						// Change to Spec is applied; Status change is ignored because Patch updates meta and spec only.
						job.Spec.Suspend = ptr.To(true)
					}),
			},
		},
		"NotStrict_OutdatedLocalObject": {
			args: args{
				obj: newObject("1"), // outdated local object.
				update: func() (client.Object, bool, error) {
					obj := newObject("1")
					obj.Spec.Suspend = ptr.To(true)
					return obj, true, nil
				},
				options: []PatchOption{WithLoose()},
			},
			want: want{
				// Unlike "Strict" version - this update is successful since the resource version is not
				// included in the patch.
				obj: newObject("3", func(job *batchv1.Job) {
					job.Spec.Suspend = ptr.To(true)
				}),
			},
		},
		"NotStrict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func() (client.Object, bool, error) {
					obj := newObject("2")
					obj.Spec.Suspend = ptr.To(true)
					return obj, true, nil
				},
				options: []PatchOption{WithLoose()},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					job.Spec.Suspend = ptr.To(true)
				}),
			},
		},
		"NoChanges": {
			// Modeled after "Strict_OutdatedLocalObject"; however since we are returning
			// updated: false - it makes no difference if the local object is outdated.
			args: args{
				obj: newObject("1"), // outdated local object results in patch error.
				update: func() (client.Object, bool, error) {
					return newObject("1"), false, nil
				},
			},
			want: want{
				obj: newObject("2"),
			},
		},
		"Error": {
			args: args{
				obj: newObject("2"),
				update: func() (client.Object, bool, error) {
					return newObject("2"), true, errors.New("test-error")
				},
			},
			want: want{
				err: true,
				obj: newObject("2"),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clnt := utiltesting.NewClientBuilder().WithObjects(clientObject).Build()
			if err := Patch(ctx, clnt, tt.args.obj, tt.args.update, tt.args.options...); (err != nil) != tt.want.err {
				t.Errorf("Patch() error = %v, wantErr %v", err, tt.want.err)
			}
			if err := clnt.Get(ctx, client.ObjectKeyFromObject(tt.args.obj), tt.args.obj); err != nil {
				t.Fatalf("Patch() unexpected error getting object: %v", err)
			}
			if diff := cmp.Diff(tt.want.obj, tt.args.obj); diff != "" {
				t.Errorf("Patch() object (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPatchStatus(t *testing.T) {
	type args struct {
		// context: initialized in t.Run().
		// client: initialized in t.Run().
		obj     client.Object
		update  func() (client.Object, bool, error)
		options []PatchOption
	}
	type want struct {
		err bool
		obj client.Object // To assert patched object.
	}
	// clientObject is used to initialize test Client in t.Run().
	clientObject := newObject("2")

	tests := map[string]struct {
		args args
		want want
	}{
		"Strict_OutdatedLocalObject": {
			args: args{
				obj: newObject("1"), // outdated local object results in patch error.
				update: func() (client.Object, bool, error) {
					return newObject("1"), true, nil
				},
			},
			want: want{
				err: true,           // object was modified error.
				obj: newObject("2"), // unchanged.
			},
		},
		"Strict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func() (client.Object, bool, error) {
					obj := newObject("2")
					obj.Status.Active = 1
					obj.Spec.Suspend = ptr.To(true)
					return obj, true, nil
				},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					// Change to Status is applied; Spec change is ignored because Patch updates status only.
					job.Status.Active = 1
				}), // incremented.
			},
		},
		"NotStrict_OutdatedLocalObject": {
			args: args{
				obj: newObject("1"), // outdated local object.
				update: func() (client.Object, bool, error) {
					obj := newObject("1")
					obj.Status.Active = 1
					return obj, true, nil
				},
				options: []PatchOption{WithLoose()},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					job.Status.Active = 1
				}),
			},
		},
		"NotStrict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func() (client.Object, bool, error) {
					obj := newObject("2")
					obj.Status.Active = 1
					return obj, true, nil
				},
				options: []PatchOption{WithLoose()},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					job.Status.Active = 1
				}),
			},
		},
		"NoChanges": {
			// Modeled after "Strict_OutdatedLocalObject"; however since we are returning
			// false - it makes no difference if the local object is outdated.
			args: args{
				obj: newObject("1"), // outdated local object results in patch error.
				update: func() (client.Object, bool, error) {
					return newObject("1"), false, nil
				},
			},
			want: want{
				obj: newObject("2"),
			},
		},
		"Error": {
			args: args{
				obj: newObject("2"),
				update: func() (client.Object, bool, error) {
					obj := newObject("2")
					obj.Status.Active = 1
					return obj, true, errors.New("test-error")
				},
			},
			want: want{
				err: true,
				obj: newObject("2"),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clnt := utiltesting.NewClientBuilder().WithObjects(clientObject).Build()
			if err := PatchStatus(ctx, clnt, tt.args.obj, tt.args.update, tt.args.options...); (err != nil) != tt.want.err {
				t.Errorf("Patch() error = %v, wantErr %v", err, tt.want.err)
			}
			if err := clnt.Get(ctx, client.ObjectKeyFromObject(tt.args.obj), tt.args.obj); err != nil {
				t.Fatalf("Patch() unexpected error getting object: %v", err)
			}
			if diff := cmp.Diff(tt.want.obj, tt.args.obj); diff != "" {
				t.Errorf("Patch() object (-want +got):\n%s", diff)
			}
		})
	}
}
