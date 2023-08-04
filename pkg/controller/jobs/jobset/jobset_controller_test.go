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
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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

var (
	jobCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(jobset.JobSet{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta", "ObjectMeta"),
		cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "Priority"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestReconciler(t *testing.T) {
	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               *jobset.JobSet
		wantJob           *jobset.JobSet
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"workload is created with podsets": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    2,
					Completions: 2,
					Parallelism: 2,
				},
			).Obj(),
			wantJob: testingjobset.MakeJobSet("jobset", "ns").ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Completions: 1,
					Parallelism: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    2,
					Completions: 2,
					Parallelism: 2,
				},
			).Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("jobset", "ns").
					PodSets(
						*utiltesting.MakePodSet("replicated-job-1", 1).Obj(),
						*utiltesting.MakePodSet("replicated-job-2", 4).Obj(),
					).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(jobset.AddToScheme)
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			kClient := clientBuilder.WithObjects(tc.job).Build()
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotJobSet jobset.JobSet
			if err := kClient.Get(ctx, jobKey, &gotJobSet); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, &gotJobSet, jobCmpOpts...); diff != "" {
				t.Errorf("Job after reconcile (-want,+got):\n%s", diff)
			}
			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, workloadCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}

}
