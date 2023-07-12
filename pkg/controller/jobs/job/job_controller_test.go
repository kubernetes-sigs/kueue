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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestPodsReady(t *testing.T) {
	testcases := map[string]struct {
		job  Job
		want bool
	}{
		"parallelism = completions; no progress": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: pointer.Int32(3),
					Completions: pointer.Int32(3),
				},
				Status: batchv1.JobStatus{},
			},
			want: false,
		},
		"parallelism = completions; not enough progress": {
			job: Job{
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
			job: Job{
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
			job: Job{
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
			job: Job{
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
			job: Job{
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
			job: Job{
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
			job: Job{
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
			job: Job{
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
			got := tc.job.PodsReady()
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}

func TestPodSetsInfo(t *testing.T) {
	testcases := map[string]struct {
		job                  *Job
		runInfo, restoreInfo []jobframework.PodSetInfo
		wantUnsuspended      *batchv1.Job
	}{
		"append": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Obj()),
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
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Obj()),
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
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(5).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Obj()),
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

			tc.job.RunWithPodSetsInfo(tc.runInfo)

			if diff := cmp.Diff(tc.job.Spec, tc.wantUnsuspended.Spec); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
			tc.job.RestorePodSetsInfo(tc.restoreInfo)
			tc.job.Suspend()
			if diff := cmp.Diff(tc.job.Spec, origSpec); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	podTemplate := utiltestingjob.MakeJob("job", "ns").Spec.Template.DeepCopy()
	cases := map[string]struct {
		job         *Job
		wantPodSets []kueue.PodSet
	}{
		"no partial admission": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").Parallelism(3).Obj()),
			wantPodSets: []kueue.PodSet{
				{
					Name:     "main",
					Template: *podTemplate.DeepCopy(),
					Count:    3,
				},
			},
		},
		"partial admission": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").Parallelism(3).SetAnnotation(JobMinParallelismAnnotation, "2").Obj()),
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
			gotPodSets := tc.job.PodSets()
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(batchv1.Job{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b kueue.Workload) bool {
			return a.Name < b.Name
		}),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta", "ObjectMeta"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
)

func TestReconciler(t *testing.T) {
	defer features.SetFeatureGateDuringTest(t, features.PartialAdmission, true)()
	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               batchv1.Job
		wantJob           batchv1.Job
		workloads         []kueue.Workload
		wantWorkloads     []kueue.Workload
		wantErr           error
	}{
		"suspended job with matching admitted workload is unsuspended": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(true).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
		},
		"non-matching admitted workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(true).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(true).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
		},
		"suspended job with partial admission and admitted workload is unsuspended": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *utiltestingjob.MakeJob("job", "ns").
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(true).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Obj(),
			},
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Parallelism(8).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Obj(),
			},
		},
		"unsuspended job with partial admission and non-matching admitted workload is suspended and workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *utiltestingjob.MakeJob("job", "ns").
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(true).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
		},
		"the workload is created when queue name is set": {
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Image("", nil).
				Queue("test-queue").
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(true).
				Image("", nil).
				Queue("test-queue").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
					Queue("test-queue").
					Priority(0).
					Obj(),
			},
		},
		"the workload is not created when queue name is not set": {
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Parallelism(1).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Parallelism(1).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
		},
		"should get error if child job owner not found": {
			job: *utiltestingjob.MakeJob("job", "ns").
				ParentWorkload("non-existing-parent-workload").
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").Obj(),
			wantErr: jobframework.ErrChildJobOwnerNotFound,
		},
		"should get error if workload owner is unknown": {
			job: *utiltestingjob.MakeJob("job", "ns").
				ParentWorkload("non-existing-parent-workload").
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").Obj(),
			wantErr: jobframework.ErrUnknownWorkloadOwner,
		},
		"non-standalone job is suspended if its parent workload is not found": {
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				ParentWorkload("unit-test").
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(true).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				ParentWorkload("unit-test").
				Obj(),
		},
		"non-standalone job is suspended if its parent workload is admitted": {
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				ParentWorkload("unit-test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(true).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				ParentWorkload("unit-test").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			kClient := clientBuilder.
				WithObjects(&tc.job).
				Build()
			for i := range tc.workloads {
				if err := ctrl.SetControllerReference(&tc.job, &tc.workloads[i], kClient.Scheme()); err != nil {
					t.Fatalf("Could not setup owner reference in Workloads: %v", err)
				}
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder, tc.reconcilerOptions...)

			jobKey := client.ObjectKeyFromObject(&tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotJob batchv1.Job
			if err := kClient.Get(ctx, jobKey, &gotJob); err != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, gotJob, jobCmpOpts...); diff != "" {
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
