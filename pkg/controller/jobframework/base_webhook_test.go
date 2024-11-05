/*
Copyright 2024 The Kubernetes Authors.

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

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
)

type testGenericJob batchv1.Job

var _ jobframework.GenericJob = (*testGenericJob)(nil)

func (t *testGenericJob) Object() client.Object {
	return (*batchv1.Job)(t)
}

func (t *testGenericJob) IsSuspended() bool {
	return ptr.Deref(t.Spec.Suspend, false)
}

func (t *testGenericJob) Suspend() {
	t.Spec.Suspend = ptr.To(true)
}

func (t *testGenericJob) RunWithPodSetsInfo([]podset.PodSetInfo) error {
	panic("not implemented")
}

func (t *testGenericJob) RestorePodSetsInfo([]podset.PodSetInfo) bool {
	panic("not implemented")
}

func (t *testGenericJob) Finished() (string, bool, bool) {
	panic("not implemented")
}

func (t *testGenericJob) PodSets() []kueue.PodSet {
	panic("not implemented")
}

func (t *testGenericJob) IsActive() bool {
	panic("not implemented")
}

func (t *testGenericJob) PodsReady() bool {
	panic("not implemented")
}

func (t *testGenericJob) GVK() schema.GroupVersionKind {
	panic("not implemented")
}

func fromObject(o runtime.Object) jobframework.GenericJob {
	return (*testGenericJob)(o.(*batchv1.Job))
}

func TestBaseWebhookDefault(t *testing.T) {
	testcases := map[string]struct {
		manageJobsWithoutQueueName bool

		job  *batchv1.Job
		want *batchv1.Job
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			want: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
				Spec: batchv1.JobSpec{Suspend: ptr.To(true)},
			},
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			manageJobsWithoutQueueName: true,
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			want: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
				Spec: batchv1.JobSpec{Suspend: ptr.To(true)},
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			w := &jobframework.BaseWebhook{
				ManageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				FromObject:                 fromObject,
			}
			if err := w.Default(context.Background(), tc.job); err != nil {
				t.Errorf("set defaults to a kubeflow/mpijob by a Defaulter")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
