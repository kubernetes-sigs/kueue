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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestPodsReady(t *testing.T) {
	testcases := map[string]struct {
		job  Job
		want bool
	}{
		"parallelism = completions; no progress": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
					Completions: new(int32(3)),
				},
				Status: batchv1.JobStatus{},
			},
			want: false,
		},
		"parallelism = completions; not enough progress": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
					Completions: new(int32(3)),
				},
				Status: batchv1.JobStatus{
					Ready:     new(int32(1)),
					Succeeded: 1,
				},
			},
			want: false,
		},
		"parallelism = completions; all ready": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
					Completions: new(int32(3)),
				},
				Status: batchv1.JobStatus{
					Ready:     new(int32(3)),
					Succeeded: 0,
				},
			},
			want: true,
		},
		"parallelism = completions; some ready, some succeeded": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
					Completions: new(int32(3)),
				},
				Status: batchv1.JobStatus{
					Ready:     new(int32(2)),
					Succeeded: 1,
				},
			},
			want: true,
		},
		"parallelism = completions; all succeeded": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
					Completions: new(int32(3)),
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
					Parallelism: new(int32(2)),
					Completions: new(int32(3)),
				},
				Status: batchv1.JobStatus{
					Ready: new(int32(2)),
				},
			},
			want: true,
		},
		"parallelism > completions; reaching completions is enough": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
					Completions: new(int32(2)),
				},
				Status: batchv1.JobStatus{
					Ready: new(int32(2)),
				},
			},
			want: true,
		},
		"parallelism specified only; not enough progress": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
				},
				Status: batchv1.JobStatus{
					Ready: new(int32(2)),
				},
			},
			want: false,
		},
		"parallelism specified only; all ready": {
			job: Job{
				Spec: batchv1.JobSpec{
					Parallelism: new(int32(3)),
				},
				Status: batchv1.JobStatus{
					Ready: new(int32(3)),
				},
			},
			want: true,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			got := tc.job.PodsReady(ctx, nil)
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}

