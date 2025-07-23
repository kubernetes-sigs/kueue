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

package job

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
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
		runInfo, restoreInfo []podset.PodSetInfo
		wantUnsuspended      *batchv1.Job
		wantRunError         error
	}{
		"append": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Toleration(corev1.Toleration{
					Key:      "orig-t-key",
					Operator: corev1.TolerationOpEqual,
					Value:    "orig-t-val",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj()),
			runInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"new-key": "new-val",
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "new-t-key",
							Operator: corev1.TolerationOpEqual,
							Value:    "new-t-val",
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				NodeSelector("new-key", "new-val").
				Toleration(corev1.Toleration{
					Key:      "orig-t-key",
					Operator: corev1.TolerationOpEqual,
					Value:    "orig-t-val",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Toleration(corev1.Toleration{
					Key:      "new-t-key",
					Operator: corev1.TolerationOpEqual,
					Value:    "new-t-val",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Suspend(false).
				Obj(),
			restoreInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"orig-key": "orig-val",
					},
					Tolerations: []corev1.Toleration{
						{
							Key:      "orig-t-key",
							Operator: corev1.TolerationOpEqual,
							Value:    "orig-t-val",
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				},
			},
		},
		"update": {
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Obj()),
			runInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"orig-key": "new-val",
					},
				},
			},
			wantRunError: podset.ErrInvalidPodSetUpdate,
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				NodeSelector("orig-key", "orig-val").
				Suspend(false).
				Obj(),
			restoreInfo: []podset.PodSetInfo{
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
			runInfo: []podset.PodSetInfo{
				{
					Count: 2,
				},
			},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(2).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Suspend(false).
				Obj(),
			restoreInfo: []podset.PodSetInfo{
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
			runInfo: []podset.PodSetInfo{},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(5).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Suspend(false).
				Obj(),
			restoreInfo: []podset.PodSetInfo{
				{
					Count: 5,
				},
			},
			wantRunError: podset.ErrInvalidPodsetInfo,
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
	jobTemplate := utiltestingjob.MakeJob("job", "ns")

	cases := map[string]struct {
		job                           *Job
		wantPodSets                   []kueue.PodSet
		enableTopologyAwareScheduling bool
	}{
		"no partial admission": {
			job: (*Job)(jobTemplate.Clone().Parallelism(3).Obj()),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(*jobTemplate.Clone().Spec.Template.Spec.DeepCopy()).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
		"partial admission": {
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					SetAnnotation(JobMinParallelismAnnotation, "2").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(*jobTemplate.Clone().Spec.Template.Spec.DeepCopy()).
					SetMinimumCount(2).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
		"with required topology annotation": {
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					RequiredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
					Obj(),
			},
			enableTopologyAwareScheduling: true,
		},
		"with preferred topology annotation": {
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					PreferredTopologyRequest("cloud.com/block").
					PodIndexLabel(ptr.To(batchv1.JobCompletionIndexAnnotation)).
					Obj(),
			},
			enableTopologyAwareScheduling: true,
		},
		"without preferred topology annotation if TAS is disabled": {
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
		"without required topology annotation if TAS is disabled": {
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},
			enableTopologyAwareScheduling: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			gotPodSets, err := tc.job.PodSets()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(batchv1.Job{}, "TypeMeta", "ObjectMeta.OwnerReferences", "ObjectMeta.ResourceVersion", "ObjectMeta.Annotations"),
	}
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b kueue.Workload) bool {
			return a.Name < b.Name
		}),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool {
			return a.Type < b.Type
		}),
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta", "ObjectMeta.OwnerReferences",
			"ObjectMeta.Name", "ObjectMeta.ResourceVersion",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
	}
	workloadCmpOptsWithOwner = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b kueue.Workload) bool {
			return a.Name < b.Name
		}),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool {
			return a.Type < b.Type
		}),
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta", "ObjectMeta.Name", "ObjectMeta.ResourceVersion",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
	}
)

