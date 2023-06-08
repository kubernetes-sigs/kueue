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
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
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
					Parallelism: ptr.To[int32](3),
					Completions: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{},
			},
			want: false,
		},
		"parallelism = completions; not enough progress": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
					Completions: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{
					Ready:     ptr.To[int32](1),
					Succeeded: 1,
				},
			},
			want: false,
		},
		"parallelism = completions; all ready": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
					Completions: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{
					Ready:     ptr.To[int32](3),
					Succeeded: 0,
				},
			},
			want: true,
		},
		"parallelism = completions; some ready, some succeeded": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
					Completions: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{
					Ready:     ptr.To[int32](2),
					Succeeded: 1,
				},
			},
			want: true,
		},
		"parallelism = completions; all succeeded": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
					Completions: ptr.To[int32](3),
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
					Parallelism: ptr.To[int32](2),
					Completions: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{
					Ready: ptr.To[int32](2),
				},
			},
			want: true,
		},
		"parallelism > completions; reaching completions is enough": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
					Completions: ptr.To[int32](2),
				},
				Status: batchv1.JobStatus{
					Ready: ptr.To[int32](2),
				},
			},
			want: true,
		},
		"parallelism specified only; not enough progress": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{
					Ready: ptr.To[int32](2),
				},
			},
			want: false,
		},
		"parallelism specified only; all ready": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: ptr.To[int32](3),
				},
				Status: batchv1.JobStatus{
					Ready: ptr.To[int32](3),
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
		wantRunError         error
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
		"noInfoOnRun": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(5).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Obj()),
			runInfo: []jobframework.PodSetInfo{},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(5).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Suspend(false).
				Obj(),
			restoreInfo: []jobframework.PodSetInfo{
				{
					Count: 5,
				},
			},
			wantRunError: jobframework.ErrInvalidPodsetInfo,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			origSpec := *tc.job.Spec.DeepCopy()

			gotErr := tc.job.RunWithPodSetsInfo(tc.runInfo)

			if diff := cmp.Diff(tc.wantRunError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}

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
					Name:     kueue.DefaultPodSetName,
					Template: *podTemplate.DeepCopy(),
					Count:    3,
				},
			},
		},
		"partial admission": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").Parallelism(3).SetAnnotation(JobMinParallelismAnnotation, "2").Obj()),
			wantPodSets: []kueue.PodSet{
				{
					Name:     kueue.DefaultPodSetName,
					Template: *podTemplate.DeepCopy(),
					Count:    3,
					MinCount: ptr.To[int32](2),
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
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta", "ObjectMeta.OwnerReferences",
			"ObjectMeta.Name", "ObjectMeta.ResourceVersion",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
)

func TestReconciler(t *testing.T) {
	defer features.SetFeatureGateDuringTest(t, features.PartialAdmission, true)()

	baseJobWrapper := utiltestingjob.MakeJob("job", "ns").
		Suspend(true).
		Parallelism(10).
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	cases := map[string]struct {
		focus             bool
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
			job: *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
		},
		"non-matching admitted workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
		},
		"suspended job with partial admission and admitted workload is unsuspended": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Parallelism(8).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Obj(),
			},
		},
		"unsuspended job with partial admission and non-matching admitted workload is suspended and workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
		},
		"the workload is created when queue name is set": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Queue("test-queue").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				UID("test-uid").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
		},
		"the workload without uid label is created when job's uid is longer than 63 characters": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Queue("test-queue").
				UID(strings.Repeat("long-uid", 8)).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				UID(strings.Repeat("long-uid", 8)).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{}).
					Obj(),
			},
		},
		"the workload is not created when queue name is not set": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(false).
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
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				ParentWorkload("unit-test").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				ParentWorkload("unit-test").
				Obj(),
		},
		"non-standalone job is not suspended if its parent workload is admitted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				ParentWorkload("unit-test").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(false).
				ParentWorkload("unit-test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
		},
		"non-standalone job is suspended if its parent workload is found and not admitted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				ParentWorkload("parent-workload").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				ParentWorkload("unit-test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
		},
		"non-standalone job is not suspended if its parent workload is admitted and queue name is set": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				ParentWorkload("parent-workload").
				Queue("test-queue").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(false).
				ParentWorkload("parent-workload").
				Queue("test-queue").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Obj(),
			},
		},
		"checking a second non-matching workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Parallelism(5).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(false).
				Parallelism(5).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("first-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").Obj()).
					Obj(),
				*utiltesting.MakeWorkload("second-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("first-workload", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").Obj()).
					Obj(),
			},
			wantErr: jobframework.ErrExtraWorkloads,
		},
		"when workload is evicted, suspend, reset startTime, restore node affinity and remove the finalizer": {
			job: *baseJobWrapper.Clone().
				Queue("foo").
				Suspend(false).
				StartTime(time.Now()).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Queue("foo").
				Suspend(true).
				Active(10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"when workload is evicted but suspended, reset startTime and restore node affinity": {
			job: *baseJobWrapper.Clone().
				Queue("foo").
				Suspend(true).
				StartTime(time.Now()).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Queue("foo").
				Suspend(true).
				Active(10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"when workload is evicted, suspended and startTime is reset, restore node affinity": {
			job: *baseJobWrapper.Clone().
				Queue("foo").
				Suspend(true).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Queue("foo").
				Suspend(true).
				Active(10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Admit(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
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
			kcBuilder := clientBuilder.
				WithObjects(&tc.job)
			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}
			kClient := kcBuilder.Build()
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
