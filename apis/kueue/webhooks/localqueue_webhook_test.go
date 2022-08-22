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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"

	. "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	testLocalQueueName      = "test-queue"
	testLocalQueueNamespace = "test-queue-ns"
)

func TestValidateLocalQueueCreate(t *testing.T) {
	testCases := map[string]struct {
		queue   *LocalQueue
		wantErr field.ErrorList
	}{
		"should reject queue creation with an invalid clusterQueue": {
			queue: testingutil.MakeLocalQueue(testLocalQueueName, testLocalQueueNamespace).ClusterQueue("invalid_cluster_queue").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("clusterQueue"), "invalid_name", ""),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			errList := ValidateLocalQueue(tc.queue)
			if diff := cmp.Diff(tc.wantErr, errList, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateLocalQueueCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateLocalQueueUpdate(t *testing.T) {
	testCases := map[string]struct {
		before, after *LocalQueue
		wantErr       field.ErrorList
	}{
		"clusterQueue cannot be updated": {
			before: testingutil.MakeLocalQueue(testLocalQueueName, testLocalQueueNamespace).ClusterQueue("foo").Obj(),
			after:  testingutil.MakeLocalQueue(testLocalQueueName, testLocalQueueNamespace).ClusterQueue("bar").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("clusterQueue"), nil, ""),
			},
		},
		"status could be updated": {
			before:  testingutil.MakeLocalQueue(testLocalQueueName, testLocalQueueNamespace).Obj(),
			after:   testingutil.MakeLocalQueue(testLocalQueueName, testLocalQueueNamespace).PendingWorkloads(10).Obj(),
			wantErr: field.ErrorList{},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			errList := ValidateLocalQueueUpdate(tc.before, tc.after)
			if diff := cmp.Diff(tc.wantErr, errList, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateLocalQueueUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
