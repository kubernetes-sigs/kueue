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
	"k8s.io/utils/pointer"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	testingjobsetutil "sigs.k8s.io/jobset/pkg/util/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestPodsReady(t *testing.T) {
	testcases := map[string]struct {
		jobSet jobset.JobSet
		want   bool
	}{
		"all jobs are ready": {
			jobSet: jobset.JobSet{
				Spec: jobset.JobSetSpec{
					ReplicatedJobs: []jobset.ReplicatedJob{
						{
							Name:     "Test1",
							Replicas: 2,
						},
						{
							Name:     "Test2",
							Replicas: 3,
						},
					},
				},
				Status: jobset.JobSetStatus{
					Conditions: nil,
					Restarts:   0,
					ReplicatedJobsStatus: []jobset.ReplicatedJobStatus{
						{
							Name:      "Test",
							Ready:     1,
							Succeeded: 1,
						},
						{
							Name:      "Test",
							Ready:     3,
							Succeeded: 0,
						},
					},
				},
			},
			want: true,
		},
		"not all jobs are ready": {
			jobSet: jobset.JobSet{
				Spec: jobset.JobSetSpec{
					ReplicatedJobs: []jobset.ReplicatedJob{
						{
							Name:     "Test1",
							Replicas: 2,
						},
						{
							Name:     "Test2",
							Replicas: 3,
						},
					},
				},
				Status: jobset.JobSetStatus{
					Conditions: nil,
					Restarts:   0,
					ReplicatedJobsStatus: []jobset.ReplicatedJobStatus{
						{
							Name:      "Test",
							Ready:     1,
							Succeeded: 0,
						},
						{
							Name:      "Test",
							Ready:     1,
							Succeeded: 2,
						},
					},
				},
			},
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
	job := testingjobsetutil.MakeJobSet("jobset", "ns").
		ReplicatedJob(testingjobsetutil.MakeReplicatedJob("replicated-job-1").
			Job(setParallelismAndCompletions(testingjobsetutil.MakeJobTemplate("test-job-1", "ns"), 1, 1).Obj()).
			Replicas(1).
			Obj()).
		ReplicatedJob(testingjobsetutil.MakeReplicatedJob("replicated-job-2").
			Job(setParallelismAndCompletions(testingjobsetutil.MakeJobTemplate("test-job-2", "ns"), 2, 2).Obj()).
			Replicas(2).
			Obj()).Obj()

	wantPodSets := []kueue.PodSet{
		{
			Name:  "replicated-job-1",
			Count: 1,
		},
		{
			Name:  "replicated-job-2",
			Count: 4,
		},
	}

	result := ((*JobSet)(job)).PodSets()

	if diff := cmp.Diff(wantPodSets, result); diff != "" {
		t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
	}
}

func setParallelismAndCompletions(wrapper *testingjobsetutil.JobTemplateWrapper, parallelism int32, completions int32) *testingjobsetutil.JobTemplateWrapper {
	wrapper.Spec.Parallelism = pointer.Int32(parallelism)
	wrapper.Spec.Completions = pointer.Int32(completions)
	return wrapper
}
