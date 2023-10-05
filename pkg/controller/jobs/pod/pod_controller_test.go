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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

func TestRunWithPodSetsInfo(t *testing.T) {
	testCases := map[string]struct {
		pod                  *corev1.Pod
		runInfo, restoreInfo []jobframework.PodSetInfo
		wantPod              *corev1.Pod
		wantErr              error
	}{
		"pod set info > 1": {
			pod:     testingpod.MakePod("test-pod", "test-namespace").Obj(),
			runInfo: make([]jobframework.PodSetInfo, 2),
			wantErr: jobframework.ErrInvalidPodsetInfo,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			pod := fromObject(tc.pod)

			gotErr := pod.RunWithPodSetsInfo(tc.runInfo)

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("error mismatch (-want +got):\n%s", diff)
			}
			if tc.wantErr == nil {
				if diff := cmp.Diff(tc.wantPod.Spec, tc.pod.Spec); diff != "" {
					t.Errorf("pod spec mismatch (-want +got):\n%s", diff)
				}
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
			kueue.Workload{}, "TypeMeta", "ObjectMeta.OwnerReferences",
			"ObjectMeta.Name", "ObjectMeta.ResourceVersion",
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

	testCases := map[string]struct {
		initObjects     []client.Object
		pod             corev1.Pod
		wantPod         *corev1.Pod
		workloads       []kueue.Workload
		wantWorkloads   []kueue.Workload
		wantErr         error
		workloadCmpOpts []cmp.Option
	}{
		"scheduling gate is removed and node selector is added if workload is admitted": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").Label("kubernetes.io/arch", "arm64").Obj(),
			},
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				NodeSelector("kubernetes.io/arch", "arm64").
				KueueFinalizer().
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
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
		},
		"non-matching admitted workload is deleted and pod is finalized": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Obj(),
			wantPod: nil,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 2).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantErr:         jobframework.ErrNoMatchingWorkloads,
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"the workload is created when queue name is set": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj(),
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							SchedulingGates(corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"}).
							Obj(),
					).
					Queue("test-queue").
					Priority(0).
					Labels(map[string]string{
						controllerconsts.JobUIDLabel: "test-uid",
					}).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"the pod reconciliation is skipped when 'kueue.x-k8s.io/managed' label is not set": {
			pod: *basePodWrapper.
				Clone().
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Obj(),
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"pod is stopped when workload is evicted": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  corev1.ConditionTrue,
					Reason:  "StoppedByKueue",
					Message: "Preempted to accommodate a higher priority Workload",
				}).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("job", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Queue("test-queue").
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
		"pod is finalized when it's succeeded": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
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
		},
		"workload status condition is added even if the pod is finalized": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
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
		},
		"pod without scheduling gate is terminated if workload is not admitted": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  corev1.ConditionTrue,
					Reason:  "StoppedByKueue",
					Message: "Not admitted by cluster queue",
				}).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"finalizer is removed for finished pod without matching workload": {
			pod: *basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPod: basePodWrapper.
				Clone().
				Label("kueue.x-k8s.io/managed", "true").
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			workloads: []kueue.Workload{},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
					Labels(map[string]string{"kueue.x-k8s.io/job-uid": "test-uid"}).
					Queue("user-queue").
					Priority(0).
					Obj(),
			},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			kcBuilder := clientBuilder.WithObjects(append(tc.initObjects, &tc.pod)...)

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kClient := kcBuilder.Build()
			for i := range tc.workloads {
				if err := ctrl.SetControllerReference(&tc.pod, &tc.workloads[i], kClient.Scheme()); err != nil {
					t.Fatalf("Could not setup owner reference in Workloads: %v", err)
				}
				if err := kClient.Create(ctx, &tc.workloads[i]); err != nil {
					t.Fatalf("Could not create workload: %v", err)
				}
			}
			recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
			reconciler := NewReconciler(kClient, recorder)

			podKey := client.ObjectKeyFromObject(&tc.pod)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: podKey,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Reconcile returned error (-want,+got):\n%s", diff)
			}

			var gotPod corev1.Pod
			if err := kClient.Get(ctx, podKey, &gotPod); err != nil {
				if tc.wantPod != nil || !apierrors.IsNotFound(err) {
					t.Fatalf("Could not get Pod after reconcile: %v", err)
				}
			}
			if tc.wantPod != nil {
				if diff := cmp.Diff(*tc.wantPod, gotPod, podCmpOpts...); diff != "" && tc.wantPod != nil {
					t.Errorf("Pod after reconcile (-want,+got):\n%s", diff)
				}
			}
			var gotWorkloads kueue.WorkloadList
			if err := kClient.List(ctx, &gotWorkloads); err != nil {
				t.Fatalf("Could not get Workloads after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, tc.workloadCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
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
				gvk := obj.GetObjectKind().GroupVersionKind()
				if gvk.GroupVersion().String() == "v1" && gvk.Kind == "Pod" {
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
	wantWl := *utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
		PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj()).
		ReserveQuota(utiltesting.MakeAdmission("cq").AssignmentPodCount(1).Obj()).
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
