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
	batchv1 "k8s.io/api/batch/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
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

func TestPodSetsInfo(t *testing.T) {
	testcases := map[string]struct {
		job                  *batchv1.Job
		runInfo, restoreInfo []jobframework.PodSetInfo
		wantUnsuspended      *batchv1.Job
	}{
		"append": {
			job: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Obj(),
			runInfo: []jobframework.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"new-key": "new-val",
					},
				},
			},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				NodeSelector("new-key", "new-val").
				Suspend(false).
				Obj(),
			restoreInfo: []jobframework.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"orig-key": "orig-val",
					},
				},
			},
		},
		"update": {
			job: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Obj(),
			runInfo: []jobframework.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"orig-key": "new-val",
					},
				},
			},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "new-val").
				Suspend(false).
				Obj(),
			restoreInfo: []jobframework.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"orig-key": "orig-val",
					},
				},
			},
		},
		"parallelism": {
			job: utiltestingjob.MakeJob("job", "ns").
				Parallelism(5).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Obj(),
			runInfo: []jobframework.PodSetInfo{
				{
					Count: 2,
				},
			},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(2).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Suspend(false).
				Obj(),
			restoreInfo: []jobframework.PodSetInfo{
				{
					Count: 5,
				},
			},
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			origSpec := *tc.job.Spec.DeepCopy()
			job := Job{tc.job}

			job.RunWithPodSetsInfo(tc.runInfo)

			if diff := cmp.Diff(job.Spec, tc.wantUnsuspended.Spec); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
			job.RestorePodSetsInfo(tc.restoreInfo)
			job.Suspend()
			if diff := cmp.Diff(job.Spec, origSpec); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEquivalentToWorkload(t *testing.T) {
	baseJob := &Job{utiltestingjob.MakeJob("job", "ns").
		Parallelism(2).
		Obj()}
	baseJobPartialAdmission := &Job{utiltestingjob.MakeJob("job", "ns").
		SetAnnotation(JobMinParallelismAnnotation, "2").
		Parallelism(2).
		Obj()}
	podSets := []kueue.PodSet{
		*utiltesting.MakePodSet("main", 2).
			Containers(*baseJob.Spec.Template.Spec.Containers[0].DeepCopy()).
			Obj(),
	}
	basWorkload := utiltesting.MakeWorkload("wl", "ns").PodSets(podSets...).Obj()
	basWorkloadPartialAdmission := basWorkload.DeepCopy()
	basWorkloadPartialAdmission.Spec.PodSets[0].MinCount = pointer.Int32(2)
	cases := map[string]struct {
		wl         *kueue.Workload
		job        *Job
		wantResult bool
	}{
		"wrong podsets number": {
			job: baseJob,
			wl: func() *kueue.Workload {
				wl := basWorkload.DeepCopy()
				wl.Spec.PodSets = append(wl.Spec.PodSets, wl.Spec.PodSets...)
				return wl
			}(),
		},
		"different pods count": {
			job: baseJob,
			wl: func() *kueue.Workload {
				wl := basWorkload.DeepCopy()
				wl.Spec.PodSets[0].Count = 3
				return wl
			}(),
		},
		"different container": {
			job: baseJob,
			wl: func() *kueue.Workload {
				wl := basWorkload.DeepCopy()
				wl.Spec.PodSets[0].Template.Spec.Containers[0].Image = "other-image"
				return wl
			}(),
		},
		"equivalent": {
			job:        baseJob,
			wl:         basWorkload.DeepCopy(),
			wantResult: true,
		},
		"partial admission bad count (suspended)": {
			job: baseJobPartialAdmission,
			wl: func() *kueue.Workload {
				wl := basWorkloadPartialAdmission.DeepCopy()
				wl.Spec.PodSets[0].Count = 3
				return wl
			}(),
		},
		"partial admission different count (unsuspended)": {
			job: func() *Job {
				j := &Job{baseJobPartialAdmission.DeepCopy()}
				j.Spec.Suspend = pointer.Bool(false)
				return j
			}(),
			wl: func() *kueue.Workload {
				wl := basWorkloadPartialAdmission.DeepCopy()
				wl.Spec.PodSets[0].Count = 3
				return wl
			}(),
			wantResult: true,
		},
		"partial admission bad minCount": {
			job: baseJobPartialAdmission,
			wl: func() *kueue.Workload {
				wl := basWorkloadPartialAdmission.DeepCopy()
				wl.Spec.PodSets[0].MinCount = pointer.Int32(3)
				return wl
			}(),
		},
		"equivalent partial admission": {
			job:        baseJobPartialAdmission,
			wl:         basWorkloadPartialAdmission.DeepCopy(),
			wantResult: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.job.EquivalentToWorkload(*tc.wl) != tc.wantResult {
				t.Fatalf("Unexpected result, wanted: %v", tc.wantResult)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	podTemplate := utiltestingjob.MakeJob("job", "ns").Spec.Template.DeepCopy()
	cases := map[string]struct {
		job         *batchv1.Job
		wantPodSets []kueue.PodSet
	}{
		"no partial admission": {
			job: utiltestingjob.MakeJob("job", "ns").Parallelism(3).Obj(),
			wantPodSets: []kueue.PodSet{
				{
					Name:     "main",
					Template: *podTemplate.DeepCopy(),
					Count:    3,
				},
			},
		},
		"partial admission": {
			job: utiltestingjob.MakeJob("job", "ns").Parallelism(3).SetAnnotation(JobMinParallelismAnnotation, "2").Obj(),
			wantPodSets: []kueue.PodSet{
				{
					Name:     "main",
					Template: *podTemplate.DeepCopy(),
					Count:    3,
					MinCount: pointer.Int32(2),
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotPodSets := (&Job{tc.job}).PodSets()
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