func TestReconciler(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	testStartTime := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(testStartTime)

	t.Cleanup(jobframework.EnableIntegrationsForTest(t, FrameworkName))
	baseJobWrapper := utiltestingjob.MakeJob("job", "ns").
		Suspend(true).
		Queue("foo").
		Parallelism(10).
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	baseWorkloadWrapper := utiltesting.MakeWorkload("wl", "ns").
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj())

	baseWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)

	highWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc-high").
		PriorityValue(200)

	basePCWrapper := utiltesting.MakePriorityClass("test-pc").
		PriorityValue(200)

	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()

	baseWaitForPodsReadyConf := &configapi.WaitForPodsReady{Enable: true}

	cases := map[string]struct {
		enableObjectRetentionPolicies                     bool
		enableTopologyAwareScheduling                     bool
		enableManagedJobsNamespaceSelectorAlwaysRespected bool

		reconcilerOptions []jobframework.Option
		job               batchv1.Job
		workloads         []kueue.Workload
		otherJobs         []batchv1.Job
		priorityClasses   []client.Object
		wantJob           batchv1.Job
		wantWorkloads     []kueue.Workload
		wantEvents        []utiltesting.EventRecord
		wantErr           error
	}{
		"PodsReady is set to False before Workload is Admitted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(false).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(false).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
		},
		"PodsReady is set to False after Workload is Admitted but not all Pods reached readiness": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
		},
		"PodsReady is set to False after Workload is Admitted, some Pods became ready but not all Pods reached readiness": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Ready(9).
				Active(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				Ready(9).
				Active(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
		},
		"PodsReady is set to True after Workload is Admitted and all Pods reached readiness": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
		},
		"PodsReady is set to True after Workload is Admitted and all Pods reached readiness without previous PodsReady condition": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
		},
		"PodsReady is set to False after Workload is running and one pod failed": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Ready(9).
				Failed(1).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(9).
				Failed(1).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForRecovery,
					Message: "At least one pod has failed, waiting for recovery",
				}).
				Obj(),
			},
		},
		"PodsReady continues to be False after a pod failed and workload is still recovering": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Ready(9).
				Failed(1).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				Ready(9).
				Failed(1).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForRecovery,
					Message: "At least one pod has failed, waiting for recovery",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForRecovery,
					Message: "At least one pod has failed, waiting for recovery",
				}).
				Obj(),
			},
		},
		"PodsReady is set to True after failing pod recovered": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForRecovery,
					Message: "At least one pod has failed, waiting for recovery",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRecovered,
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
		},
		"PodsReady=False has the new Reason if there was the old one before (pre v0.11.0)": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
		},
		"PodsReady=True has the new Reason if there was the old one before (pre v0.11.0)": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  "PodsReady",
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
		},
		"PodsReady is set to False if there's an invalid Reason (pre v0.11.0)": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "InvalidReason",
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
		},
		"PodSet label and Workload annotation are set when Job is starting; TopologyAwareScheduling enabled": {
			enableTopologyAwareScheduling: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodAnnotation(kueuealpha.WorkloadAnnotation, "wl").
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"when workload is created, it has its owner ProvReq annotations": {
			job: *baseJobWrapper.Clone().
				SetAnnotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
				SetAnnotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
				SetAnnotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
				UID("test-uid").
				Suspend(true).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Annotations(map[string]string{controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					Labels(map[string]string{controllerconsts.JobUIDLabel: "test-uid"}).
					Obj(),
			},

			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID("test-uid")),
				},
			},
		},
		"when workload is created, it has correct labels set": {
			job: *baseJobWrapper.Clone().
				Label("toCopyKey", "toCopyValue").
				Label("dontCopyKey", "dontCopyValue").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Label("toCopyKey", "toCopyValue").
				Label("dontCopyKey", "dontCopyValue").
				UID("test-uid").
				Suspend(true).
				Obj(),
			reconcilerOptions: []jobframework.Option{
				jobframework.WithLabelKeysToCopy([]string{"toCopyKey", "redundantToCopyKey"}),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
						"toCopyKey":                  "toCopyValue"}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID("test-uid")),
				},
			},
		},
		"when workload is admitted the PodSetUpdates are propagated to job": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel("ac-key", "ac-value").
				PodLabel(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"when workload is evicted due to spec.active field being false, job gets suspended and quota is unset": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The workload is deactivated",
				},
			},
		},
		"when workload is active after deactivation; objectRetentionPolicies.workloads.afterDeactivatedByKueue=0; should not delete the job": {
			enableObjectRetentionPolicies: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 0},
					},
				}),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Active(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(testStartTime),
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Active(true).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The workload is deactivated",
				},
			},
		},
		"when workload is manually deactivated; objectRetentionPolicies.workloads.afterDeactivatedByKueue=0; should not delete the job": {
			enableObjectRetentionPolicies: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 0},
					},
				}),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadDeactivated,
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(testStartTime),
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadDeactivated,
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The workload is deactivated",
				},
			},
		},
		"when workload is deactivated by kueue; objectRetentionPolicies.workloads.afterDeactivatedByKueue=0; should delete the job": {
			enableObjectRetentionPolicies: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 0},
					},
				}),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(testStartTime),
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The workload is deactivated",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Deleted",
					Message:   "Deleted job: deactivation retention period expired",
				},
			},
		},
		"when workload is deactivated by kueue; objectRetentionPolicies.workloads.afterDeactivatedByKueue=60; retention period has not expired": {
			enableObjectRetentionPolicies: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 2 * time.Minute},
					},
				}),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-2*time.Minute)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(testStartTime.Add(-time.Minute)),
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(120).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The workload is deactivated",
				},
			},
		},
		"when workload is deactivated by kueue; objectRetentionPolicies.workloads.afterDeactivatedByKueue=60; retention period has expired": {
			enableObjectRetentionPolicies: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: time.Minute},
					},
				}),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-2*time.Minute)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(testStartTime.Add(-time.Minute)),
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(120).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  fmt.Sprintf("%sDueTo%s", kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The workload is deactivated",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Deleted",
					Message:   "Deleted job: deactivation retention period expired",
				},
			},
		},
		"when workload is evicted due to pods ready timeout, job gets suspended and quota is unset": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: "Exceeded the PodsReady timeout",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "Exceeded the PodsReady timeout",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: "Exceeded the PodsReady timeout",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: "Exceeded the PodsReady timeout",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Exceeded the PodsReady timeout",
				},
			},
		},
		"when workload is evicted due to admission check, job gets suspended": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByAdmissionCheck,
						Message: "At least one admission check is false",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "At least one admission check is false",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadEvictedByAdmissionCheck,
						Message: "At least one admission check is false",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByAdmissionCheck,
						Message: "At least one admission check is false",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "At least one admission check is false",
				},
			},
		},
		"when workload is evicted due to cluster queue stopped, job gets suspended": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
						Message: "The ClusterQueue is stopped",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The ClusterQueue is stopped",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
						Message: "The ClusterQueue is stopped",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
						Message: "The ClusterQueue is stopped",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The ClusterQueue is stopped",
				},
			},
		},
		"when workload is evicted due to local queue stopped, job gets suspended": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
						Message: "The LocalQueue is stopped",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The LocalQueue is stopped",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
						Message: "The LocalQueue is stopped",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
						Message: "The LocalQueue is stopped",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "The LocalQueue is stopped",
				},
			},
		},
		"when workload is evicted due to preemption, job gets suspended": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Preempted",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "Preempted",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Preempted",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Preempted",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Preempted",
				},
			},
		},
		"when job is initially suspended, the Workload has active=false and it's not admitted, " +
			"it should not get an evicted condition, but the job should remain suspended": {
			job: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Active(false).
					Queue("foo").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Active(false).
					Queue("foo").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
		},
		"when workload is admitted and PodSetUpdates conflict between admission checks on labels, the workload is finished with failure": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value1",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value1",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "FailedToStart",
						Message: `in admission check "check2": invalid admission check PodSetUpdate: conflict for labels: conflict for key=ac-key, value1=ac-value1, value2=ac-value2`,
					}).
					Obj(),
			},
		},
		"when workload is admitted and PodSetUpdates conflict between admission checks on annotations, the workload is finished with failure": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Annotations: map[string]string{
									"ac-key": "ac-value1",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Annotations: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Annotations: map[string]string{
									"ac-key": "ac-value1",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Annotations: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "FailedToStart",
						Message: `in admission check "check2": invalid admission check PodSetUpdate: conflict for annotations: conflict for key=ac-key, value1=ac-value1, value2=ac-value2`,
					}).
					Obj(),
			},
		},
		"when workload is admitted and PodSetUpdates conflict between admission checks on nodeSelector, the workload is finished with failure": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								NodeSelector: map[string]string{
									"ac-key": "ac-value1",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								NodeSelector: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								NodeSelector: map[string]string{
									"ac-key": "ac-value1",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								NodeSelector: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "FailedToStart",
						Message: `in admission check "check2": invalid admission check PodSetUpdate: conflict for nodeSelector: conflict for key=ac-key, value1=ac-value1, value2=ac-value2`,
					}).
					Obj(),
			},
		},
		"when workload is admitted and PodSetUpdates conflict between admission check nodeSelector and current node selector, the workload is finished with failure": {
			job: *baseJobWrapper.Clone().
				NodeSelector("provisioning", "spot").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				NodeSelector("provisioning", "spot").
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								NodeSelector: map[string]string{
									"provisioning": "on-demand",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								NodeSelector: map[string]string{
									"provisioning": "on-demand",
								},
							},
						},
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "FailedToStart",
						Message: `invalid admission check PodSetUpdate: conflict for nodeSelector: conflict for key=provisioning, value1=spot, value2=on-demand`,
					}).
					Obj(),
			},
		},
		"when workload is admitted the PodSetUpdates values matching for key": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodAnnotation("annotation-key1", "common-value").
				PodAnnotation("annotation-key2", "only-in-check1").
				PodLabel("label-key1", "common-value").
				PodLabel(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
				NodeSelector("node-selector-key1", "common-value").
				NodeSelector("node-selector-key2", "only-in-check2").
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"label-key1": "common-value",
								},
								Annotations: map[string]string{
									"annotation-key1": "common-value",
									"annotation-key2": "only-in-check1",
								},
								NodeSelector: map[string]string{
									"node-selector-key1": "common-value",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"label-key1": "common-value",
								},
								Annotations: map[string]string{
									"annotation-key1": "common-value",
								},
								NodeSelector: map[string]string{
									"node-selector-key1": "common-value",
									"node-selector-key2": "only-in-check2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"label-key1": "common-value",
								},
								Annotations: map[string]string{
									"annotation-key1": "common-value",
									"annotation-key2": "only-in-check1",
								},
								NodeSelector: map[string]string{
									"node-selector-key1": "common-value",
								},
							},
						},
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check2",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: kueue.DefaultPodSetName,
								Labels: map[string]string{
									"label-key1": "common-value",
								},
								Annotations: map[string]string{
									"annotation-key1": "common-value",
								},
								NodeSelector: map[string]string{
									"node-selector-key1": "common-value",
									"node-selector-key2": "only-in-check2",
								},
							},
						},
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"suspended job with matching admitted workload is unsuspended": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"non-matching admitted workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Admitted(true).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/wl",
				},
			},
		},
		"non-matching non-admitted workload is updated": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated not matching Workload for suspended job: ns/a",
				},
			},
		},
		"suspended job with partial admission and admitted workload is unsuspended": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Parallelism(8).
				PodLabel(controllerconsts.PodSetLabel, string(kueue.DefaultPodSetName)).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Admitted(true).
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
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Admitted(true).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"unsuspended job with partial admission and non-matching admitted workload is suspended and workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
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
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Admitted(true).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "No matching Workload; restoring pod templates according to existent Workload",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/a",
				},
			},
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
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Missing Workload; unable to restore pod templates",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID("test-uid")),
				},
			},
		},
		"the workload is updated when queue name has changed for suspended job": {
			job: *baseJobWrapper.
				Clone().
				Suspend(true).
				Queue("test-queue-new").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue-new").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue-new").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
		},
		"the workload is updated when priority class has changed for suspended job": {
			job: *baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), highWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(baseWPCWrapper.Value).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					PriorityClass(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(highWPCWrapper.Value).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					PriorityClass(highWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated not matching Workload for suspended job: ns/job",
				},
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
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Missing Workload; unable to restore pod templates",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID(strings.Repeat("long-uid", 8))),
				},
			},
		},
		"the workload is not created when queue name is not set": {
			job: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
		},
		"non-standalone job is suspended if its parent workload is not found": {
			job: *baseJobWrapper.
				Clone().
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			otherJobs: []batchv1.Job{
				*utiltestingjob.MakeJob("parent", "ns").
					UID("parent").
					Queue("queue").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Suspended",
					Message:   "Kueue managed child job suspended",
				},
			},
		},
		"non-standalone job is not suspended if its parent workload is admitted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: *baseJobWrapper.
				Clone().
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Suspend(false).
				Obj(),
			otherJobs: []batchv1.Job{
				*utiltestingjob.MakeJob("parent", "ns").
					Queue("queue").
					UID("parent-uid").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent-uid").
					Obj(),
			},
		},
		"non-standalone job is suspended if its parent workload is found and not admitted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: *baseJobWrapper.
				Clone().
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			otherJobs: []batchv1.Job{
				*utiltestingjob.MakeJob("parent", "ns").
					Queue("queue").
					UID("parent-uid").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent-uid").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Suspended",
					Message:   "Kueue managed child job suspended",
				},
			},
		},
		"non-standalone job is not suspended if its parent workload is admitted and queue name is set": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Queue("test-queue").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(false).
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("Job")).
				Queue("test-queue").
				Obj(),
			otherJobs: []batchv1.Job{
				*utiltestingjob.MakeJob("parent", "ns").
					UID("parent-uid").
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent-uid").
					Admitted(true).
					Obj(),
			},
		},
		"checking a second non-matching workload is deleted": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
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
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("second-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("first-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
					Admitted(true).
					Obj(),
			},
			wantErr: jobframework.ErrExtraWorkloads,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/second-workload",
				},
			},
		},
		"when workload is evicted, suspend, reset startTime and restore node affinity": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				StartTime(time.Now()).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Active(10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
				},
			},
		},
		"when workload is evicted but suspended, reset startTime and restore node affinity": {
			job: *baseJobWrapper.Clone().
				Suspend(true).
				StartTime(time.Now()).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Active(10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"when workload is evicted, suspended and startTime is reset, restore node affinity": {
			job: *baseJobWrapper.Clone().
				Suspend(true).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Active(10).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"when job completes, workload is marked as finished": {
			job: *baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{
					Type:    batchv1.JobComplete,
					Status:  corev1.ConditionTrue,
					Message: "Job finished successfully",
				}).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Generation(1).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{
					Type:    batchv1.JobComplete,
					Status:  corev1.ConditionTrue,
					Message: "Job finished successfully",
				}).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadFinished,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadFinishedReasonSucceeded,
						Message:            "Job finished successfully",
						ObservedGeneration: 1,
					}).
					Generation(1).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/wl' is declared finished",
				},
			},
		},
		"when the workload is finished, its finalizer is removed": {
			job: *baseJobWrapper.Clone().Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/a' is declared finished",
				},
			},
		},
		"the workload is created when queue name is set, with workloadPriorityClass": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Queue("test-queue").
				UID("test-uid").
				WorkloadPriorityClass("test-wpc").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				UID("test-uid").
				WorkloadPriorityClass("test-wpc").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Missing Workload; unable to restore pod templates",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID("test-uid")),
				},
			},
		},
		"the workload is created when queue name is set, with PriorityClass": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Queue("test-queue").
				UID("test-uid").
				PriorityClass("test-pc").
				Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(),
			},
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				UID("test-uid").
				PriorityClass("test-pc").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-pc").
					Priority(200).
					PriorityClassSource(constants.PodPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Missing Workload; unable to restore pod templates",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID("test-uid")),
				},
			},
		},
		"the workload is created when queue name is set, with workloadPriorityClass and PriorityClass": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Queue("test-queue").
				UID("test-uid").
				WorkloadPriorityClass("test-wpc").
				PriorityClass("test-pc").
				Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(), baseWPCWrapper.Obj(),
			},
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				UID("test-uid").
				WorkloadPriorityClass("test-wpc").
				PriorityClass("test-pc").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Missing Workload; unable to restore pod templates",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, types.UID("test-uid")),
				},
			},
		},
		"the workload shouldn't be recreated for the completed job": {
			job: *baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
				Obj(),
			workloads: []kueue.Workload{},
			wantJob: *baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
				Obj(),
			wantWorkloads: []kueue.Workload{},
		},
		"when the prebuilt workload is missing, no new one is created, the job is suspended and prebuilt workload not found error is returned": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Label(controllerconsts.PrebuiltWorkloadLabel, "missing-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Label(controllerconsts.PrebuiltWorkloadLabel, "missing-workload").
				UID("test-uid").
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "missing workload",
				},
			},
			wantErr: jobframework.ErrPrebuiltWorkloadNotFound,
		},
		"when the prebuilt workload exists its owner info is updated": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Label(controllerconsts.PrebuiltWorkloadLabel, "prebuilt-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Label(controllerconsts.PrebuiltWorkloadLabel, "prebuilt-workload").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Not admitted by cluster queue",
				},
			},
		},
		"when the prebuilt workload is owned by another object": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Label(controllerconsts.PrebuiltWorkloadLabel, "prebuilt-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Label(controllerconsts.PrebuiltWorkloadLabel, "prebuilt-workload").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "other-job", "other-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "other-job", "other-uid").
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "missing workload",
				},
			},
			wantErr: jobframework.ErrPrebuiltWorkloadNotFound,
		},
		"when the prebuilt workload is not equivalent to the job": {
			job: *baseJobWrapper.
				Clone().
				Suspend(false).
				Label(controllerconsts.PrebuiltWorkloadLabel, "prebuilt-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Label(controllerconsts.PrebuiltWorkloadLabel, "prebuilt-workload").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "OutOfSync",
						Message: "The prebuilt workload is out of sync with its user job",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "missing workload",
				},
			},
			wantErr: jobframework.ErrPrebuiltWorkloadNotFound,
		},
		"the workload is not admitted, tolerations and node selector change": {
			job: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).NodeSelector("node-label", "value").
				Obj(),
			wantJob: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).NodeSelector("node-label", "value").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							Toleration(corev1.Toleration{
								Key:      "tolerationkey1",
								Operator: corev1.TolerationOpExists,
								Effect:   corev1.TaintEffectNoSchedule,
							}).
							NodeSelector(map[string]string{"different node-label": "different value"}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "",
					}).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							Toleration(corev1.Toleration{
								Key:      "tolerationkey2",
								Operator: corev1.TolerationOpExists,
								Effect:   corev1.TaintEffectNoSchedule,
							}).
							NodeSelector(map[string]string{"node-label": "value"}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "",
					}).
					Priority(0).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated not matching Workload for suspended job: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()),
				},
			},
		},
		"the workload is admitted, tolerations and node selector change": {
			job: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).NodeSelector("node-label", "value").
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).NodeSelector("node-label", "value").
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							Toleration(corev1.Toleration{
								Key:      "tolerationkey1",
								Operator: corev1.TolerationOpExists,
								Effect:   corev1.TaintEffectNoSchedule,
							}).
							NodeSelector(map[string]string{"different node-label": "different value"}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "",
					}).
					Priority(0).
					ReserveQuota(utiltesting.MakeAdmission("cq").PodSets(kueue.PodSetAssignment{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: ptr.To[int32](10),
					}).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							Toleration(corev1.Toleration{
								Key:      "tolerationkey1",
								Operator: corev1.TolerationOpExists,
								Effect:   corev1.TaintEffectNoSchedule,
							}).
							NodeSelector(map[string]string{"different node-label": "different value"}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "",
					}).
					Priority(0).
					ReserveQuota(utiltesting.MakeAdmission("cq").PodSets(kueue.PodSetAssignment{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: ptr.To[int32](10),
					}).Obj()).
					Admitted(true).
					Obj(),
			},
		},
		"the workload is admitted, job still suspended and tolerations change": {
			job: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
							Toleration(corev1.Toleration{
								Key:      "tolerationkey1",
								Operator: corev1.TolerationOpExists,
								Effect:   corev1.TaintEffectNoSchedule,
							}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "",
					}).
					Priority(0).
					ReserveQuota(utiltesting.MakeAdmission("cq").PodSets(kueue.PodSetAssignment{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: ptr.To[int32](10),
					}).Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()),
				},
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
		},
		"admission check message is emitted as event for job": {
			job: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(false).
					Active(false).
					Queue("foo").
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
						Reason: "Reason",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName",
						State:   kueue.CheckStatePending,
						Message: "Not admitted, ETA: 2024-02-22T10:36:40Z.",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(false).
					Active(false).
					Queue("foo").
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
						Reason: "Reason",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName",
						State:   kueue.CheckStatePending,
						Message: "Not admitted, ETA: 2024-02-22T10:36:40Z.",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    jobframework.ReasonUpdatedAdmissionCheck,
					Message:   "acName: Not admitted, ETA: 2024-02-22T10:36:40Z.",
				},
			},
		},
		"multiple admission check messages are emitted as a single event for job": {
			job: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(false).
					Active(false).
					Queue("foo").
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
						Reason: "Reason",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName1",
						State:   kueue.CheckStatePending,
						Message: "Some message.",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName2",
						State:   kueue.CheckStatePending,
						Message: "Another message.",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					Admitted(false).
					Active(false).
					Queue("foo").
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
						Reason: "Reason",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName1",
						State:   kueue.CheckStatePending,
						Message: "Some message.",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName2",
						State:   kueue.CheckStatePending,
						Message: "Another message.",
					}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    jobframework.ReasonUpdatedAdmissionCheck,
					Message:   "acName1: Some message.; acName2: Another message.",
				},
			},
		},
		"the maximum execution time is passed to the created workload": {
			job: *baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					MaximumExecutionTimeSeconds(10).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					Labels(map[string]string{controllerconsts.JobUIDLabel: string(baseJobWrapper.GetUID())}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()),
				},
			},
		},
		"the maximum execution time is updated in the workload": {
			job: *baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					MaximumExecutionTimeSeconds(5).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					Labels(map[string]string{controllerconsts.JobUIDLabel: string(baseJobWrapper.GetUID())}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").
					MaximumExecutionTimeSeconds(10).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					Labels(map[string]string{controllerconsts.JobUIDLabel: string(baseJobWrapper.GetUID())}).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated not matching Workload for suspended job: ns/job",
				},
			},
		},
		"job with queue name is not reconciled in unlabelled namespace when AlwaysRespected is enabled": {
			enableManagedJobsNamespaceSelectorAlwaysRespected: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManagedJobsNamespaceSelector(labels.SelectorFromSet(map[string]string{
					"managed-by-kueue": "true",
				})),
			},
			job: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Queue("test-queue").
				Suspend(false).
				Obj(),
			wantWorkloads: nil,
		},
		"job with queue name is reconciled in labelled namespace when AlwaysRespected is enabled": {
			enableManagedJobsNamespaceSelectorAlwaysRespected: true,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManagedJobsNamespaceSelector(labels.SelectorFromSet(map[string]string{
					"managed-by-kueue": "true",
				})),
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: *utiltestingjob.MakeJob("job", "labelled-ns").
				Queue("test-queue").
				Suspend(true).
				UID("test-uid").
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "labelled-ns").
				Queue("test-queue").
				UID("test-uid").
				Suspend(true).
				Parallelism(10).
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "labelled-ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "labelled-ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: labelled-ns/" + GetWorkloadNameForJob("job", "test-uid"),
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			features.SetFeatureGateDuringTest(t, features.ObjectRetentionPolicies, tc.enableObjectRetentionPolicies)
			features.SetFeatureGateDuringTest(t, features.ManagedJobsNamespaceSelectorAlwaysRespected, tc.enableManagedJobsNamespaceSelectorAlwaysRespected)

			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}

			labelledNamespace := utiltesting.MakeNamespaceWrapper("labelled-ns").
				Label("managed-by-kueue", "true").
				Obj()

			objs := append(tc.priorityClasses, &tc.job, utiltesting.MakeResourceFlavor("default").Obj(), testNamespace, labelledNamespace)
			kcBuilder := clientBuilder.
				WithObjects(objs...)

			if len(tc.otherJobs) > 0 {
				kcBuilder = kcBuilder.WithLists(&batchv1.JobList{Items: tc.otherJobs})
			}

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			// For prebuilt workloads we are skipping the ownership setup in the test body and
			// expect the reconciler to do it.
			_, useesPrebuiltWorkload := tc.job.Labels[controllerconsts.PrebuiltWorkloadLabel]

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				controller := metav1.GetControllerOfNoCopy(&tc.workloads[i])
				if !useesPrebuiltWorkload && controller == nil {
					if err := ctrl.SetControllerReference(&tc.job, &tc.workloads[i], kClient.Scheme()); err != nil {
						t.Fatalf("Could not setup owner reference in Workloads: %v", err)
					}
				}
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}
			recorder := &utiltesting.EventRecorder{}
			reconciler := NewReconciler(kClient, recorder, append(tc.reconcilerOptions, jobframework.WithClock(t, fakeClock))...)

			jobKey := client.ObjectKeyFromObject(&tc.job)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: jobKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotJob batchv1.Job
			if err := kClient.Get(ctx, jobKey, &gotJob); client.IgnoreNotFound(err) != nil {
				t.Fatalf("Could not get Job after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantJob, gotJob, jobCmpOpts...); diff != "" {
				t.Errorf("Job after reconcile (-want,+got):\n%s", diff)
			}
			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}

			wlCheckOpts := workloadCmpOpts
			if useesPrebuiltWorkload {
				wlCheckOpts = workloadCmpOptsWithOwner
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, wlCheckOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
