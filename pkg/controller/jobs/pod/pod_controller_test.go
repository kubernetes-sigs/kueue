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

package pod

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
)

type keyUIDs struct {
	key  types.NamespacedName
	uids []types.UID
}

func TestPodsReady(t *testing.T) {
	testCases := map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"pod is ready": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				StatusConditions(
					corev1.PodCondition{
						Type:   corev1.PodReady,
						Status: corev1.ConditionTrue,
					},
				).
				Obj(),
			want: true,
		},
		"pod is not ready": {
			pod: testingpod.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				StatusConditions().
				Obj(),
			want: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			pod := FromObject(tc.pod)
			got := pod.PodsReady()
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}

func TestRun(t *testing.T) {
	testCases := map[string]struct {
		pods                 []corev1.Pod
		runInfo, restoreInfo []podset.PodSetInfo
		wantErr              error
	}{
		"pod set info > 1 for the single pod": {
			pods:    []corev1.Pod{*testingpod.MakePod("test-pod", "test-namespace").Obj()},
			runInfo: make([]podset.PodSetInfo, 2),
			wantErr: podset.ErrInvalidPodsetInfo,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			pod := FromObject(&tc.pods[0])

			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}

			kClient := clientBuilder.WithLists(&corev1.PodList{Items: tc.pods}).Build()

			gotErr := pod.Run(ctx, kClient, tc.runInfo, nil, "")

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		pod         *Pod
		wantPodSets func(pod *Pod) []kueue.PodSet
	}{
		"no annotations": {
			pod: FromObject(testingpod.MakePod("pod", "ns").Obj()),
			wantPodSets: func(pod *Pod) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     kueue.DefaultPodSetName,
						Count:    1,
						Template: corev1.PodTemplateSpec{Spec: *pod.pod.Spec.DeepCopy()},
					},
				}
			},
		},
		"with required topology annotation": {
			pod: FromObject(testingpod.MakePod("pod", "ns").
				Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			),
			wantPodSets: func(pod *Pod) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     kueue.DefaultPodSetName,
						Count:    1,
						Template: corev1.PodTemplateSpec{Spec: *pod.pod.Spec.DeepCopy()},
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/block"),
							PodIndexLabel: ptr.To(kueuealpha.PodGroupPodIndexLabel)},
					},
				}
			},
		},
		"with required topology preferred": {
			pod: FromObject(testingpod.MakePod("pod", "ns").
				Annotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			),
			wantPodSets: func(pod *Pod) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:     kueue.DefaultPodSetName,
						Count:    1,
						Template: corev1.PodTemplateSpec{Spec: *pod.pod.Spec.DeepCopy()},
						TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block"),
							PodIndexLabel: ptr.To(kueuealpha.PodGroupPodIndexLabel)},
					},
				}
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantPodSets(tc.pod), tc.pod.PodSets()); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

var (
	podCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(corev1.Pod{}, "TypeMeta", "ObjectMeta.ResourceVersion",
			"ObjectMeta.DeletionTimestamp"),
		cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime"),
	}
	defaultWorkloadCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b kueue.Workload) bool {
			return a.Name < b.Name
		}),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool {
			return a.Type < b.Type
		}),
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta",
			"ObjectMeta.ResourceVersion",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
	}
)

