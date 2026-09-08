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
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestReadOnlyClient(t *testing.T) {
	ctx := t.Context()
	existingJob := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "test-job",
		},
	}

	fakeClient := utiltesting.NewFakeClient(existingJob)
	readOnlyCl := NewReadOnlyClient(fakeClient)

	t.Run("Get succeeds", func(t *testing.T) {
		gotJob := &batchv1.Job{}
		err := readOnlyCl.Get(ctx, client.ObjectKeyFromObject(existingJob), gotJob)
		if err != nil {
			t.Errorf("expected Get to succeed, got %v", err)
		}
	})

	t.Run("List succeeds", func(t *testing.T) {
		gotList := &batchv1.JobList{}
		err := readOnlyCl.List(ctx, gotList, client.InNamespace("default"))
		if err != nil {
			t.Errorf("expected List to succeed, got %v", err)
		}
		if len(gotList.Items) != 1 {
			t.Errorf("expected 1 item, got %d", len(gotList.Items))
		}
	})

	t.Run("Create fails", func(t *testing.T) {
		newJob := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "new-job"},
		}
		err := readOnlyCl.Create(ctx, newJob)
		if err == nil {
			t.Error("expected Create to fail in follower mode")
		}
	})

	t.Run("Update fails", func(t *testing.T) {
		err := readOnlyCl.Update(ctx, existingJob)
		if err == nil {
			t.Error("expected Update to fail in follower mode")
		}
	})

	t.Run("Delete fails", func(t *testing.T) {
		err := readOnlyCl.Delete(ctx, existingJob)
		if err == nil {
			t.Error("expected Delete to fail in follower mode")
		}
	})

	t.Run("DeleteAllOf fails", func(t *testing.T) {
		err := readOnlyCl.DeleteAllOf(ctx, &batchv1.Job{}, client.InNamespace("default"))
		if err == nil {
			t.Error("expected DeleteAllOf to fail in follower mode")
		}
	})

	t.Run("Patch fails", func(t *testing.T) {
		patch := client.MergeFrom(existingJob)
		err := readOnlyCl.Patch(ctx, existingJob, patch)
		if err == nil {
			t.Error("expected Patch to fail in follower mode")
		}
	})

	t.Run("Status Create fails", func(t *testing.T) {
		err := readOnlyCl.Status().Create(ctx, existingJob, existingJob)
		if err == nil {
			t.Error("expected Status().Create to fail in follower mode")
		}
	})

	t.Run("Status Update fails", func(t *testing.T) {
		err := readOnlyCl.Status().Update(ctx, existingJob)
		if err == nil {
			t.Error("expected Status().Update to fail in follower mode")
		}
	})

	t.Run("Status Patch fails", func(t *testing.T) {
		patch := client.MergeFrom(existingJob)
		err := readOnlyCl.Status().Patch(ctx, existingJob, patch)
		if err == nil {
			t.Error("expected Status().Patch to fail in follower mode")
		}
	})

	t.Run("Status Apply fails", func(t *testing.T) {
		err := readOnlyCl.Status().Apply(ctx, nil)
		if err == nil {
			t.Error("expected Status().Apply to fail in follower mode")
		}
	})

	t.Run("SubResource Create fails", func(t *testing.T) {
		err := readOnlyCl.SubResource("scale").Create(ctx, existingJob, existingJob)
		if err == nil {
			t.Error("expected SubResource().Create to fail in follower mode")
		}
	})

	t.Run("SubResource Update fails", func(t *testing.T) {
		err := readOnlyCl.SubResource("scale").Update(ctx, existingJob)
		if err == nil {
			t.Error("expected SubResource().Update to fail in follower mode")
		}
	})

	t.Run("SubResource Patch fails", func(t *testing.T) {
		patch := client.MergeFrom(existingJob)
		err := readOnlyCl.SubResource("scale").Patch(ctx, existingJob, patch)
		if err == nil {
			t.Error("expected SubResource().Patch to fail in follower mode")
		}
	})

	t.Run("SubResource Apply fails", func(t *testing.T) {
		err := readOnlyCl.SubResource("scale").Apply(ctx, nil)
		if err == nil {
			t.Error("expected SubResource().Apply to fail in follower mode")
		}
	})

	t.Run("Apply fails", func(t *testing.T) {
		err := readOnlyCl.Apply(ctx, nil)
		if err == nil {
			t.Error("expected Apply to fail in follower mode")
		}
	})
}
