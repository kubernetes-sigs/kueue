package pod

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
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
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestPodsReady(t *testing.T) {
	testCases := map[string]struct {
		pod  *corev1.Pod
		want bool
	}{
		"pod is ready": {
			pod: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				StatusConditions(
					corev1.PodCondition{
						Type:   "Ready",
						Status: "True",
					},
				).
				Obj(),
			want: true,
		},
		"pod is not ready": {
			pod: testingutil.MakePod("test-pod", "test-ns").
				Queue("test-queue").
				StatusConditions().
				Obj(),
			want: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			pod := (*Pod)(tc.pod)
			got := pod.PodsReady()
			if tc.want != got {
				t.Errorf("Unexpected response (want: %v, got: %v)", tc.want, got)
			}
		})
	}
}

func TestRunWithPodSetsInfo(t *testing.T) {
	testCases := map[string]struct {
		pod                  *Pod
		runInfo, restoreInfo []jobframework.PodSetInfo
		wantPod              *corev1.Pod
		wantErr              error
	}{
		"pod set info > 1": {
			pod:     (*Pod)(testingutil.MakePod("test-pod", "test-namespace").Obj()),
			runInfo: make([]jobframework.PodSetInfo, 2),
			wantErr: fmt.Errorf("invalid podset infos: expecting 1 got 2"),
		},
		"pod with scheduling gate and empty node selector": {
			pod: (*Pod)(testingutil.MakePod("test-pod", "test-namespace").
				KueueSchedulingGate().
				Obj()),
			runInfo: []jobframework.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"test-key": "test-val",
					},
				},
			},
			wantPod: testingutil.MakePod("test-pod", "test-namespace").
				NodeSelector("test-key", "test-val").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := tc.pod.RunWithPodSetsInfo(tc.runInfo)

			if tc.wantErr != nil {
				if diff := cmp.Diff(tc.wantErr.Error(), gotErr.Error()); diff != "" {
					t.Errorf("error mismatch mismatch (-want +got):\n%s", diff)
				}
			} else {
				if gotErr != nil {
					t.Fatalf("unexpected error: %s", gotErr)
				}
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
	basePodWrapper := testingutil.MakePod("pod", "ns").
		UID("test-uid").
		Queue("user-queue").
		Request(corev1.ResourceCPU, "1").
		Image("", nil)

	testCases := map[string]struct {
		initObjects     []client.Object
		pod             corev1.Pod
		wantPod         corev1.Pod
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
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
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
		"non-matching admitted workload is deleted": {
			pod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  "True",
					Reason:  "StoppedByKueue",
					Message: "No matching Workload",
				}).
				Obj(),
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
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
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
			wantPod: *basePodWrapper.
				Clone().
				Obj(),
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"the pod reconciliation is skipped when it's owner is managed by kueue (Job)": {
			pod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-job", batchv1.SchemeGroupVersion.WithKind("Job")).
				Obj(),
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"the pod reconciliation is skipped when it's owner is managed by kueue (RayCluster)": {
			pod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-ray-cluster", rayjobapi.GroupVersion.WithKind("RayCluster")).
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				OwnerReference("parent-ray-cluster", rayjobapi.GroupVersion.WithKind("RayCluster")).
				Obj(),
			wantWorkloads:   []kueue.Workload{},
			workloadCmpOpts: defaultWorkloadCmpOpts,
		},
		"pod is stopped when workload is evicted": {
			pod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				KueueSchedulingGate().
				Queue("test-queue").
				StatusConditions(corev1.PodCondition{
					Type:    "TerminationTarget",
					Status:  "True",
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
				SetLabel("kueue.x-k8s.io/managed", "true").
				KueueFinalizer().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantPod: *basePodWrapper.
				Clone().
				SetLabel("kueue.x-k8s.io/managed", "true").
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
				t.Fatalf("Could not get Pod after reconcile: %v", err)
			}
			if diff := cmp.Diff(tc.wantPod, gotPod, podCmpOpts...); diff != "" {
				t.Errorf("Pod after reconcile (-want,+got):\n%s", diff)
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
			pod := testingutil.MakePod("pod", "ns").
				UID("test-uid").
				Request(corev1.ResourceCPU, "1").
				Image("", nil).
				Obj()

			pod.OwnerReferences = append(pod.OwnerReferences, tc.ownerReference)

			if diff := cmp.Diff(tc.wantRes, IsPodOwnerManagedByQueue((*Pod)(pod))); diff != "" {
				t.Errorf("Unexpected 'IsPodOwnerManagedByQueue' result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestGetWorkloadNameForPod(t *testing.T) {
	wlName := GetWorkloadNameForPod("unit-test")

	if diff := cmp.Diff("pod-unit-test-7bb47", wlName); diff != "" {
		t.Errorf("Expected different workload name (-want,+got):\n%s", diff)
	}
}
