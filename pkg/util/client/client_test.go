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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

var (
	errTestNotFound = apierrors.NewNotFound(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"test",
	)
	errTestConflict = apierrors.NewConflict(
		schema.GroupResource{Group: "batch", Resource: "jobs"},
		"test",
		errors.New("object was modified"),
	)
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
		obj     *batchv1.Job
		update  func(job *batchv1.Job) UpdateFunc
		options []PatchOption
	}
	type want struct {
		err error
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
				obj: newObject("1"), // outdated local object.
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Spec.Suspend = ptr.To(true)
						return true, nil
					}
				},
			},
			want: want{
				err: errTestConflict,
				obj: newObject("2"),
			},
		},
		"Strict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Spec.Suspend = ptr.To(true)
						job.Status.Active = 1
						return true, nil
					}
				},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					// Change to Spec is applied; Status change is ignored because Patch updates meta and spec only.
					job.Spec.Suspend = ptr.To(true)
				}),
			},
		},
		"NotStrict_OutdatedLocalObject": {
			args: args{
				obj: newObject("1"), // outdated local object.
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Spec.Suspend = ptr.To(true)
						return true, nil
					}
				},
				options: []PatchOption{WithLoose()},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					job.Spec.Suspend = ptr.To(true)
				}),
			},
		},
		"NotStrict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Spec.Suspend = ptr.To(true)
						return true, nil
					}
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
			args: args{
				obj: newObject("1"), // outdated local object.
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						return false, nil
					}
				},
			},
			want: want{
				obj: newObject("2"),
			},
		},
		"Error": {
			args: args{
				obj: newObject("2"),
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						return false, errTestNotFound
					}
				},
			},
			want: want{
				err: errTestNotFound,
				obj: newObject("2"),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clnt := utiltesting.NewClientBuilder().WithObjects(clientObject).Build()
			err := Patch(ctx, clnt, tt.args.obj, tt.args.update(tt.args.obj), tt.args.options...)
			if diff := cmp.Diff(tt.want.err, err); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
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
		obj     *batchv1.Job
		update  func(job *batchv1.Job) UpdateFunc
		options []PatchOption
	}
	type want struct {
		err error
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
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Status.Active = 1
						return true, nil
					}
				},
			},
			want: want{
				err: errTestConflict,
				obj: newObject("2"),
			},
		},
		"Strict_CurrentLocalObject": {
			args: args{
				obj: newObject("2"),
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Status.Active = 1
						job.Spec.Suspend = ptr.To(true)
						return true, nil
					}
				},
			},
			want: want{
				obj: newObject("3", func(job *batchv1.Job) {
					// Change to Status is applied; Spec change is ignored because Patch updates status only.
					job.Status.Active = 1
				}),
			},
		},
		"NotStrict_OutdatedLocalObject": {
			args: args{
				obj: newObject("1"), // outdated local object.
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Status.Active = 1
						return true, nil
					}
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
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Status.Active = 1
						return true, nil
					}
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
			args: args{
				obj: newObject("1"), // outdated local object.
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						return false, nil
					}
				},
			},
			want: want{
				obj: newObject("2"),
			},
		},
		"Error": {
			args: args{
				obj: newObject("2"),
				update: func(job *batchv1.Job) UpdateFunc {
					return func() (bool, error) {
						job.Status.Active = 1
						return true, errTestNotFound
					}
				},
			},
			want: want{
				err: errTestNotFound,
				obj: newObject("2"),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clnt := utiltesting.NewClientBuilder().WithObjects(clientObject).Build()
			err := PatchStatus(ctx, clnt, tt.args.obj, tt.args.update(tt.args.obj), tt.args.options...)
			if diff := cmp.Diff(tt.want.err, err); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
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