func TestReconciler(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	testStartTime := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(testStartTime)

	basePodWrapper := testingpod.MakePod("pod", "ns").
		UID("test-uid").
		Queue("user-queue").
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	now := time.Now()

	podUID := "dc85db45"

	testCases := map[string]struct {
		reconcileKey           *types.NamespacedName
		initObjects            []client.Object
		pods                   []corev1.Pod
		wantPods               []corev1.Pod
		workloads              []kueue.Workload
		wantWorkloads          []kueue.Workload
		wantErr                error
		workloadCmpOpts        []cmp.Option
		excessPodsExpectations []keyUIDs
		// If true, the test will delete workloads before running reconcile
		deleteWorkloads bool

		wantEvents        []utiltesting.EventRecord
		reconcilerOptions []jobframework.Option
	}{
		"scheduling gate is removed and node selector is added if workload is admitted": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").NodeLabel("kubernetes.io/arch", "arm64").Obj(),
			},
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				NodeSelector("kubernetes.io/arch", "arm64").
				KueueFinalizer().
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(1).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(1).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},

		"non-matching admitted workload is deleted and pod is finalized": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				Obj()},
			wantPods: nil,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantErr:         jobframework.ErrNoMatchingWorkloads,
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "No matching Workload; restoring pod templates according to existent Workload",
				},
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "DeletedWorkload",
					Message:   "Deleted not matching Workload: ns/unit-test",
				},
			},
		},
		"the workload is created when queue name is set": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj()},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForPod(basePodWrapper.GetName(), basePodWrapper.GetUID()), "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("test-queue").
					Priority(0).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForPod(basePodWrapper.GetName(), basePodWrapper.GetUID()),
				},
			},
		},
		"the pod reconciliation is skipped when 'kueue.x-k8s.io/managed' label is not set": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Obj()},
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"pod is stopped when workload is evicted": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				Queue("test-queue").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				Queue("test-queue").
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  corev1.ConditionTrue,
					Reason:  "WorkloadEvictedDueToPreempted",
					Message: "Preempted to accommodate a higher priority Workload",
				}).
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Preempted to accommodate a higher priority Workload",
				},
			},
		},
		"pod is finalized when it's succeeded": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				StatusMessage("Job finished successfully").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				StatusPhase(corev1.PodSucceeded).
				StatusMessage("Job finished successfully").
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "Job finished successfully",
					}).
					Obj(),
			},
			workloadCmpOpts: append(
				defaultWorkloadCmpOpts,
				// This is required because SSA doesn't work properly for the fake k8s client.
				// Reconciler appends "Finished" condition using SSA. Fake client will just
				// replace all the older conditions.
				// See: https://github.com/kubernetes-sigs/controller-runtime/issues/2341
				cmpopts.IgnoreSliceElements(func(c metav1.Condition) bool {
					return c.Type == "Admitted"
				}),
			),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/unit-test' is declared finished",
				},
			},
		},
		"workload status condition is added even if the pod is finalized": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				StatusPhase(corev1.PodSucceeded).
				StatusMessage("Job finished successfully").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				StatusPhase(corev1.PodSucceeded).
				StatusMessage("Job finished successfully").
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "Job finished successfully",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/unit-test' is declared finished",
				},
			},
		},
		"pod without scheduling gate is terminated if workload is not admitted": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  corev1.ConditionTrue,
					Reason:  string(jobframework.StopReasonNotAdmitted),
					Message: "Not admitted by cluster queue",
				}).
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Not admitted by cluster queue",
				},
			},
		},
		"when a workload is created for the pod it has its ProvReq annotations copied": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Label(constants.ManagedByKueueLabel, "true").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Label(constants.ManagedByKueueLabel, "true").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("wl", "ns").
					Annotations(map[string]string{controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					Obj(),
			},
			workloadCmpOpts: []cmp.Option{
				cmpopts.IgnoreFields(kueue.Workload{},
					"TypeMeta",
					"ObjectMeta.Name",
					"ObjectMeta.Finalizers",
					"ObjectMeta.ResourceVersion",
					"ObjectMeta.OwnerReferences",
					"ObjectMeta.Labels",
					"Spec",
				),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForPod("pod", "test-uid"),
				},
			},
		},
		"workload is composed and created for the pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{
						"kueue.x-k8s.io/is-group-workload":                           "true",
						controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"workload is composed and created for the pod group with fast admission": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Group("test-group").
					GroupTotalCount("3").
					Annotation(GroupFastAdmissionAnnotation, "true").
					Obj(),
			},
			wantPods: []corev1.Pod{
				// Other pods are created on second reconcile
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Annotation(controllerconsts.ProvReqAnnotationPrefix+"test-annotation", "test-val").
					Annotation("invalid-provreq-prefix/test-annotation-2", "test-val-2").
					Group("test-group").
					GroupTotalCount("3").
					Annotation(GroupFastAdmissionAnnotation, "true").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Annotations(map[string]string{
						"kueue.x-k8s.io/is-group-workload":                           "true",
						controllerconsts.ProvReqAnnotationPrefix + "test-annotation": "test-val"}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"workload is composed and created for the pod group with max exec time": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("dc85db45", 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{
						"kueue.x-k8s.io/is-group-workload": "true",
					}).
					MaximumExecutionTimeSeconds(10).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"workload is recreated when max exec time changes": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("dc85db45", 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{"kueue.x-k8s.io/is-group-workload": "true"}).
					MaximumExecutionTimeSeconds(5).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Label(controllerconsts.MaxExecTimeSecondsLabel, "10").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("dc85db45", 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{"kueue.x-k8s.io/is-group-workload": "true"}).
					MaximumExecutionTimeSeconds(10).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "UpdatedWorkload",
					Message:   "Updated not matching Workload for suspended job: ns/test-group",
				},
			},
		},
		"workload is found for the pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Queue("user-queue").
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Queue("user-queue").
					Priority(0).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"scheduling gate is removed for all pods in the group if workload is admitted": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").NodeLabel("kubernetes.io/arch", "arm64").Obj(),
			},
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					NodeSelector("kubernetes.io/arch", "arm64").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					NodeSelector("kubernetes.io/arch", "arm64").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(podUID, 2).Request(corev1.ResourceCPU, "1").Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(
						utiltesting.MakeAdmission("cq", podUID).
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(podUID, 2).Request(corev1.ResourceCPU, "1").Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(
						utiltesting.MakeAdmission("cq", podUID).
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
				{
					Key:       types.NamespacedName{Name: "pod2", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"workload is not finished if the pod in the group is running": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},

			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Admitted(true).
					ReclaimablePods(kueue.ReclaimablePod{Name: podUID, Count: 1}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"workload is finished if all pods in the group has finished": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "Pods succeeded: 2/2.",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/test-group' is declared finished",
				},
			},
		},
		"workload is not deleted if the pod in group has been deleted after admission": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				Group("test-group").
				GroupTotalCount("2").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				Group("test-group").
				GroupTotalCount("2").
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"pod group remains stopped when workload is evicted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(podUID, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(podUID, 2).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"Pods are finalized even if one of the pods in the finished group is absent": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					KueueFinalizer().
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},

			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "Pods succeeded: 1/2. Pods failed: 1/2",
					}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "Pods succeeded: 1/2. Pods failed: 1/2",
					}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/test-group' is declared finished",
				},
			},
		},
		"workload for pod group with different queue names shouldn't be created": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("new-test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("new-test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},

			workloads:       []kueue.Workload{},
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"all pods in group should be removed if workload is deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(podUID, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Queue("test-queue").
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: append(defaultWorkloadCmpOpts, cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.DeletionTimestamp")),
			deleteWorkloads: true,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Workload is deleted",
				},
				{
					Key:       types.NamespacedName{Name: "pod2", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Workload is deleted",
				},
			},
		},
		"replacement pod should be started for pod group of size 1": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod2", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"replacement pod should be started for set of Running, Failed, Succeeded pods": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(3).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReclaimablePods(kueue.ReclaimablePod{Name: podUID, Count: 1}).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(3).Obj()).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  podUID,
						Count: 1,
					}).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement", "test-uid").
					ReclaimablePods(kueue.ReclaimablePod{Name: podUID, Count: 1}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "test-group", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "OwnerReferencesAdded",
					Message:   "Added 1 owner reference(s)",
				},
				{
					Key:       types.NamespacedName{Name: "replacement", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
			},
		},
		"pod group of size 2 is finished when 2 pods has succeeded and 1 pod has failed": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "Pods succeeded: 2/2.",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/test-group' is declared finished",
				},
			},
		},
		"wl should not get the quota reservation cleared for a running pod group of size 1": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodRunning).
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodRunning).
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"wl should get the quota reservation cleared for a failed pod group of size 1": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodFailed).
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodFailed).
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(testStartTime),
						Reason:             "NoReservation",
						Message:            "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
						Reason:             "Pending",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadRequeued,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"deleted pods in group should not be finalized if the workload doesn't match": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					NodeName("test-node").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					NodeName("test-node").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					NodeName("test-node").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					NodeName("test-node").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("incorrect-role-name", 1).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					ReserveQuota(
						utiltesting.MakeAdmission("cq").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(podUID, 2).Request(corev1.ResourceCPU, "1").NodeName("test-node").Obj()).
					Queue("test-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{"kueue.x-k8s.io/is-group-workload": "true"}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			deleteWorkloads: true,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"workload is not deleted if all of the pods in the group are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"workload is not deleted if one pod role is absent from the cluster": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("absent-pod-role", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("absent-pod-role", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"if pod group is finished and wl is deleted, new workload shouldn't be created": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads:       []kueue.Workload{},
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"if pod in group is scheduling gated and wl is deleted, workload should be recreated": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{"kueue.x-k8s.io/is-group-workload": "true"}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"if there's not enough non-failed pods in the group, workload should not be created": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			workloads:       []kueue.Workload{},
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Warning",
					Reason:    "ErrWorkloadCompose",
					Message:   "'test-group' group has fewer runnable pods than expected",
				},
			},
		},
		"pod group is considered finished if there is an unretriable pod and no running pods": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Annotation("kueue.x-k8s.io/retriable-in-group", "false").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					Request(corev1.ResourceMemory, "1Gi").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Annotation("kueue.x-k8s.io/retriable-in-group", "false").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					Request(corev1.ResourceMemory, "1Gi").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("a119f908", 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Obj(),
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("a119f908", 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Obj(),
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Condition(
						metav1.Condition{
							Type:    kueue.WorkloadFinished,
							Status:  metav1.ConditionTrue,
							Reason:  kueue.WorkloadFinishedReasonSucceeded,
							Message: "Pods succeeded: 1/3.",
						},
					).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "FinishedWorkload",
					Message:   "Workload 'ns/test-group' is declared finished",
				},
			},
		},
		"reclaimable pods updated for pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					Request(corev1.ResourceMemory, "1Gi").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					Request(corev1.ResourceMemory, "1Gi").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4ebdd4a6", 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Obj(),
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4ebdd4a6", 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Obj(),
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReclaimablePods(kueue.ReclaimablePod{Name: "4ebdd4a6", Count: 1}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"reclaimablePods field is not updated for a serving pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					PodGroupServingAnnotation(true).
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					PodGroupServingAnnotation(true).
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					Request(corev1.ResourceMemory, "1Gi").
					GroupTotalCount("3").
					PodGroupServingAnnotation(true).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					PodGroupServingAnnotation(true).
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					PodGroupServingAnnotation(true).
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					Request(corev1.ResourceMemory, "1Gi").
					GroupTotalCount("3").
					PodGroupServingAnnotation(true).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4ebdd4a6", 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Obj(),
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4ebdd4a6", 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Obj(),
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"excess pods before wl creation, youngest pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now.Add(time.Minute)).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
			},
			workloads: []kueue.Workload{},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Annotations(map[string]string{"kueue.x-k8s.io/is-group-workload": "true"}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
				{
					Key:       types.NamespacedName{Name: "pod2", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "ExcessPodDeleted",
					Message:   "Excess pod deleted",
				},
			},
		},
		"excess pods before admission, youngest pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(time.Minute)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute * 2)).
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(time.Minute)).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Queue("user-queue").
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod3", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "ExcessPodDeleted",
					Message:   "Excess pod deleted",
				},
			},
		},
		// In this case, group-total-count is equal to the number of pods in the cluster.
		// But one of the roles is missing, and another role has an excess pod.
		"excess pods in pod set after admission, youngest pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute * 2)).
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("aaf269e6", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("aaf269e6", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			excessPodsExpectations: []keyUIDs{{
				key:  types.NamespacedName{Name: "another-group", Namespace: "ns"},
				uids: []types.UID{"pod"},
			}},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod2", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "ExcessPodDeleted",
					Message:   "Excess pod deleted",
				},
			},
		},
		"waiting to observe previous deletion of excess pod, no pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("1").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("1").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("1").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			excessPodsExpectations: []keyUIDs{{
				key:  types.NamespacedName{Name: "test-group", Namespace: "ns"},
				uids: []types.UID{"pod2"},
			}},
		},
		"delete excess pod that is gated": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "pod2").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "pod2").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod3", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "ExcessPodDeleted",
					Message:   "Excess pod deleted",
				},
			},
		},
		// If an excess pod is already deleted and finalized, but an external finalizer blocks
		// pod deletion, kueue should ignore such a pod, when creating a workload.
		"deletion of excess pod is blocked by another controller": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueSchedulingGate().
					Finalizer("kubernetes").
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now.Add(time.Minute)).
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueSchedulingGate().
					Finalizer("kubernetes").
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now.Add(time.Minute)).
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{"kueue.x-k8s.io/is-group-workload": "true"}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"deleted pods in incomplete group are finalized": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("p1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodPending).
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("p2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodPending).
					Delete().
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "p1", Namespace: "ns"},
					EventType: "Warning",
					Reason:    "ErrWorkloadCompose",
					Message:   "'group' group has fewer runnable pods than expected",
				},
			},
		},
		"finalize workload for non existent pod": {
			reconcileKey: &types.NamespacedName{Namespace: "ns", Name: "deleted_pod"},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "deleted_pod", "").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "deleted_pod", "").
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"replacement pods are owning the workload": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement-for-pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement-for-pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement-for-pod1", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "test-group", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "OwnerReferencesAdded",
					Message:   "Added 1 owner reference(s)",
				},
			},
		},
		"all pods in a group should receive the event about preemption, unless already terminating": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusConditions(corev1.PodCondition{
						Type:    "TerminationTarget",
						Status:  corev1.ConditionTrue,
						Reason:  "WorkloadEvictedDueToPreempted",
						Message: "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusConditions(corev1.PodCondition{
						Type:    "TerminationTarget",
						Status:  corev1.ConditionTrue,
						Reason:  "WorkloadEvictedDueToPreempted",
						Message: "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod1", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Preempted to accommodate a higher priority Workload",
				},
				{
					Key:       types.NamespacedName{Name: "pod3", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Preempted to accommodate a higher priority Workload",
				},
			},
		},
		"preemption reason should be propagated to termination target": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusConditions(corev1.PodCondition{
						Type:    "TerminationTarget",
						Status:  corev1.ConditionTrue,
						Reason:  "WorkloadEvictedDueToPodsReadyTimeout",
						Message: "Workload evicted due to a PodsReady timeout",
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "PodsReadyTimeout",
						Message:            "Workload evicted due to a PodsReady timeout",
					}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReadyTimeout",
						Message: "Workload evicted due to a PodsReady timeout",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "PodsReadyTimeout",
						Message:            "Workload evicted due to a PodsReady timeout",
					}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  "PodsReadyTimeout",
						Message: "Workload evicted due to a PodsReady timeout",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod1", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Stopped",
					Message:   "Workload evicted due to a PodsReady timeout",
				},
			},
		},
		"the failed pods are finalized in order": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("active-pod").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("deleted").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					StatusPhase(corev1.PodFailed).
					DeletionTimestamp(now.Add(-4 * time.Minute)).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("active-pod").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("4").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("4").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 4).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "active-pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "deleted", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 4).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "active-pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "deleted", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "test-group", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "OwnerReferencesAdded",
					Message:   "Added 2 owner reference(s)",
				},
			},
		},
		"no failed pods are finalized while waiting for expectations": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("active-pod").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("active-pod").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "active-pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "active-pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			excessPodsExpectations: []keyUIDs{{
				key:  types.NamespacedName{Name: "test-group", Namespace: "ns"},
				uids: []types.UID{"some-other-pod"},
			}},
		},
		"no unnecessary additional failed pods are finalized": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("finished-with-error").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error-no-finalizer").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("finished-with-error").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("finished-with-error-no-finalizer").
					Label(constants.ManagedByKueueLabel, "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					StatusConditions(corev1.PodCondition{
						Type:               corev1.ContainersReady,
						Status:             corev1.ConditionFalse,
						Reason:             string(corev1.PodFailed),
						LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)).Rfc3339Copy(),
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 4).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error-no-finalizer", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet(podUID, 4).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "finished-with-error-no-finalizer", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "test-group", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "OwnerReferencesAdded",
					Message:   "Added 1 owner reference(s)",
				},
			},
		},
		"workload is created with correct labels for a single pod": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				Label("toCopyKey", "toCopyValue").
				Label("dontCopyKey", "dontCopyValue").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj()},
			wantPods: nil,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithLabelKeysToCopy([]string{"toCopyKey", "keyAbsentInThePod"}),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForPod(basePodWrapper.GetName(), basePodWrapper.GetUID()), "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("test-queue").
					Priority(0).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
						"toCopyKey":                  "toCopyValue",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/" + GetWorkloadNameForPod(basePodWrapper.GetName(), basePodWrapper.GetUID()),
				},
			},
		},
		"workload is created with correct labels for pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Label("toCopyKey1", "toCopyValue1").
					Label("dontCopyKey", "dontCopyValue").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupIndex("0").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Label("toCopyKey1", "toCopyValue1").
					Label("toCopyKey2", "toCopyValue2").
					Label("dontCopyKey", "dontCopyValue").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupIndex("1").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: nil,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithLabelKeysToCopy([]string{"toCopyKey1", "toCopyKey2"}),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Annotations(map[string]string{
						"kueue.x-k8s.io/is-group-workload": "true"}).
					Labels(map[string]string{
						"toCopyKey1": "toCopyValue1",
						"toCopyKey2": "toCopyValue2",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "CreatedWorkload",
					Message:   "Created Workload: ns/test-group",
				},
			},
		},
		"reconciler returns error in case pod group pod index is bigger or equal pod group total count": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Group("test-group").
				GroupIndex("1").
				GroupTotalCount("1").
				Obj(),
			},
			wantErr: utilpod.ErrValidation,
		},
		"reconciler returns error in case pod group pod index is less than 0": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Group("test-group").
				GroupIndex("-1").
				GroupTotalCount("1").
				Obj(),
			},
			wantErr: utilpod.ErrInvalidUInt,
		},
		"reconciler returns error in case of label mismatch in pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label(constants.ManagedByKueueLabel, "true").
					Label("toCopyKey1", "toCopyValue1").
					Label("dontCopyKey", "dontCopyValue").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					Label("toCopyKey1", "otherValue").
					Label("toCopyKey2", "toCopyValue2").
					Label("dontCopyKey", "dontCopyValue").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: nil,
			reconcilerOptions: []jobframework.Option{
				jobframework.WithLabelKeysToCopy([]string{"toCopyKey1", "toCopyKey2"}),
			},
			wantWorkloads: nil,
			wantErr:       errPodGroupLabelsMismatch,
		},
		"admission check message is recorded as event for a single pod": {
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label(constants.ManagedByKueueLabel, "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Obj()},
			wantPods: nil,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForPod(basePodWrapper.GetName(), basePodWrapper.GetUID()), "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Active(false).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName1",
						State:   kueue.CheckStatePending,
						Message: "Not admitted.",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName2",
						State:   kueue.CheckStatePending,
						Message: "Test message.",
					}).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(false).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload(GetWorkloadNameForPod(basePodWrapper.GetName(), basePodWrapper.GetUID()), "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Active(false).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName1",
						State:   kueue.CheckStatePending,
						Message: "Not admitted.",
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName2",
						State:   kueue.CheckStatePending,
						Message: "Test message.",
					}).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(false).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod", Namespace: "ns"},
					EventType: "Normal",
					Reason:    jobframework.ReasonUpdatedAdmissionCheck,
					Message:   "acName1: Not admitted.; acName2: Test message.",
				},
			},
		},
		"admission check message is recorded as event for each pod in the group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Active(false).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName",
						State:   kueue.CheckStatePending,
						Message: "Not admitted, ETA: 2024-02-22T10:36:40Z.",
					}).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(false).
					Obj(),
			},
			wantPods: nil,
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					Active(false).
					Condition(metav1.Condition{
						Type:   kueue.WorkloadQuotaReserved,
						Status: metav1.ConditionTrue,
					}).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:    "acName",
						State:   kueue.CheckStatePending,
						Message: "Not admitted, ETA: 2024-02-22T10:36:40Z.",
					}).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(false).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "pod1", Namespace: "ns"},
					EventType: "Normal",
					Reason:    jobframework.ReasonUpdatedAdmissionCheck,
					Message:   "acName: Not admitted, ETA: 2024-02-22T10:36:40Z.",
				},
				{
					Key:       types.NamespacedName{Name: "pod2", Namespace: "ns"},
					EventType: "Normal",
					Reason:    jobframework.ReasonUpdatedAdmissionCheck,
					Message:   "acName: Not admitted, ETA: 2024-02-22T10:36:40Z.",
				},
			},
		},
		"deleted unschedulable pods are finalized": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodRunning).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodRunning).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "replacement", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "Started",
					Message:   "Admitted by clusterQueue cq",
				},
				{
					Key:       types.NamespacedName{Name: "test-group", Namespace: "ns"},
					EventType: "Normal",
					Reason:    "OwnerReferencesAdded",
					Message:   "Added 1 owner reference(s)",
				},
			},
		},
		"shouldn't set waiting for pods ready condition to true when all pods pending": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
			},
		},
		"should set waiting for pods ready condition to true when at least one pod failed": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
		},
		"should set waiting for pods ready condition to true when at least one pod deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					Delete().
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Delete().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
		},
		"should set waiting for pods ready condition to true when workload was evicted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
		},
		"should update reason and message on waiting for pods ready condition when workload was evicted again": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					Delete().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: "Exceeded the PodsReady timeout",
					}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPreemption,
						Message: "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: "Exceeded the PodsReady timeout",
					}).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
						Message: "Exceeded the PodsReady timeout",
					}).
					Obj(),
			},
		},
		"shouldn't change waiting for pods ready condition when it's true and workload was readmitted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodFailed).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Condition(metav1.Condition{
						Type:               WorkloadWaitingForReplacementPods,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
		},
		"should set waiting for pods ready condition to false when pods was replaced": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionTrue,
						Reason:  WorkloadPodsFailed,
						Message: "Some Failed pods need replacement",
					}).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label(constants.ManagedByKueueLabel, "true").
					KueueFinalizer().
					StatusPhase(corev1.PodPending).
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(-time.Hour)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(podUID, 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", podUID).AssignmentPodCount(2).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    WorkloadWaitingForReplacementPods,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadPodsReady,
						Message: "No pods need replacement",
					}).
					Obj(),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}

			kcBuilder := clientBuilder.WithObjects(tc.initObjects...)
			for i := range tc.pods {
				kcBuilder = kcBuilder.WithObjects(&tc.pods[i])
			}

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}

				if tc.deleteWorkloads {
					if err := kClient.Delete(ctx, &tc.workloads[i]); err != nil {
						t.Fatalf("Could not delete workload: %v", err)
					}
				}
			}
			recorder := &utiltesting.EventRecorder{}
			reconciler := NewReconciler(kClient, recorder, append(tc.reconcilerOptions, jobframework.WithClock(t, fakeClock))...)
			pReconciler := reconciler.(*Reconciler)
			for _, e := range tc.excessPodsExpectations {
				pReconciler.expectationsStore.ExpectUIDs(log, e.key, e.uids)
			}

			var reconcileRequest reconcile.Request
			if tc.reconcileKey != nil {
				reconcileRequest.NamespacedName = *tc.reconcileKey
			} else {
				reconcileRequest = reconcileRequestForPod(&tc.pods[0])
			}
			_, err := reconciler.Reconcile(ctx, reconcileRequest)

			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotPods corev1.PodList
			if err := kClient.List(ctx, &gotPods); err != nil {
				if tc.wantPods != nil || !apierrors.IsNotFound(err) {
					t.Fatalf("Could not get Pod after reconcile: %v", err)
				}
			}
			if tc.wantPods != nil {
				if diff := cmp.Diff(tc.wantPods, gotPods.Items, podCmpOpts...); diff != "" && tc.wantPods != nil {
					t.Errorf("Pods after reconcile (-want,+got):\n%s", diff)
				}
			}

			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, tc.workloadCmpOpts...); diff != "" {
				for i, p := range tc.pods {
					// Make life easier when changing the hashing function.
					hash, _ := getRoleHash(p)
					t.Logf("note, the hash for pod[%v] = %s", i, hash)
				}
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestReconciler_ErrorFinalizingPod(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	clientBuilder := utiltesting.NewClientBuilder()
	if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
		t.Fatalf("Could not setup indexes: %v", err)
	}

	basePodWrapper := testingpod.MakePod("pod", "ns").
		UID("test-uid").
		Queue("user-queue").
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	pod := *basePodWrapper.
		Clone().
		Label(constants.ManagedByKueueLabel, "true").
		KueueFinalizer().
		StatusPhase(corev1.PodSucceeded).
		StatusMessage("Job finished successfully").
		Obj()

	wl := *utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
		Admitted(true).
		Obj()

	reqcount := 0
	errMock := fmt.Errorf("connection refused: %w", syscall.ECONNREFUSED)

	kcBuilder := clientBuilder.
		WithObjects(&pod).
		WithStatusSubresource(&wl).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, client client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				_, isPod := obj.(*corev1.Pod)
				if isPod {
					defer func() { reqcount++ }()
					if reqcount == 0 {
						// return a connection refused error for the first update request.
						return errMock
					}
					if reqcount == 1 {
						// Exec a regular update operation for the second request
						return client.Patch(ctx, obj, patch, opts...)
					}
				}
				return client.Patch(ctx, obj, patch, opts...)
			},
			SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge,
		})

	kClient := kcBuilder.Build()
	if err := ctrl.SetControllerReference(&pod, &wl, kClient.Scheme()); err != nil {
		t.Fatalf("Could not setup owner reference in Workloads: %v", err)
	}
	if err := kClient.Create(ctx, &wl); err != nil {
		t.Fatalf("Could not create workload: %v", err)
	}
	recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})

	reconciler := NewReconciler(kClient, recorder)

	podKey := client.ObjectKeyFromObject(&pod)
	_, err := reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: podKey,
	})

	if diff := cmp.Diff(errMock, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Expected reconcile error (-want,+got):\n%s", diff)
	}

	// Reconcile for the second time
	_, err = reconciler.Reconcile(ctx, reconcile.Request{
		NamespacedName: podKey,
	})
	if err != nil {
		t.Errorf("Got unexpected error while running reconcile:\n%v", err)
	}

	var gotPod corev1.Pod
	if err := kClient.Get(ctx, podKey, &gotPod); err != nil {
		t.Fatalf("Could not get Pod after second reconcile: %v", err)
	}
	// Validate that pod has no finalizer after the second reconcile
	wantPod := *basePodWrapper.
		Clone().
		Label(constants.ManagedByKueueLabel, "true").
		StatusPhase(corev1.PodSucceeded).
		StatusMessage("Job finished successfully").
		Obj()
	if diff := cmp.Diff(wantPod, gotPod, podCmpOpts...); diff != "" {
		t.Errorf("Pod after second reconcile (-want,+got):\n%s", diff)
	}

	var gotWorkloads kueue.WorkloadList
	if err := kClient.List(ctx, &gotWorkloads); err != nil {
		t.Fatalf("Could not get Workloads after second reconcile: %v", err)
	}

	// Workload should be finished after the second reconcile
	wantWl := *utiltesting.MakeWorkload("unit-test", "ns").
		PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
		ControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
		Admitted(true).
		Condition(
			metav1.Condition{
				Type:    kueue.WorkloadFinished,
				Status:  metav1.ConditionTrue,
				Reason:  kueue.WorkloadFinishedReasonSucceeded,
				Message: "Job finished successfully",
			},
		).
		Obj()
	if diff := cmp.Diff([]kueue.Workload{wantWl}, gotWorkloads.Items, defaultWorkloadCmpOpts...); diff != "" {
		t.Errorf("Workloads after second reconcile (-want,+got):\n%s", diff)
	}
}

