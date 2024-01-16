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
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
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
		cmpopts.IgnoreFields(batchv1.Job{}, "TypeMeta", "ObjectMeta.OwnerReferences", "ObjectMeta.ResourceVersion", "ObjectMeta.Annotations"),
	}
	workloadCmpOpts = []cmp.Option{
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
	workloadCmpOptsWithOwner = []cmp.Option{
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
	baseJobWrapper := utiltestingjob.MakeJob("job", "ns").
		Suspend(true).
		Queue("foo").
		Parallelism(10).
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	baseWPCWrapper := utiltesting.MakeWorkloadPriorityClass("test-wpc").
		PriorityValue(100)

	basePCWrapper := utiltesting.MakePriorityClass("test-pc").
		PriorityValue(200)

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               batchv1.Job
		workloads         []kueue.Workload
		priorityClasses   []client.Object
		wantJob           batchv1.Job
		wantWorkloads     []kueue.Workload
		wantEvents        []utiltesting.EventRecord
		wantErr           error
	}{
		"when workload is admitted the PodSetUpdates are propagated to job": {
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				PodLabel("ac-key", "ac-value").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
		"when workload is admitted and spec.active is set to false, the workload's conditions is set to Evicted": {
			job: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Active(false).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "InactiveWorkload",
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
							},
						},
					}).
					Obj(),
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Active(false).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "InactiveWorkload",
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
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
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "InactiveWorkload",
						Message: "The workload is deactivated",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
		"when job is initially suspended, the Workload has active=false and it's not admitted, " +
			"it should not get an evicted condition, but the job should remain suspended": {
			job: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
								Name: "main",
								Labels: map[string]string{
									"ac-key": "ac-value",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
								Name: "main",
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
								Labels: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
								Annotations: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
								NodeSelector: map[string]string{
									"ac-key": "ac-value2",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
								NodeSelector: map[string]string{
									"provisioning": "on-demand",
								},
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
				NodeSelector("node-selector-key1", "common-value").
				NodeSelector("node-selector-key2", "only-in-check2").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check1",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "main",
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
								Name: "main",
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
			},
			job: *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Obj(),
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/a",
				},
			},
		},
		"non-matching non-admitted workload is updated": {
			reconcilerOptions: []jobframework.Option{
				jobframework.WithManageJobsWithoutQueueName(true),
			},
			job:     *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.DeepCopy(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(8).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
			},
			job: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
					Message:   "Created Workload: ns/job-job-ed7d5",
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
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
				UID("test-uid").
				Obj(),
			wantJob: *baseJobWrapper.
				Clone().
				UID("test-uid").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					PriorityClass("new-priority-class").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("foo").
					Priority(0).
					PriorityClass("new-priority-class").
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
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
					Message:   "Created Workload: ns/job-job-ed7d5",
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
				OwnerReference("parent", batchv1.SchemeGroupVersion.WithKind("CronJob")).
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
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
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
				*utiltesting.MakeWorkload("parent-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
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
				*utiltesting.MakeWorkload("parent-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("parent-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
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
				*utiltesting.MakeWorkload("first-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("second-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).SetMinimumCount(5).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("first-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
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
				Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().
				Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(10).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  "JobFinished",
						Message: "Job finished successfully",
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
		"when the workload is finished, its finalizer is removed": {
			job: *baseJobWrapper.Clone().Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadFinished,
						Status: metav1.ConditionTrue,
					}).
					Obj(),
			},
			wantJob: *baseJobWrapper.Clone().Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					PodSets(*utiltesting.MakePodSet("main", 10).Request(corev1.ResourceCPU, "1").Obj()).
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
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
					Message:   "Created Workload: ns/job-job-ed7d5",
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
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
					Message:   "Created Workload: ns/job-job-ed7d5",
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
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
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
					Message:   "Created Workload: ns/job-job-ed7d5",
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
		"when the prebuilt workload is missing, no new one is created and the job is suspended": {
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
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					OwnerReference(batchv1.SchemeGroupVersion.String(), "Job", "job", "test-uid", true, true).
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
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					OwnerReference(batchv1.SchemeGroupVersion.String(), "Job", "other-job", "other-uid", true, true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 10).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					OwnerReference(batchv1.SchemeGroupVersion.String(), "Job", "other-job", "other-uid", true, true).
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
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("prebuilt-workload", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").PriorityClass("test-pc").Obj()).
					Queue("test-queue").
					PriorityClass("test-wpc").
					Priority(100).
					PriorityClassSource(constants.WorkloadPriorityClassSource).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					OwnerReference(batchv1.SchemeGroupVersion.String(), "Job", "job", "test-uid", true, true).
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
		},
		"the workload is not admitted and tolerations change": {
			job: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).
				Obj(),
			wantJob: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet("main", 10).
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
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet("main", 10).
							Toleration(corev1.Toleration{
								Key:      "tolerationkey2",
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
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated not matching Workload for suspended job: ns/job-job-ed7d5",
				},
			},
		},
		"the workload is admitted and tolerations change": {
			job: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey2",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).
				Suspend(false).
				Obj(),
			wantJob: *baseJobWrapper.Clone().Toleration(corev1.Toleration{
				Key:      "tolerationkey1",
				Operator: corev1.TolerationOpExists,
				Effect:   corev1.TaintEffectNoSchedule,
			}).Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet("main", 10).
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
						Name: "main",
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
					Reason:    "Stopped",
					Message:   "No matching Workload; restoring pod templates according to existent Workload",
				},
				{
					Key:       types.NamespacedName{Name: "job", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/job-job-ed7d5",
				},
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
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
				*utiltesting.MakeWorkload(GetWorkloadNameForJob(baseJobWrapper.Name), "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Queue("foo").
					PodSets(
						*utiltesting.MakePodSet("main", 10).
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
						Name: "main",
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
					Message:   "Deleted not matching Workload: ns/job-job-ed7d5",
				},
			},
			wantErr: jobframework.ErrNoMatchingWorkloads,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			objs := append(tc.priorityClasses, &tc.job, utiltesting.MakeResourceFlavor("default").Obj())
			kcBuilder := clientBuilder.
				WithObjects(objs...)

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			// For prebuilt workloads we are skipping the ownership setup in the test body and
			// expect the reconciler to do it.
			_, useesPrebuiltWorkload := tc.job.Labels[controllerconsts.PrebuiltWorkloadLabel]

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				if !useesPrebuiltWorkload {
					if err := ctrl.SetControllerReference(&tc.job, &tc.workloads[i], kClient.Scheme()); err != nil {
						t.Fatalf("Could not setup owner reference in Workloads: %v", err)
					}
				}
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}
			recorder := &utiltesting.EventRecorder{}
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
