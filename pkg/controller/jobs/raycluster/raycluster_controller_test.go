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

package raycluster

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
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

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		rayCluster                    *RayCluster
		wantPodSets                   func(rayJob *RayCluster) []kueue.PodSet
		enableTopologyAwareScheduling bool
	}{
		"no annotations": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "group1",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group2",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltesting.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltesting.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
		"with required topology annotation": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "group1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group2",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.HeadGroupSpec.Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltesting.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[0].Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltesting.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: true,
		},
		"with preferred topology annotation": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "group1",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group2",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.HeadGroupSpec.Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltesting.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltesting.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[1].Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
				}
			},
			enableTopologyAwareScheduling: true,
		},
		"without required and preferred topology annotation if TAS is disabled": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "group1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group2",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group3",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltesting.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.HeadGroupSpec.Template.Annotations).
						Obj(),
					*utiltesting.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[0].Template.Annotations).
						Obj(),
					*utiltesting.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[1].Template.Annotations).
						Obj(),
					*utiltesting.MakePodSet("group3", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[2].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			gotPodSets, err := tc.rayCluster.PodSets()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets(tc.rayCluster), gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReconciler(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	testStartTime := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(testStartTime)

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
				utiltesting.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				NodeLabel(rayv1.HeadNode, controllerconsts.PodSetLabel, "head").
				NodeLabel(rayv1.WorkerNode, controllerconsts.PodSetLabel, "workers-group-0").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltesting.MakePodSet("workers-group-0", 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										WithResourceReq(corev1.ResourceCPU, "10").
										Obj(),
								},
							}).
							Obj(),
					).
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
						*utiltesting.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Labels(map[string]string{controllerconsts.PodSetLabel: "head"}).
							Obj(),
						*utiltesting.MakePodSet("workers-group-0", 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										Obj(),
								},
							}).
							Labels(map[string]string{controllerconsts.PodSetLabel: "workers-group-0"}).
							Obj(),
					).
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
				utiltesting.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltesting.MakePodSet("workers-group-0", 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										WithResourceReq(corev1.ResourceCPU, "10").
										Obj(),
								},
							}).
							Obj(),
					).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(utiltesting.MakeAdmission("cq", "head", "workers-group-0").AssignmentPodCount(1).Obj()).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadDeactivated,
						Message:            "The workload was deactivated",
						ObservedGeneration: 1,
					}).
					AdmittedAt(true, testStartTime.Add(-time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltesting.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltesting.MakePodSet("workers-group-0", 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										Obj(),
								},
							}).
							Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("cq", "head", "workers-group-0").AssignmentPodCount(1).Obj()).
					Generation(1).
					PastAdmittedTime(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadDeactivated,
						Message:            "The workload was deactivated",
						ObservedGeneration: 1,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "The workload was deactivated",
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
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadDeactivated,
						Message:            "The workload was deactivated",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
		"RayCluster with NumOfHosts > 1": {
			initObjects: []client.Object{
				utiltesting.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				WithNumOfHosts("workers-group-0", 2).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				WithNumOfHosts("workers-group-0", 2).
				NodeLabel(rayv1.HeadNode, controllerconsts.PodSetLabel, "head").
				NodeLabel(rayv1.WorkerNode, controllerconsts.PodSetLabel, "workers-group-0").
				Obj(),
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltesting.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltesting.MakePodSet("workers-group-0", 2).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										WithResourceReq(corev1.ResourceCPU, "10").
										Obj(),
								},
							}).
							Obj(),
					).
					Request(corev1.ResourceCPU, "10").
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "head", "workers-group-0").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(2).
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
						*utiltesting.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltesting.MakePodSet("workers-group-0", 2).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										Obj(),
								},
							}).
							Obj(),
					).
					ReserveQuota(
						utiltesting.MakeAdmission("cq", "head", "workers-group-0").
							Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
							AssignmentPodCount(2).
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
			reconciler := NewReconciler(kClient, recorder, append(tc.reconcilerOptions, jobframework.WithClock(t, fakeClock))...)

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
