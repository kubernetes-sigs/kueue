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
	"fmt"
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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
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
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.HeadGroupSpec.Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[0].Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
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
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
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
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.HeadGroupSpec.Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
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
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
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
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.HeadGroupSpec.Template.Annotations).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[0].Template.Annotations).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[1].Template.Annotations).
						Obj(),
					*utiltestingapi.MakePodSet("group3", 3).
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
			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.rayCluster.PodSets(ctx)
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
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	const (
		localQueueName   = "foo"
		clusterQueueName = "cq"
	)

	baseJobWrapper := testingrayutil.MakeCluster("job", "ns").
		Suspend(true).
		Queue(localQueueName).
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
				utiltestingapi.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				NodeLabel(rayv1.HeadNode, constants.PodSetLabel, "head").
				NodeLabel(rayv1.WorkerNode, constants.PodSetLabel, "workers-group-0").
				NodeAnnotation(rayv1.HeadNode, kueue.WorkloadAnnotation, "test").
				NodeAnnotation(rayv1.WorkerNode, kueue.WorkloadAnnotation, "test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltestingapi.MakePodSet("workers-group-0", 1).
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
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission(clusterQueueName).
							PodSets(
								utiltestingapi.MakePodSetAssignment("head").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									Obj(),
								utiltestingapi.MakePodSetAssignment("workers-group-0").
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
				*utiltestingapi.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Labels(map[string]string{constants.PodSetLabel: "head"}).
							Obj(),
						*utiltestingapi.MakePodSet("workers-group-0", 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("worker-container").
										Obj(),
								},
							}).
							Labels(map[string]string{constants.PodSetLabel: "workers-group-0"}).
							Obj(),
					).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission(clusterQueueName).
							PodSets(
								utiltestingapi.MakePodSetAssignment("head").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									Obj(),
								utiltestingapi.MakePodSetAssignment("workers-group-0").
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
				utiltestingapi.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(true).
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltestingapi.MakePodSet("workers-group-0", 1).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(clusterQueueName).PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), utiltestingapi.MakePodSetAssignment("workers-group-0").Obj()).Obj(), now).
					Generation(1).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadDeactivated,
						Message:            "The workload was deactivated",
						ObservedGeneration: 1,
					}).
					AdmittedAt(true, now.Add(-time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("a", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltestingapi.MakePodSet("workers-group-0", 1).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(clusterQueueName).PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), utiltestingapi.MakePodSetAssignment("workers-group-0").Obj()).Obj(), now).
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
					AdmittedAt(true, now).
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
				utiltestingapi.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.Clone().
				WithNumOfHosts("workers-group-0", 2).
				Obj(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				WithNumOfHosts("workers-group-0", 2).
				NodeLabel(rayv1.HeadNode, constants.PodSetLabel, "head").
				NodeLabel(rayv1.WorkerNode, constants.PodSetLabel, "workers-group-0").
				NodeAnnotation(rayv1.HeadNode, kueue.WorkloadAnnotation, "test").
				NodeAnnotation(rayv1.WorkerNode, kueue.WorkloadAnnotation, "test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("test", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltestingapi.MakePodSet("workers-group-0", 2).
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
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission(clusterQueueName).
							PodSets(
								utiltestingapi.MakePodSetAssignment("head").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									Count(2).
									Obj(),
								utiltestingapi.MakePodSetAssignment("workers-group-0").
									Count(2).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
				*utiltestingapi.MakeWorkload("a", "ns").
					Finalizers(kueue.ResourceInUseFinalizerName).
					PodSets(
						*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
							PodSpec(corev1.PodSpec{
								RestartPolicy: corev1.RestartPolicyNever,
								Containers: []corev1.Container{
									*utiltesting.MakeContainer().
										Name("head-container").
										Obj(),
								},
							}).
							Obj(),
						*utiltestingapi.MakePodSet("workers-group-0", 2).
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
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission(clusterQueueName).
							PodSets(
								utiltestingapi.MakePodSetAssignment("head").
									Assignment(corev1.ResourceCPU, "unit-test-flavor", "1").
									Count(2).
									Obj(),
								utiltestingapi.MakePodSetAssignment("workers-group-0").
									Count(2).
									Obj(),
							).
							Obj(), now,
					).
					AdmittedAt(true, now).
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
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				ctx, _ := utiltesting.ContextWithLog(t)
				clientBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
				indexer := utiltesting.AsIndexer(clientBuilder)

				if err := SetupIndexes(ctx, indexer); err != nil {
					t.Fatalf("Could not setup indexes: %v", err)
				}
				// Add namespace to prevent early return when ManagedJobsNamespaceSelectorAlwaysRespected is enabled
				namespace := utiltesting.MakeNamespace("ns")
				objs := append(tc.priorityClasses, &tc.job)
				kcBuilder := clientBuilder.WithObjects(namespace).WithObjects(objs...)

				for i := range tc.workloads {
					kcBuilder = kcBuilder.WithStatusSubresource(&tc.workloads[i])
				}

				kcBuilder = clientBuilder.WithObjects(tc.initObjects...)

				kClient := kcBuilder.Build()
				for _, testWl := range tc.workloads {
					if err := ctrl.SetControllerReference(&tc.job, &testWl, kClient.Scheme()); err != nil {
						t.Fatalf("Could not setup owner reference in Workloads: %v", err)
					}
					if err := kClient.Create(ctx, &testWl); err != nil {
						t.Fatalf("Could not create workload: %v", err)
					}
				}
				recorder := record.NewBroadcaster().NewRecorder(kClient.Scheme(), corev1.EventSource{Component: "test"})
				reconciler, err := NewReconciler(ctx, kClient, indexer, recorder, append(tc.reconcilerOptions, jobframework.WithClock(fakeClock))...)
				if err != nil {
					t.Errorf("Error creating the reconciler: %v", err)
				}

				jobKey := client.ObjectKeyFromObject(&tc.job)
				_, err = reconciler.Reconcile(ctx, reconcile.Request{
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
				// The fake client with patch.Apply cannot reset the Admission field (patch.Merge can).
				// However, other important Status fields (e.g. Conditions) still reflect the change,
				// so we deliberately ignore the Admission field here.
				wlCheckOpts := workloadCmpOpts
				if features.Enabled(features.WorkloadRequestUseMergePatch) {
					wlCheckOpts = append(wlCheckOpts, cmpopts.IgnoreFields(kueue.WorkloadStatus{}, "Admission"))
				}

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, wlCheckOpts...); diff != "" {
					t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
				}
			})
		}
	}
}