func TestPodSetsInfo(t *testing.T) {
	testcases := map[string]struct {
		featureGates         map[featuregate.Feature]bool
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
		"replace stale workload slice annotation": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			job: (*Job)(utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				PodAnnotation(kueue.WorkloadSliceNameAnnotation, "old-slice").
				Obj()),
			runInfo: []podset.PodSetInfo{
				{
					Annotations: map[string]string{
						kueue.WorkloadSliceNameAnnotation: "new-slice",
					},
				},
			},
			wantUnsuspended: utiltestingjob.MakeJob("job", "ns").
				Parallelism(1).
				PodAnnotation(kueue.WorkloadSliceNameAnnotation, "new-slice").
				Suspend(false).
				Obj(),
			restoreInfo: []podset.PodSetInfo{
				{
					Annotations: map[string]string{
						kueue.WorkloadSliceNameAnnotation: "old-slice",
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
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			origSpec := *tc.job.Spec.DeepCopy()

			gotErr := tc.job.RunWithPodSetsInfo(ctx, nil, tc.runInfo)

			if diff := cmp.Diff(tc.wantRunError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.job.Spec, tc.wantUnsuspended.Spec); diff != "" {
				t.Errorf("node selectors mismatch (-want +got):\n%s", diff)
			}
			tc.job.RestorePodSetsInfo(t.Context(), tc.restoreInfo)
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
		featureGates map[featuregate.Feature]bool
		job          *Job
		wantPodSets  []kueue.PodSet
	}{
		"no partial admission": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			job:          (*Job)(jobTemplate.Clone().Parallelism(3).Obj()),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(*jobTemplate.Clone().Spec.Template.Spec.DeepCopy()).
					Obj(),
			},
		},
		"partial admission": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					SetAnnotation(JobMinParallelismAnnotation, "2").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(*jobTemplate.Clone().Spec.Template.Spec.DeepCopy()).
					SetMinimumCount(2).
					Obj(),
			},
		},
		"with required topology annotation": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					RequiredTopologyRequest("cloud.com/block").
					PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
					Obj(),
			},
		},
		"with preferred topology annotation": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					PreferredTopologyRequest("cloud.com/block").
					PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
					Obj(),
			},
		},
		"with slice-only topology": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
					PodAnnotation(kueue.PodSetSliceSizeAnnotation, "1").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{
						kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
						kueue.PodSetSliceSizeAnnotation:             "1",
					}).
					PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
					SliceRequiredTopologyRequest("cloud.com/block").
					SliceSizeTopologyRequest(1).
					Obj(),
			},
		},
		"with slice-only topology if TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
					PodAnnotation(kueue.PodSetSliceSizeAnnotation, "1").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{
						kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
						kueue.PodSetSliceSizeAnnotation:             "1",
					}).
					Obj(),
			},
		},
		"with slice-only topology – only podset slice required topology annotation": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{
						kueue.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
					}).
					PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
					Obj(),
			},
		},
		"with slice-only topology – only podset slice size annotation": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetSliceSizeAnnotation, "1").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{
						kueue.PodSetSliceSizeAnnotation: "1",
					}).
					PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
					Obj(),
			},
		},
		"without preferred topology annotation if TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetPreferredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},
		},
		"without required topology annotation if TAS is disabled": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
			job: (*Job)(
				jobTemplate.Clone().
					Parallelism(3).
					PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, "cloud.com/block").
					Obj(),
			),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					PodSpec(jobTemplate.Clone().Spec.Template.Spec).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.job.PodSets(ctx, nil)
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
	now := time.Now().Truncate(time.Second)

	const (
		localQueueName   = "foo"
		clusterQueueName = "cq"
	)
	clusterQueueNameWith100Chars := strings.Repeat("cq", 50)

	integrationManager := newTestIntegrationManager(t)
	t.Cleanup(integrationManager.EnableIntegrationsForTest(t, FrameworkName))
	baseJobWrapper := utiltestingjob.MakeJob("job", "ns").
		Suspend(true).
		Queue(localQueueName).
		Parallelism(10).
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	baseWorkloadWrapper := utiltestingapi.MakeWorkload("wl", "ns").
		Queue(localQueueName).
		Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now)

	baseWPCWrapper := utiltestingapi.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)

	highWPCWrapper := utiltestingapi.MakeWorkloadPriorityClass("test-wpc-high").
		PriorityValue(200)

	basePCWrapper := utiltesting.MakePriorityClass("test-pc").
		PriorityValue(200)

	testNamespace := utiltesting.MakeNamespaceWrapper("ns").Label(corev1.LabelMetadataName, "ns").Obj()

	baseWaitForPodsReadyConf := &configapi.WaitForPodsReady{}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool

		reconcilerOptions []jobframework.Option
		reconcileKey      *types.NamespacedName
		job               *batchv1.Job
		workloads         []kueue.Workload
		otherJobs         []batchv1.Job
		priorityClasses   []client.Object
		wantJob           batchv1.Job
		wantWorkloads     []kueue.Workload
		wantEvents        []utiltesting.EventRecord
		wantErr           error
	}{
		"job is not found with FinishOrphanedWorkloads disabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: false},
			reconcileKey: &types.NamespacedName{Namespace: "ns", Name: "deleted_job"},
			job:          nil,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(gvk, "deleted_job", "deleted_job").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					ControllerReference(gvk, "deleted_job", "deleted_job").
					Obj(),
			},
		},
		"job is not found with FinishOrphanedWorkloads enabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: true},
			reconcileKey: &types.NamespacedName{Namespace: "ns", Name: "deleted_job"},
			job:          nil,
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(gvk, "deleted_job", "deleted_job").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					ControllerReference(gvk, "deleted_job", "deleted_job").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadFinished,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.NewTime(now),
						Reason:             kueue.WorkloadFinishedReasonOwnerNotFound,
						Message:            "The workload's owner no longer exists",
					}).
					Obj(),
			},
		},
		"job is deleted with FinishOrphanedWorkloads disabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: false},
			job: baseJobWrapper.Clone().
				DeletionTimestamp(now).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				DeletionTimestamp(now).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(gvk, baseJobWrapper.GetName(), string(baseJobWrapper.GetUID())).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(gvk, baseJobWrapper.GetName(), string(baseJobWrapper.GetUID())).
					Obj(),
			},
		},
		"job is deleted with FinishOrphanedWorkloads enabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: true},
			job: baseJobWrapper.Clone().
				DeletionTimestamp(now).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				DeletionTimestamp(now).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(gvk, baseJobWrapper.GetName(), string(baseJobWrapper.GetUID())).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(gvk, baseJobWrapper.GetName(), string(baseJobWrapper.GetUID())).
					Obj(),
			},
		},
		"PodsReady is set to False before Workload is Admitted": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(false, now).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(false, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
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
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
				Ready(9).
				Failed(1).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(9).
				Failed(1).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
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
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForRecovery,
					Message: "At least one pod has failed, waiting for recovery",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForRecovery,
					Message: "At least one pod has failed, waiting for recovery",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job: baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Ready(10).
				Obj(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  "PodsReady",
					Message: "All pods reached readiness and the workload is running",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithWaitForPodsReady(baseWaitForPodsReadyConf),
			},
			job:     baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "InvalidReason",
					Message: "Not all pods are ready or succeeded",
				}).
				Obj(),
			},
			wantWorkloads: []kueue.Workload{*baseWorkloadWrapper.Clone().
				AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				PodAnnotation(kueue.WorkloadAnnotation, "wl").
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
		"Pod queue labels are not set when AssignQueueLabelsForPods is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: false,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
		"Pod cluster queue label is not set when cluster queue name is too long": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Queue(localQueueName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueNameWith100Chars)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl", "ns").
					Queue(localQueueName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueNameWith100Chars)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue " + clusterQueueNameWith100Chars,
				},
			},
		},
		"when workload is created, it has its owner ProvReq annotations": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Annotations(map[string]string{controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(0).
					Labels(map[string]string{controllerconsts.JobUIDLabel: "test-uid"}).
					Obj(),
			},

			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, "test-uid"),
				},
			},
		},
		"when workload is created, it has correct labels set": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
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
				jobframework.WithLabelKeysToCopy(sets.New("toCopyKey", "redundantToCopyKey")),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
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
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, "test-uid"),
				},
			},
		},
		"when workload is admitted the PodSetUpdates are propagated to job": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel("ac-key", "ac-value").
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 0},
					},
				}),
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
					Active(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(now),
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 0},
					},
				}),
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadDeactivated,
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(now),
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 0},
					},
				}),
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(now),
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 2 * time.Minute},
					},
				}),
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-2*time.Minute)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithObjectRetentionPolicies(&configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: time.Minute},
					},
				}),
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-2*time.Minute)).
					Active(false).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message:            "The workload is deactivated",
						LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
						Message: "The workload is deactivated",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  workloadpatching.ReasonWithCause(kueue.WorkloadDeactivated, kueue.WorkloadRequeuingLimitExceeded),
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now.Add(-time.Second)).
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
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
					Active(false).
					Queue(localQueueName).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
					AdmittedAt(true, now).
					Active(false).
					Queue(localQueueName).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  "NoReservation",
						Message: "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				NodeSelector("provisioning", "spot").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				NodeSelector("provisioning", "spot").
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodAnnotation("annotation-key1", "common-value").
				PodAnnotation("annotation-key2", "only-in-check1").
				PodLabel("label-key1", "common-value").
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				NodeSelector("node-selector-key1", "common-value").
				NodeSelector("node-selector-key2", "only-in-check2").
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job:     baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job:     baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Parallelism(8).
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					Queue(localQueueName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(8).Obj()).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					Queue(localQueueName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(8).Obj()).Obj(), now).
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
							SetMinimumCount(5).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(8).Obj()).Obj(), now).
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
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
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, "test-uid"),
				},
			},
		},
		"the workload is updated when queue name has changed for suspended job": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue-new").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
		},
		"a warning names the WorkloadPriorityClass that does not exist": {
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			// missing-wpc is deliberately absent here.
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			// The error identity is pinned in jobframework, where it is classified.
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Warning",
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		// The boundary the concept page draws. A Workload whose priority came from
		// a Pod PriorityClass is not re-resolved, so the label is taken and does
		// nothing, and nothing is reported. Tracked separately; pinned here so the
		// documented contract and the code cannot drift apart quietly.
		"a Pod PriorityClass-backed workload is left alone and reports nothing": {
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(2000).
					PodPriorityClassRef("pod-high").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(2000).
					PodPriorityClassRef("pod-high").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
		},
		// Same warning when the label is changed to a class that does not exist,
		// which reaches extractPriority through two more error wraps.
		"a warning names the WorkloadPriorityClass the label was changed to": {
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Warning",
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		"the workload is updated when priority class has changed for suspended job": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
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
					Message:   "Updated workload priority class: ns/job",
				},
			},
		},
		// An elastic Job whose pod set counts have not changed takes the workload
		// slice path, which returns the existing slice as compatible before the
		// priority is reconciled.
		// The negative half of the slice path, pinned here rather than only in
		// envtest: a reconcile against a fake client is synchronous, so the failed
		// lookup is observed rather than inferred from the absence of a change.
		"the workload slice is left alone when the class does not exist": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.AssignQueueLabelsForPods:     true,
				features.ElasticJobsViaWorkloadSlices: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass("missing-wpc").
				UID("test-uid").
				Obj(),
			// missing-wpc is deliberately absent.
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantErr: cmpopts.AnyError,
			// The compatible slice path now reaches the class lookup, so the failure
			// is reported rather than passing silently.
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: corev1.EventTypeWarning,
					Reason:    jobframework.ReasonWorkloadPriorityClassNotFound,
					Message:   `WorkloadPriorityClass "missing-wpc" not found`,
				},
			},
		},
		"the workload slice is updated when priority class has changed for suspended job": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.AssignQueueLabelsForPods:     true,
				features.ElasticJobsViaWorkloadSlices: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), highWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
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
					Message:   "Updated workload priority class: ns/job",
				},
			},
		},
		// EnsureWorkloadSlices writes the new counts itself before reporting the
		// slice compatible, so the priority update is a second write to the same
		// object in one reconcile.
		"the workload slice takes both a count and a priority class change": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.AssignQueueLabelsForPods:     true,
				features.ElasticJobsViaWorkloadSlices: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), highWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
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
					Message:   "Updated workload priority class: ns/job",
				},
			},
		},
		// A scale-up waiting for quota keeps the admitted slice alongside its
		// pending replacement, and normalizeActiveSlices returns only the
		// replacement. Both are live, so both have to follow the label.
		"the workload slice and its retained admitted slice both follow the label": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.AssignQueueLabelsForPods:     true,
				features.ElasticJobsViaWorkloadSlices: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), highWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("replacement", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/admitted").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("replacement", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
					Annotation(workloadslicing.WorkloadSliceReplacementFor, "ns/admitted").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			// Compared in order, and the reconciler visits the returned slice
			// before the retained admitted one.
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated workload priority class: ns/replacement",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated workload priority class: ns/admitted",
				},
			},
		},
		// Scaling up an admitted slice returns no slice at all, because a new one
		// is about to be created. The admitted slice is still live and still has to
		// follow the label.
		"the admitted slice follows the label when a scale-up replaces it": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.AssignQueueLabelsForPods:     true,
				features.ElasticJobsViaWorkloadSlices: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), highWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(baseWPCWrapper.Value).
					WorkloadPriorityClassRef(baseWPCWrapper.Name).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("admitted", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("job-job-2e122", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(highWPCWrapper.Value).
					WorkloadPriorityClassRef(highWPCWrapper.Name).
					Annotations(map[string]string{
						constants.ElasticJobAnnotation:              "true",
						kueue.WorkloadSliceNameAnnotation:           "admitted",
						workloadslicing.WorkloadSliceReplacementFor: "ns/admitted",
					}).
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
					Message:   "Updated workload priority class: ns/admitted",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/job-job-2e122",
				},
			},
		},
		// Deterministic counterpart to the integration spec: the fake client has no
		// such class, so the lookup cannot race with anything creating it.
		// The API server refuses to add a priorityClassRef once quota is reserved,
		// so the slice keeps none. The fake client does not enforce that rule, which
		// is what makes this case fail if the reconciler stops skipping the slice.
		"a workload slice that reserved quota with no priority class is left alone": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.AssignQueueLabelsForPods:     true,
				features.ElasticJobsViaWorkloadSlices: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(true).
				SetAnnotation(constants.ElasticJobAnnotation, "true").
				WorkloadPriorityClass(highWPCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				baseWPCWrapper.Obj(), highWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
		},
		"shouldn't update workload when priority class no changes": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(true).
				PriorityClass(basePCWrapper.Name).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				PriorityClass(basePCWrapper.Name).
				UID("test-uid").
				Obj(),
			priorityClasses: []client.Object{
				basePCWrapper.Obj(), baseWPCWrapper.Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).PriorityClass(basePCWrapper.Name).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(basePCWrapper.Value).
					PodPriorityClassRef(basePCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).PriorityClass(basePCWrapper.Name).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(basePCWrapper.Value).
					PodPriorityClassRef(basePCWrapper.Name).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
		},
		"the workload without uid label is created when job's uid is longer than 63 characters": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
			wantJob: *utiltestingjob.MakeJob("job", "ns").
				Suspend(false).
				Obj(),
		},
		"non-standalone job is suspended if its parent workload is not found": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
					UID("parent").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("unit-test", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent").
					Obj(),
			},
		},
		"non-standalone job is suspended if its parent workload is found and not admitted": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: baseJobWrapper.
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
					UID("parent").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent").
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
					UID("parent").
					Queue("queue").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent").
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("parent-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Count(10).Obj()).Obj(), now).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "parent", "parent").
					AdmittedAt(true, now).
					Obj(),
			},
		},
		"checking a second non-matching workload is deleted": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithManagedJobsNamespaceSelector(labels.Everything()),
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("first-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("second-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("first-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).Obj(), now).
					AdmittedAt(true, now).
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
			job: baseJobWrapper.Clone().
				Suspend(false).
				StartTime(now).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(true).
				StartTime(now).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"when workload is evicted, suspended and startTime is reset, restore node affinity": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(true).
				NodeSelector("provisioning", "spot").
				Active(10).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadEvicted,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
		},
		"when job completes, workload is marked as finished": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{
					Type:    batchv1.JobComplete,
					Status:  corev1.ConditionTrue,
					Message: "Job finished successfully",
				}).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(true, now).
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
					AdmittedAt(true, now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
					}).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.DeepCopy(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionFalse,
					}).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
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
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, "test-uid"),
				},
			},
		},
		"the workload is created when queue name is set, with PriorityClass": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PodPriorityClassRef("test-pc").
					Priority(200).
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
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, "test-uid"),
				},
			},
		},
		"the workload is created when queue name is set, with workloadPriorityClass and PriorityClass": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
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
				*utiltestingapi.MakeWorkload("job", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
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
					Message:   "Created Workload: ns/" + GetWorkloadNameForJob(baseJobWrapper.Name, "test-uid"),
				},
			},
		},
		"the workload shouldn't be recreated for the completed job": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
				Obj(),
			workloads: []kueue.Workload{},
			wantJob: *baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
				Obj(),
			wantWorkloads: []kueue.Workload{},
		},
		"when the prebuilt workload is missing, no new one is created, the job is suspended and prebuilt workload not found error is returned": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("missing-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				PrebuiltWorkloadLabel("missing-workload").
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
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
		"admitted prebuilt workload with implicit TAS remains in sync": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:  true,
				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				PodAnnotation(kueue.WorkloadAnnotation, "prebuilt-workload").
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				SchedulingGate(kueue.TopologySchedulingGate).
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				PodAnnotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				PodAnnotation(kueue.WorkloadAnnotation, "prebuilt-workload").
				PodLabel(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				PodLabel(constants.LocalQueueLabel, localQueueName).
				PodLabel(constants.ClusterQueueLabel, clusterQueueName).
				SchedulingGate(kueue.TopologySchedulingGate).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
						Obj()).
					Queue(localQueueName).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(clusterQueueName).
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Count(10).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 10).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
						Request(corev1.ResourceCPU, "1").
						PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
						Obj()).
					Queue(localQueueName).
					ReserveQuotaAt(utiltestingapi.MakeAdmission(clusterQueueName).
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Count(10).
							TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
								Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 10).Obj()).
								Obj()).
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Labels(map[string]string{controllerconsts.JobUIDLabel: "test-uid"}).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
					Obj(),
			},
		},
		"prebuilt workload with a different topology request is out of sync": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("prebuilt-workload").
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				PrebuiltWorkloadLabel("prebuilt-workload").
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("test-pc").
						Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: corev1.LabelTopologyZone}).
						RequiredTopologyRequest(corev1.LabelTopologyZone).
						Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
						Request(corev1.ResourceCPU, "1").
						PriorityClass("test-pc").
						Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: corev1.LabelTopologyZone}).
						RequiredTopologyRequest(corev1.LabelTopologyZone).
						Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
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
		"when the prebuilt workload is owned by another object": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
					ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "other-job", "other-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.
				Clone().
				Suspend(false).
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				PrebuiltWorkloadLabel("prebuilt-workload").
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("prebuilt-workload", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					WorkloadPriorityClassRef("test-wpc").
					Priority(100).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().Toleration(corev1.Toleration{
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
				*utiltestingapi.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue(localQueueName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
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
				*utiltestingapi.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue(localQueueName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().Toleration(corev1.Toleration{
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
				*utiltestingapi.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue(localQueueName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(kueue.PodSetAssignment{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: new(int32(10)),
					}).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue(localQueueName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(kueue.PodSetAssignment{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: new(int32(10)),
					}).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
			},
		},
		"the workload is admitted, job still suspended and tolerations change": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().Toleration(corev1.Toleration{
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
				*utiltestingapi.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name, baseJobWrapper.GetUID()), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue(localQueueName).
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(kueue.PodSetAssignment{
						Name: kueue.DefaultPodSetName,
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "default",
						},
						Count: new(int32(10)),
					}).Obj(), now).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(false, now).
					Active(false).
					Queue(localQueueName).
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
					AdmittedAt(false, now).
					Active(false).
					Queue(localQueueName).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*baseWorkloadWrapper.Clone().
					AdmittedAt(false, now).
					Active(false).
					Queue(localQueueName).
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
					AdmittedAt(false, now).
					Active(false).
					Queue(localQueueName).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					MaximumExecutionTimeSeconds(10).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			job: baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					MaximumExecutionTimeSeconds(5).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
					Priority(0).
					Labels(map[string]string{controllerconsts.JobUIDLabel: string(baseJobWrapper.GetUID())}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("job", "ns").
					MaximumExecutionTimeSeconds(10).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue(localQueueName).
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManagedJobsNamespaceSelector(labels.SelectorFromSet(map[string]string{
					"managed-by-kueue": "true",
				})),
			},
			job: baseJobWrapper.
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
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: false,

				features.AssignQueueLabelsForPods: true,
			},
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManagedJobsNamespaceSelector(labels.SelectorFromSet(map[string]string{
					"managed-by-kueue": "true",
				})),
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job: utiltestingjob.MakeJob("job", "labelled-ns").
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
				*utiltestingapi.MakeWorkload("job", "labelled-ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).
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
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGatesDuringTest(t, tc.featureGates)
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)

				ctx, _ := utiltesting.ContextWithLog(t)
				clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(
					interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
				indexer := utiltesting.AsIndexer(clientBuilder)
				if err := SetupIndexes(ctx, indexer); err != nil {
					t.Fatalf("Could not setup indexes: %v", err)
				}

				labelledNamespace := utiltesting.MakeNamespaceWrapper("labelled-ns").
					Label("managed-by-kueue", "true").
					Obj()

				objs := append(tc.priorityClasses, utiltestingapi.MakeResourceFlavor("default").Obj(), testNamespace, labelledNamespace)
				if tc.job != nil {
					objs = append(objs, tc.job)
				}

				kClient := clientBuilder.
					WithObjects(objs...).
					WithLists(&batchv1.JobList{Items: tc.otherJobs}).
					WithStatusSubresource(&kueue.Workload{}).
					Build()

				prebuiltWorkload := ""
				if tc.job != nil {
					// For prebuilt workloads we are skipping the ownership setup in the test body and
					// expect the reconciler to do it.
					prebuiltWorkload = jobframework.PrebuiltWorkloadNameFor(tc.job)
				}

				for _, testWl := range tc.workloads {
					controller := metav1.GetControllerOfNoCopy(&testWl)
					if prebuiltWorkload == "" && controller == nil {
						if err := ctrl.SetControllerReference(tc.job, &testWl, kClient.Scheme()); err != nil {
							t.Fatalf("Could not setup owner reference in Workloads: %v", err)
						}
					}
					if err := kClient.Create(ctx, &testWl); err != nil {
						t.Fatalf("Could not create workload: %v", err)
					}
				}
				recorder := &utiltesting.EventRecorder{}
				reconciler, err := NewReconciler(
					ctx,
					kClient,
					indexer,
					recorder,
					append(
						tc.reconcilerOptions,
						jobframework.WithIntegrationManager(integrationManager),
						jobframework.WithCache(schdcache.New(kClient)),
						jobframework.WithClock(testingclock.NewFakeClock(now)),
					)...,
				)
				if err != nil {
					t.Errorf("Error creating the reconciler: %v", err)
				}

				var reconcileRequest reconcile.Request
				if tc.reconcileKey != nil {
					reconcileRequest.NamespacedName = *tc.reconcileKey
				} else {
					reconcileRequest.NamespacedName = client.ObjectKeyFromObject(tc.job)
				}
				_, err = reconciler.Reconcile(ctx, reconcileRequest)
				if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
					t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
				}

				var gotJob batchv1.Job
				if err := kClient.Get(ctx, reconcileRequest.NamespacedName, &gotJob); client.IgnoreNotFound(err) != nil {
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
				if prebuiltWorkload != "" {
					wlCheckOpts = workloadCmpOptsWithOwner
				}

				// The fake client with patch.Apply cannot reset the Admission field (patch.Merge can).
				// However, other important Status fields (e.g. Conditions) still reflect the change,
				// so we deliberately ignore the Admission field here.
				if features.Enabled(features.WorkloadRequestUseMergePatch) {
					wlCheckOpts = append(wlCheckOpts, cmpopts.IgnoreFields(kueue.WorkloadStatus{}, "Admission"))
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
}

func TestCleanLabels(t *testing.T) {
	cases := map[string]struct {
		labels     map[string]string
		wantLabels map[string]string
	}{
		"feature enabled": {
			labels: map[string]string{
				"foo":                      "bar",
				batchv1.JobNameLabel:       "job-name",
				"controller-uid":           "uid",
				batchv1.ControllerUidLabel: "uid",
			},
			wantLabels: map[string]string{
				"foo":                "bar",
				batchv1.JobNameLabel: "job-name",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			print(tc.labels)
			pt := &corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: tc.labels,
				},
			}
			cleanLabels(pt)
			if diff := cmp.Diff(tc.wantLabels, pt.Labels); diff != "" {
				t.Errorf("cleanLabels() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestTerminalIndexesCount(t *testing.T) {
	cases := map[string]struct {
		completedIndexes string
		failedIndexes    string
		completions      int32
		want             int32
	}{
		"empty":                               {completedIndexes: "", completions: 10, want: 0},
		"zero completions":                    {completedIndexes: "0-9", completions: 0, want: 0},
		"single completed index":              {completedIndexes: "0", completions: 10, want: 1},
		"single failed index":                 {failedIndexes: "1", completions: 10, want: 1},
		"single range":                        {completedIndexes: "0-4", completions: 10, want: 5},
		"mixed intervals":                     {completedIndexes: "0-4,7,9-11", completions: 10, want: 7},
		"completed and failed indexes":        {completedIndexes: "0-2,7", failedIndexes: "3-5,8", completions: 10, want: 8},
		"overlapping terminal indexes":        {completedIndexes: "0-4", failedIndexes: "3-7", completions: 10, want: 8},
		"contained failed indexes":            {completedIndexes: "0-9", failedIndexes: "2-3", completions: 10, want: 10},
		"surviving low indexes":               {completedIndexes: "0-8", completions: 10, want: 9},
		"all completed within range":          {completedIndexes: "0-14", completions: 10, want: 10},
		"range straddling the cap":            {completedIndexes: "5-19", completions: 10, want: 5},
		"all removed (above the cap)":         {completedIndexes: "10-19", completions: 10, want: 0},
		"failed range straddling the cap":     {failedIndexes: "5-19", completions: 10, want: 5},
		"all failed removed (above the cap)":  {failedIndexes: "10-19", completions: 10, want: 0},
		"stale failed index after scale-down": {failedIndexes: "4", completions: 3, want: 0},
		"cap applied to completed and failed": {completedIndexes: "0-1", failedIndexes: "2-19", completions: 5, want: 5},
		"range capped tighter":                {completedIndexes: "0-4", completions: 3, want: 3},
		"discrete indexes":                    {completedIndexes: "3,5,7", completions: 10, want: 3},
		"discrete indexes partly above":       {completedIndexes: "3,5,12", completions: 10, want: 2},
		"malformed interval skipped":          {completedIndexes: "abc,0-2", completions: 10, want: 3},
		"malformed range end skipped":         {completedIndexes: "0-x,4", completions: 10, want: 1},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			if got := terminalIndexesCount(log, tc.completedIndexes, tc.failedIndexes, tc.completions); got != tc.want {
				t.Errorf("terminalIndexesCount(%q, %q, %d) = %d, want %d", tc.completedIndexes, tc.failedIndexes, tc.completions, got, tc.want)
			}
		})
	}
}

func TestReclaimablePods(t *testing.T) {
	indexedJob := func(succeeded, failed int32, completedIndexes, failedIndexes string) *Job {
		j := utiltestingjob.MakeJob("job", "ns").
			Indexed(true).
			Parallelism(8).
			Completions(8).
			Obj()
		j.Status.Succeeded = succeeded
		j.Status.Failed = failed
		j.Status.CompletedIndexes = completedIndexes
		if failedIndexes != "" {
			j.Spec.BackoffLimitPerIndex = new(int32(0))
			j.Status.FailedIndexes = new(failedIndexes)
		}
		return (*Job)(j)
	}
	retryableFailureJob := indexedJob(1, 1, "0", "")
	retryableFailureJob.Spec.BackoffLimitPerIndex = new(int32(1))
	cases := map[string]struct {
		job  *Job
		want []kueue.ReclaimablePod
	}{
		// An ordinary (non-elastic) Indexed Job must reclaim its completed indexes
		// exactly as before, now that the count is derived from the terminal index sets.
		"indexed Job reclaims its completed indexes": {
			job:  indexedJob(4, 0, "0-3", ""),
			want: []kueue.ReclaimablePod{{Name: kueue.DefaultPodSetName, Count: 4}},
		},
		"indexed Job holds quota for a retryable failure": {
			job:  retryableFailureJob,
			want: []kueue.ReclaimablePod{{Name: kueue.DefaultPodSetName, Count: 1}},
		},
		"indexed Job reclaims completed and failed indexes": {
			job:  indexedJob(1, 1, "0", "1"),
			want: []kueue.ReclaimablePod{{Name: kueue.DefaultPodSetName, Count: 2}},
		},
		// Status counters without terminal index sets should not happen with the
		// native Job controller, but can with a custom spec.managedBy controller.
		// We trust completedIndexes and failedIndexes and hold the quota.
		"indexed Job with empty terminal indexes holds quota": {
			job:  indexedJob(4, 0, "", ""),
			want: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := tc.job.ReclaimablePods(t.Context(), nil)
			if err != nil {
				t.Fatalf("ReclaimablePods() returned error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("ReclaimablePods() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
