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

	batchv1 "k8s.io/api/batch/v1"

	"sigs.k8s.io/kueue/pkg/util/pointer"
)

func TestPodsReady(t *testing.T) {
	testcases := map[string]struct {
		job  batchv1.Job
		want bool
	}{
		"parallelism = completions; no progress": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{},
			},
			want: false,
		},
		"parallelism = completions; not enough progress": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Ready:     pointer.Int32(1),
					Succeeded: 1,
				},
			},
			want: false,
		},
		"parallelism = completions; all ready": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Ready:     pointer.Int32(3),
					Succeeded: 0,
				},
			},
			want: true,
		},
		"parallelism = completions; some ready, some succeeded": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Ready:     pointer.Int32(2),
					Succeeded: 1,
				},
			},
			want: true,
		},
		"parallelism = completions; all succeeded": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Succeeded: 3,
				},
			},
			want: true,
		},
		"parallelism < completions; reaching parallelism is enough": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(2),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Ready: pointer.Int32(2),
				},
			},
			want: true,
		},
		"parallelism > completions; reaching completions is enough": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(2),
				},
				Status: batchv1.JobStatus{
					Ready: pointer.Int32(2),
				},
			},
			want: true,
		},
		"parallelism specified only; not enough progress": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Ready: pointer.Int32(2),
				},
			},
			want: false,
		},
		"parallelism specified only; all ready": {
			job: batchv1.Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{
					Ready: pointer.Int32(3),
				},
			},
			want: true,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			batchJob := &Job{&tc.job}
			got := batchJob.PodsReady()
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}
