/*
Copyright 2023 The Kubernetes Authors.

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

package jobset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
)

func TestPodsReady(t *testing.T) {
	testcases := map[string]struct {
		jobSet jobset.JobSet
		want   bool
	}{
		"all jobs are ready": {
			jobSet: *testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    2,
					Parallelism: 1,
					Completions: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).JobsStatus(
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Ready:     1,
					Succeeded: 1,
				},
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Ready:     3,
					Succeeded: 0,
				},
			).Obj(),
			want: true,
		},
		"not all jobs are ready": {
			jobSet: *testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    2,
					Parallelism: 1,
					Completions: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).JobsStatus(
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Ready:     1,
					Succeeded: 0,
				},
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Ready:     1,
					Succeeded: 2,
				},
			).Obj(),
			want: false,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			jobSet := (JobSet)(tc.jobSet)
			got := jobSet.PodsReady()
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	jobset := testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
		testingjobset.ReplicatedJobRequirements{
			Name:        "replicated-job-1",
			Replicas:    1,
			Parallelism: 1,
			Completions: 1,
		},
		testingjobset.ReplicatedJobRequirements{
			Name:        "replicated-job-2",
			Replicas:    2,
			Parallelism: 2,
			Completions: 2,
		},
	).Obj()
	wantPodSets := []kueue.PodSet{
		{
			Name:  "replicated-job-1",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: *testingjobset.TestPodSpec.DeepCopy(),
			},
		},
		{
			Name:  "replicated-job-2",
			Count: 4,
			Template: corev1.PodTemplateSpec{
				Spec: *testingjobset.TestPodSpec.DeepCopy(),
			},
		},
	}

	result := ((*JobSet)(jobset)).PodSets()

	if diff := cmp.Diff(wantPodSets, result); diff != "" {
		t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
	}
}

func TestReclaimablePods(t *testing.T) {
	baseWrapper := testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
		testingjobset.ReplicatedJobRequirements{
			Name:        "replicated-job-1",
			Replicas:    1,
			Parallelism: 2,
			Completions: 2,
		},
		testingjobset.ReplicatedJobRequirements{
			Name:        "replicated-job-2",
			Replicas:    2,
			Parallelism: 3,
			Completions: 6,
		},
	)

	testcases := map[string]struct {
		jobSet *jobset.JobSet
		want   []kueue.ReclaimablePod
	}{
		"no status": {
			jobSet: baseWrapper.DeepCopy().Obj(),
			want:   nil,
		},
		"empty jobs status": {
			jobSet: baseWrapper.DeepCopy().JobsStatus().Obj(),
			want:   nil,
		},
		"single job done": {
			jobSet: baseWrapper.DeepCopy().JobsStatus(jobset.ReplicatedJobStatus{
				Name:      "replicated-job-1",
				Succeeded: 1,
			}).Obj(),
			want: []kueue.ReclaimablePod{{
				Name:  "replicated-job-1",
				Count: 2,
			}},
		},
		"single job partial done": {
			jobSet: baseWrapper.DeepCopy().JobsStatus(jobset.ReplicatedJobStatus{
				Name:      "replicated-job-2",
				Succeeded: 1,
			}).Obj(),
			want: []kueue.ReclaimablePod{{
				Name:  "replicated-job-2",
				Count: 3,
			}},
		},
		"all done": {
			jobSet: baseWrapper.DeepCopy().JobsStatus(
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-1",
					Succeeded: 1,
				},
				jobset.ReplicatedJobStatus{
					Name:      "replicated-job-2",
					Succeeded: 2,
				},
			).Obj(),
			want: []kueue.ReclaimablePod{
				{
					Name:  "replicated-job-1",
					Count: 2,
				},
				{
					Name:  "replicated-job-2",
					Count: 6,
				},
			},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			jobSet := (*JobSet)(tc.jobSet)
			got := jobSet.ReclaimablePods()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected Reclaimable pods (-want +got):\n%s", diff)
			}
		})
	}
}