func TestIsPodOwnerManagedByQueue(t *testing.T) {
	t.Cleanup(jobframework.EnableIntegrationsForTest(t, "batch/job", "ray.io/raycluster"))
	testCases := map[string]struct {
		ownerReference metav1.OwnerReference
		wantRes        bool
	}{
		"batch/v1/Job": {
			ownerReference: metav1.OwnerReference{
				APIVersion: "batch/v1",
				Controller: ptr.To(true),
				Kind:       "Job",
			},
			wantRes: true,
		},
		"apps/v1/ReplicaSet": {
			ownerReference: metav1.OwnerReference{
				APIVersion: "apps/v1",
				Controller: ptr.To(true),
				Kind:       "ReplicaSet",
			},
			wantRes: false,
		},
		"ray.io/v1/RayCluster": {
			ownerReference: metav1.OwnerReference{
				APIVersion: "ray.io/v1",
				Controller: ptr.To(true),
				Kind:       "RayCluster",
			},
			wantRes: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			pod := testingpod.MakePod("pod", "ns").
				UID("test-uid").
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj()

			pod.OwnerReferences = append(pod.OwnerReferences, tc.ownerReference)

			if tc.wantRes != IsPodOwnerManagedByKueue(FromObject(pod)) {
				t.Errorf("Unexpected 'IsPodOwnerManagedByKueue' result\n want: %t\n got: %t)",
					tc.wantRes, IsPodOwnerManagedByKueue(FromObject(pod)))
			}
		})
	}
}

