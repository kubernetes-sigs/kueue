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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

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
			pod := fromObject(tc.pod)
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
			pod := fromObject(&tc.pods[0])

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
	basePodWrapper := testingpod.MakePod("pod", "ns").
		UID("test-uid").
		Queue("user-queue").
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	now := time.Now()

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

		wantEvents []utiltesting.EventRecord
	}{
		"scheduling gate is removed and node selector is added if workload is admitted": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").Label("kubernetes.io/arch", "arm64").Obj(),
			},
			pods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				NodeSelector("kubernetes.io/arch", "arm64").
				KueueFinalizer().
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
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
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
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
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Obj()},
			wantPods: nil,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
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
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj()},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("pod-pod-a91e8", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("test-queue").
					Priority(0).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
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
					Message:   "Created Workload: ns/pod-pod-a91e8",
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
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Queue("test-queue").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Queue("test-queue").
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  corev1.ConditionTrue,
					Reason:  "StoppedByKueue",
					Message: "Preempted to accommodate a higher priority Workload",
				}).
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
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
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  "JobFinished",
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
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  "JobFinished",
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
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  corev1.ConditionTrue,
					Reason:  "StoppedByKueue",
					Message: "Not admitted by cluster queue",
				}).
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
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
		"workload is composed and created for the pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
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
		"workload is found for the pod group": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Queue("user-queue").
					Priority(0).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Queue("user-queue").
					Priority(0).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"scheduling gate is removed for all pods in the group if workload is admitted": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").Label("kubernetes.io/arch", "arm64").Obj(),
			},
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Annotation("kueue.x-k8s.io/role-hash", "b990493b").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Annotation("kueue.x-k8s.io/role-hash", "b990493b").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Annotation("kueue.x-k8s.io/role-hash", "b990493b").
					NodeSelector("kubernetes.io/arch", "arm64").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Annotation("kueue.x-k8s.io/role-hash", "b990493b").
					NodeSelector("kubernetes.io/arch", "arm64").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("b990493b", 2).Request(corev1.ResourceCPU, "1").Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "b990493b").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "2").
							AssignmentPodCount(2).
							Obj(),
					).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("b990493b", 2).Request(corev1.ResourceCPU, "1").Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "b990493b").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},

			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Admitted(true).
					ReclaimablePods(kueue.ReclaimablePod{Name: "b990493b", Count: 1}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"workload is finished if all pods in the group has finished": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  "JobFinished",
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
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Group("test-group").
				GroupTotalCount("2").
				Obj()},
			wantPods: []corev1.Pod{*basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Group("test-group").
				GroupTotalCount("2").
				Obj()},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"pod group remains stopped when workload is evicted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("b990493b", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet("b990493b", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"workload is not deleted if one of the pods in the finished group is absent": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},

			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  "JobFinished",
						Message: "Pods succeeded: 1/2. Pods failed: 1/2",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  "JobFinished",
						Message: "Pods succeeded: 1/2. Pods failed: 1/2",
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					PodSets(*utiltesting.MakePodSet("b990493b", 2).Request(corev1.ResourceCPU, "1").Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", "b990493b").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq", "b990493b").AssignmentPodCount(1).Obj()).
					Admitted(true).
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq", "b990493b").AssignmentPodCount(3).Obj()).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReclaimablePods(kueue.ReclaimablePod{Name: "b990493b", Count: 1}).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq", "b990493b").AssignmentPodCount(3).Obj()).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  "b990493b",
						Count: 1,
					}).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement", "test-uid").
					ReclaimablePods(kueue.ReclaimablePod{Name: "b990493b", Count: 1}).
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:    "Finished",
						Status:  "True",
						Reason:  "JobFinished",
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
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
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
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
						Reason:             "NoReservation",
						Message:            "The workload has no reservation",
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					SetOrReplaceCondition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						LastTransitionTime: metav1.Now(),
						Reason:             "Pending",
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Queue("test-queue").
					Group("test-group").
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
					PodSets(*utiltesting.MakePodSet("b990493b", 2).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
					Priority(0).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"workload is not deleted if one pod role is absent from the cluster": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Delete().
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("absent-pod-role", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"if pod group is finished and wl is deleted, new workload shouldn't be created": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 2).
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Message:   "'test-group' group total count is less than the actual number of pods in the cluster",
				},
			},
		},
		"pod group is considered finished if there is an unretriable pod and no running pods": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Annotation("kueue.x-k8s.io/retriable-in-group", "false").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					Image("test-image-role2", nil).
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Annotation("kueue.x-k8s.io/retriable-in-group", "false").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					Image("test-image-role2", nil).
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4389b941", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4389b941", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Condition(
						metav1.Condition{
							Type:    kueue.WorkloadFinished,
							Status:  metav1.ConditionTrue,
							Reason:  "JobFinished",
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					Image("test-image-role2", nil).
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					Image("test-image-role2", nil).
					GroupTotalCount("3").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4389b941", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("4389b941", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReclaimablePods(kueue.ReclaimablePod{Name: "4389b941", Count: 1}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"excess pods before wl creation, youngest pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
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
		"excess pods before admission, youngest pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now.Add(time.Minute)).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Queue("user-queue").
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		// In this case, group-total-count is equal to the number of pods in the cluster.
		// But one of the roles is missing, and another role has an excess pod.
		"excess pods in pod set after admission, youngest pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("aaf269e6", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
			excessPodsExpectations: []keyUIDs{{
				key:  types.NamespacedName{Name: "another-group", Namespace: "ns"},
				uids: []types.UID{"pod"},
			}},
		},
		"waiting to observe previous deletion of excess pod, no pods are deleted": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("1").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("1").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
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
						*utiltesting.MakePodSet("b990493b", 1).
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					UID("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					CreationTimestamp(now.Add(time.Minute)).
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "pod2").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "pod2").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		// If an excess pod is already deleted and finalized, but an external finalizer blocks
		// pod deletion, kueue should ignore such a pod, when creating a workload.
		"deletion of excess pod is blocked by another controller": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					KueueSchedulingGate().
					Group("test-group").
					GroupTotalCount("1").
					CreationTimestamp(now).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
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
						*utiltesting.MakePodSet("b990493b", 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("user-queue").
					Priority(0).
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
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
					Label("kueue.x-k8s.io/managed", "true").
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
					Label("kueue.x-k8s.io/managed", "true").
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
					Message:   "'group' group total count is less than the actual number of pods in the cluster",
				},
			},
		},
		"finalize workload for non existent pod": {
			reconcileKey: &types.NamespacedName{Namespace: "ns", Name: "deleted_pod"},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "deleted_pod", "").
					Finalizers(kueue.ResourceInUseFinalizerName).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "deleted_pod", "").
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"replacement pods are owning the workload": {
			pods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement-for-pod1").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label("kueue.x-k8s.io/managed", "true").
					Group("test-group").
					GroupTotalCount("2").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("replacement-for-pod1").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("2").
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "replacement-for-pod1", "test-uid").
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
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
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
			},
			wantPods: []corev1.Pod{
				*basePodWrapper.
					Clone().
					Name("pod1").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusConditions(corev1.PodCondition{
						Type:    "TerminationTarget",
						Status:  corev1.ConditionTrue,
						Reason:  "StoppedByKueue",
						Message: "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod2").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Delete().
					Group("test-group").
					GroupTotalCount("3").
					Obj(),
				*basePodWrapper.
					Clone().
					Name("pod3").
					Label("kueue.x-k8s.io/managed", "true").
					KueueFinalizer().
					Group("test-group").
					GroupTotalCount("3").
					StatusConditions(corev1.PodCondition{
						Type:    "TerminationTarget",
						Status:  corev1.ConditionTrue,
						Reason:  "StoppedByKueue",
						Message: "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
						Message:            "Preempted to accommodate a higher priority Workload",
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test-group", "ns").
					PodSets(
						*utiltesting.MakePodSet("b990493b", 3).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Queue("user-queue").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod1", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod2", "test-uid").
					SetOwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod3", "test-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "Preempted",
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
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
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
			reconciler := NewReconciler(kClient, recorder)
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
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmp.Transformer("sortedEvents", utiltesting.SortedEvents)); diff != "" {
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
		Label("kueue.x-k8s.io/managed", "true").
		KueueFinalizer().
		StatusPhase(corev1.PodSucceeded).
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
			Update: func(ctx context.Context, client client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				_, isPod := obj.(*corev1.Pod)
				if isPod {
					defer func() { reqcount++ }()
					if reqcount == 0 {
						// return a connection refused error for the first update request.
						return errMock
					}
					if reqcount == 1 {
						// Exec a regular update operation for the second request
						return client.Update(ctx, obj, opts...)
					}
				}
				return client.Update(ctx, obj, opts...)
			},
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
		Label("kueue.x-k8s.io/managed", "true").
		StatusPhase(corev1.PodSucceeded).
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
		SetControllerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "pod", "test-uid").
		Admitted(true).
		Condition(
			metav1.Condition{
				Type:    kueue.WorkloadFinished,
				Status:  metav1.ConditionTrue,
				Reason:  "JobFinished",
				Message: "Job finished successfully",
			},
		).
		Obj()
	if diff := cmp.Diff([]kueue.Workload{wantWl}, gotWorkloads.Items, defaultWorkloadCmpOpts...); diff != "" {
		t.Errorf("Workloads after second reconcile (-want,+got):\n%s", diff)
	}
}

func TestIsPodOwnerManagedByQueue(t *testing.T) {
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
		"ray.io/v1alpha1/RayCluster": {
			ownerReference: metav1.OwnerReference{
				APIVersion: "ray.io/v1alpha1",
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

			if tc.wantRes != IsPodOwnerManagedByKueue(fromObject(pod)) {
				t.Errorf("Unexpected 'IsPodOwnerManagedByKueue' result\n want: %t\n got: %t)",
					tc.wantRes, IsPodOwnerManagedByKueue(fromObject(pod)))
			}
		})
	}
}

func TestGetWorkloadNameForPod(t *testing.T) {
	wantWlName := "pod-unit-test-7bb47"
	wlName := GetWorkloadNameForPod("unit-test")

	if wantWlName != wlName {
		t.Errorf("Expected different workload name\n want: %s\n got: %s", wantWlName, wlName)
	}
}
