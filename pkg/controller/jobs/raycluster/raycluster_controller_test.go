/*
Copyright 2024 The Kubernetes Authors.

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

package raycluster

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
)

var (
	jobCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(rayv1.RayCluster{}, "TypeMeta", "ObjectMeta"),
	}
	workloadCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(kueue.Workload{}, "TypeMeta", "ObjectMeta"),
		cmpopts.IgnoreFields(kueue.WorkloadSpec{}, "Priority"),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.PodSet{}, "Template"),
	}
)

func TestReconciler(t *testing.T) {
	baseJobWrapper := testingrayutil.MakeCluster("job", "ns").
		Suspend(true).
		Queue("foo").
		RequestHead(corev1.ResourceCPU, "10").
		RequestWorkerGroup(corev1.ResourceCPU, "10")

	cases := map[string]struct {
		reconcilerOptions []jobframework.Option
		job               rayv1.RayCluster
		initObjects       []client.Object
		workloads         []kueue.Workload
		priorityClasses   []client.Object
		wantJob           rayv1.RayCluster
		wantWorkloads     []kueue.Workload
		runInfo           []podset.PodSetInfo
		wantErr           error
	}{
		"when workload is admitted, cluster is unsuspended": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").Label("kubernetes.io/arch", "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup("kubernetes.io/arch", "arm64").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name:  "head",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{

									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "head-container",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						},
						kueue.PodSet{
							Name:  "workers-group-0",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,

									Containers: []corev1.Container{
										{
											Name: "worker-container",
											Resources: corev1.ResourceRequirements{
												Requests: corev1.ResourceList{
													corev1.ResourceCPU: resource.MustParse("10"),
												},
											},
										},
									},
								},
							},
						}).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "head", "workers-group-0").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(1).
							Obj(),
					).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "head",
							},
							{
								Name: "workers-group-0",
							},
						},
					}).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name:  "head",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "head-container",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						},
						kueue.PodSet{
							Name:  "workers-group-0",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "worker-container",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						}).
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "head", "workers-group-0").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(1).
							Obj(),
					).
					Admitted(true).
					AdmissionCheck(kueue.AdmissionCheckState{
						Name:  "check",
						State: kueue.CheckStateReady,
						PodSetUpdates: []kueue.PodSetUpdate{
							{
								Name: "head",
							},
							{
								Name: "workers-group-0",
							},
						},
					}).
					Obj(),
			},
		},
		"when workload is admitted but workload's conditions is Evicted, suspend it and restore node selector": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").Label("kubernetes.io/arch", "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup("kubernetes.io/arch", "arm64").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name:  "head",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "head-container",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						},
						kueue.PodSet{
							Name:  "workers-group-0",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "worker-container",
											Resources: corev1.ResourceRequirements{
												Requests: corev1.ResourceList{
													corev1.ResourceCPU: resource.MustParse("10"),
												},
											},
										},
									},
								},
							},
						},
					).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("cq", "head", "workers-group-0").AssignmentPodCount(1).Obj()).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						ObservedGeneration: 1,
					}).
					Admitted(true).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						kueue.PodSet{
							Name:  "head",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "head-container",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						},
						kueue.PodSet{
							Name:  "workers-group-0",
							Count: int32(1),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									RestartPolicy: corev1.RestartPolicyNever,
									Containers: []corev1.Container{
										{
											Name: "worker-container",
											Resources: corev1.ResourceRequirements{
												Requests: make(corev1.ResourceList),
											},
										},
									},
								},
							},
						}).
					ReserveQuota(utiltesting.MakeAdmission("cq", "head", "workers-group-0").AssignmentPodCount(1).Obj()).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						ObservedGeneration: 1,
					}).
					Admitted(true).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             "NoReservation",
						Message:            "The workload has no reservation",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadRequeued,
						Status:             metav1.ConditionTrue,
						Reason:             "Pending",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {

			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

			if err := SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder)); err != nil {
				t.Fatalf("Could not setup indexes: %v", err)
			}
			objs := append(tc.priorityClasses, &tc.job)
			kcBuilder := clientBuilder.WithObjects(objs...)

			for i := range tc.workloads {
				kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
			}

			kcBuilder = clientBuilder.WithObjects(tc.initObjects...)

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

			var gotJob rayv1.RayCluster
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