func TestGetWorkloadNameForPod(t *testing.T) {
	wantWlNameStart := "pod-unit-test-"
	wlName1 := GetWorkloadNameForPod("unit-test", "test-uid")
	if strings.Index(wlName1, wantWlNameStart) != 0 {
		t.Fatalf("Expecting %q to start with %q", wlName1, wantWlNameStart)
	}

	// The same name is generated for with the same input.
	wlName2 := GetWorkloadNameForPod("unit-test", "test-uid")
	if wlName2 != wlName1 {
		t.Fatalf("Expecting %q to be equal to %q", wlName2, wlName1)
	}

	// Different suffix is generated with different uid
	wlName3 := GetWorkloadNameForPod("unit-test", "test-uid2")
	if wlName3 == wlName1 {
		t.Fatalf("Expecting %q to be different then %q", wlName3, wlName1)
	}
	if strings.Index(wlName3, wantWlNameStart) != 0 {
		t.Fatalf("Expecting %q to start with %q", wlName3, wantWlNameStart)
	}
}

func TestReconciler_DeletePodAfterTransientErrorsOnUpdateOrDeleteOps(t *testing.T) {
	now := time.Now()
	connRefusedErrMock := fmt.Errorf("connection refused: %w", syscall.ECONNREFUSED)
	ctx, _ := utiltesting.ContextWithLog(t)
	var triggerUpdateErr, triggerDeleteErr bool

	podUID := "dc85db45"

	basePodWrapper := testingpod.MakePod("pod", "ns").
		UID("test-uid").
		Queue("user-queue").
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	pods := []corev1.Pod{
		*basePodWrapper.
			Clone().
			Label(constants.ManagedByKueueLabel, "true").
			KueueFinalizer().
			Group("test-group").
			GroupTotalCount("2").
			CreationTimestamp(now).
			Obj(),
		*basePodWrapper.
			Clone().
			Name("pod2").
			Label(constants.ManagedByKueueLabel, "true").
			KueueFinalizer().
			Group("test-group").
			CreationTimestamp(now.Add(time.Minute)).
			GroupTotalCount("2").
			Obj(),
		*basePodWrapper.
			Clone().
			Name("excessPod").
			Label(constants.ManagedByKueueLabel, "true").
			KueueFinalizer().
			Group("test-group").
			CreationTimestamp(now.Add(time.Minute * 2)).
			GroupTotalCount("2").
			Obj(),
	}

	wl := *utiltesting.MakeWorkload("test-group", "ns").
		PodSets(
			*utiltesting.MakePodSet(podUID, 2).
				Request(corev1.ResourceCPU, "1").
				Obj(),
		).
		Queue("user-queue").
		OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
		OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
		OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "excessPod", "test-uid").
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
		Admitted(true).
		Obj()

	clientBuilder := utiltesting.NewClientBuilder()
	if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
		t.Fatalf("Could not setup indexes: %v", err)
	}

	for i := range pods {
		clientBuilder = clientBuilder.WithObjects(&pods[i])
	}

	kcBuilder := clientBuilder.
		WithStatusSubresource(&wl).
		WithInterceptorFuncs(interceptor.Funcs{
			Patch: func(ctx context.Context, client client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
				if triggerUpdateErr {
					return connRefusedErrMock
				}
				return client.Patch(ctx, obj, patch, opts...)
			},
			Delete: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.DeleteOption) error {
				if triggerDeleteErr {
					return connRefusedErrMock
				}
				return client.Delete(ctx, obj, opts...)
			},
		})

	kClient := kcBuilder.Build()
	if err := kClient.Create(ctx, &wl); err != nil {
		t.Fatalf("Could not create workload: %v", err)
	}

	recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
	reconciler := NewReconciler(kClient, recorder)
	reconcileRequest := reconcileRequestForPod(&pods[0])

	// Reconcile for the first time. It'll try  to remove the finalizers but fail
	triggerUpdateErr = true
	_, err := reconciler.Reconcile(ctx, reconcileRequest)
	if diff := cmp.Diff(connRefusedErrMock, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Expected reconcile error (-want,+got):\n%s", diff)
	}

	// Reconcile for the second time to remove the finalizers. Then it should attempt to delete the pod but fail
	triggerUpdateErr = false
	triggerDeleteErr = true
	_, err = reconciler.Reconcile(ctx, reconcileRequest)
	if diff := cmp.Diff(connRefusedErrMock, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("Expected reconcile error (-want,+got):\n%s", diff)
	}

	// Reconcile for the third time and delete the pod
	triggerDeleteErr = false
	_, err = reconciler.Reconcile(ctx, reconcileRequest)
	if err != nil {
		t.Errorf("Got unexpected error while running reconcile:\n%v", err)
	}

	// Verify that the pod has been indeed deleted
	var gotPod corev1.Pod
	podKey := client.ObjectKeyFromObject(&pods[2])
	err = kClient.Get(ctx, podKey, &gotPod)
	if err == nil {
		t.Fatalf("Expected pod %q to be deleted", podKey.String())
	} else if !apierrors.IsNotFound(err) {
		t.Fatalf("Got unexpected error %v when checking if pod %q was deleted", err, podKey.String())
	}
}
