/*
Copyright 2022 The Kubernetes Authors.

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

package job

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestValidateCreate(t *testing.T) {
	testcases := []struct {
		name    string
		job     *batchv1.Job
		wantErr error
	}{
		{
			name:    "simple",
			job:     testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name:    "valid parent-workload annotation",
			job:     testingutil.MakeJob("job", "default").ParentWorkload("parent-workload-name").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name:    "invalid parent-workload annotation",
			job:     testingutil.MakeJob("job", "default").ParentWorkload("parent workload name").Queue("queue").Obj(),
			wantErr: field.Invalid(parentWorkloadKeyPath, "parent workload name", `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := validateCreate(tc.job)

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	suspendPath := field.NewPath("job", "spec", "suspend")

	testcases := []struct {
		name    string
		oldJob  *batchv1.Job
		newJob  *batchv1.Job
		wantErr error
	}{
		{
			name:    "normal update",
			oldJob:  testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantErr: nil,
		},
		{
			name:    "add queue name with suspend is false",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantErr: field.Forbidden(suspendPath, "suspend should be true when adding the queue name"),
		},
		{
			name:    "add queue name with suspend is true",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantErr: nil,
		},
		{
			name:    "change queue name with suspend is false",
			oldJob:  testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue2").Suspend(false).Obj(),
			wantErr: field.Forbidden(suspendPath, "should not update queue name when job is unsuspend"),
		},
		{
			name:    "change queue name with suspend is true",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantErr: nil,
		},
		{
			name:    "update the nil parent workload to non-empty",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").ParentWorkload("parent").Obj(),
			wantErr: field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable"),
		},
		{
			name:    "update the non-empty parent workload to nil",
			oldJob:  testingutil.MakeJob("job", "default").ParentWorkload("parent").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Obj(),
			wantErr: field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable"),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := validateUpdate(tc.oldJob, tc.newJob)

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("validateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
