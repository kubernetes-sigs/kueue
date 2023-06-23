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
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/pointer"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	testingjobsetutil "sigs.k8s.io/jobset/pkg/util/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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

func TestEquivalentToWorkload(t *testing.T) {
	testContainer := corev1.Container{
		Name:  "ic1",
		Image: "image1",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			},
		},
	}

	job := (*JobSet)(testingjobsetutil.MakeJobSet("jobset", "ns").
		ReplicatedJob(testingjobsetutil.MakeReplicatedJob("replicated-job-1").
			Job(setParallelismAndCompletions(testingjobsetutil.MakeJobTemplate("test-job-1", "ns").PodSpec(corev1.PodSpec{
				InitContainers: []corev1.Container{testContainer, testContainer},
				Containers:     []corev1.Container{testContainer},
			}), 1, 1).Obj()).
			Replicas(1).Obj()).
		ReplicatedJob(testingjobsetutil.MakeReplicatedJob("replicated-job-2").
			Job(setParallelismAndCompletions(testingjobsetutil.MakeJobTemplate("test-job-2", "ns").PodSpec(corev1.PodSpec{
				InitContainers: []corev1.Container{testContainer},
				Containers:     []corev1.Container{testContainer, testContainer, testContainer},
			}), 2, 2).Obj()).
			Replicas(2).Obj()).Obj())

	podSets := []kueue.PodSet{
		*utiltesting.MakePodSet("single", 1).
			InitContainers(testContainer, testContainer).
			Containers(testContainer).
			Obj(),
		*utiltesting.MakePodSet("group", 4).
			InitContainers(testContainer).
			Containers(testContainer, testContainer, testContainer).
			Obj(),
	}
	baseWorkload := utiltesting.MakeWorkload("wl", "ns").PodSets(podSets...).Obj()
	cases := map[string]struct {
		wl         kueue.Workload
		wantResult bool
	}{
		"wrong podsets number": {
			wl: *(&utiltesting.WorkloadWrapper{Workload: *baseWorkload.DeepCopy()}).PodSets(podSets[:1]...).Obj(),
		},
		"bad single podSet count": {
			wl: func() kueue.Workload {
				wl := *baseWorkload.DeepCopy()
				wl.Spec.PodSets[0].Count = 3
				return wl
			}(),
		},
		"bad single podSet init container": {
			wl: func() kueue.Workload {
				wl := *baseWorkload.DeepCopy()
				wl.Spec.PodSets[0].Template.Spec.InitContainers[0].Image = "another-image"
				return wl
			}(),
		},
		"bad single podSet container": {
			wl: func() kueue.Workload {
				wl := *baseWorkload.DeepCopy()
				wl.Spec.PodSets[0].Template.Spec.Containers[0].Image = "another-image"
				return wl
			}(),
		},
		"bad group podSet count": {
			wl: func() kueue.Workload {
				wl := *baseWorkload.DeepCopy()
				wl.Spec.PodSets[1].Count = 3
				return wl
			}(),
		},
		"bad group podSet init container": {
			wl: func() kueue.Workload {
				wl := *baseWorkload.DeepCopy()
				wl.Spec.PodSets[1].Template.Spec.InitContainers[0].Image = "another-image"
				return wl
			}(),
		},
		"bad group podSet container": {
			wl: func() kueue.Workload {
				wl := *baseWorkload.DeepCopy()
				wl.Spec.PodSets[1].Template.Spec.Containers[0].Image = "another-image"
				return wl
			}(),
		},
		"equivalent": {
			wl:         *baseWorkload.DeepCopy(),
			wantResult: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if job.EquivalentToWorkload(tc.wl) != tc.wantResult {
				t.Fatalf("Unexpected result, wanted: %v", tc.wantResult)
			}
		})
	}
}

func setParallelismAndCompletions(wrapper *testingjobsetutil.JobTemplateWrapper, parallelism int32, completions int32) *testingjobsetutil.JobTemplateWrapper {
	wrapper.Spec.Parallelism = pointer.Int32(parallelism)
	wrapper.Spec.Completions = pointer.Int32(completions)
	return wrapper
}
