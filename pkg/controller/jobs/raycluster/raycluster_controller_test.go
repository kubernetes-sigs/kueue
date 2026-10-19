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
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
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
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
		rayCluster   *RayCluster
		wantPodSets  func(rayJob *RayCluster) []kueue.PodSet
		featureGates map[featuregate.Feature]bool
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
						Replicas:  new(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group2",
						Replicas:  new(int32(3)),
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
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
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
						Replicas:  new(int32(1)),
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
						Replicas:  new(int32(3)),
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
						SubGroupCount(new(int32(1))).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						PodIndexLabel(new(rayutils.RayWorkerReplicaIndexKey)).
						SubGroupCount(new(int32(3))).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
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
						Replicas:  new(int32(1)),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "group2",
						Replicas:  new(int32(3)),
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
						SubGroupCount(new(int32(1))).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[1].Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						PodIndexLabel(new(rayutils.RayWorkerReplicaIndexKey)).
						SubGroupCount(new(int32(3))).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"with multi-host and multi-replica worker group": {
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
						GroupName:  "group1",
						Replicas:   new(int32(2)),
						NumOfHosts: 4,
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
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
					*utiltestingapi.MakePodSet("group1", 8).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[0].Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						PodIndexLabel(new(rayutils.RayHostIndexKey)).
						SubGroupIndexLabel(new(rayutils.RayWorkerReplicaIndexKey)).
						SubGroupCount(new(int32(2))).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"with multi-host and single-replica worker group": {
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
						GroupName:  "group1",
						Replicas:   new(int32(1)),
						NumOfHosts: 4,
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.WorkerGroupSpecs[0].Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						PodIndexLabel(new(rayutils.RayHostIndexKey)).
						SubGroupCount(new(int32(1))).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
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
						Replicas:  new(int32(1)),
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
						Replicas:  new(int32(3)),
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
						Replicas:  new(int32(3)),
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
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"partial preemption opt-in": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(constants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(4, 2, 8).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						SetMinimumCount(2).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false, features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
		},
		"partial preemption opt-in with minReplicas unset": {
			rayCluster: (*RayCluster)(func() *rayv1.RayCluster {
				c := testingrayutil.MakeCluster("raycluster", "ns").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					SetAnnotation(constants.PartialPreemptionAnnotation, "true").
					FirstWorkerGroupReplicas(4, 2, 8).
					Obj()
				c.Spec.WorkerGroupSpecs[0].MinReplicas = nil
				return c
			}()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						SetMinimumCount(0).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false, features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
		},
		"partial preemption opt-in with the PartialPreemption gate disabled": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(constants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(4, 2, 8).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false, features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: false},
		},
		"partial preemption opt-in on a non-elastic cluster": {
			rayCluster: (*RayCluster)(testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation(constants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(4, 2, 8).
				Obj()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false, features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
		},
		"partial preemption opt-in with two worker groups": {
			rayCluster: (*RayCluster)(func() *rayv1.RayCluster {
				c := testingrayutil.MakeCluster("raycluster", "ns").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					SetAnnotation(constants.PartialPreemptionAnnotation, "true").
					FirstWorkerGroupReplicas(4, 2, 8).
					Obj()
				second := *c.Spec.WorkerGroupSpecs[0].DeepCopy()
				second.GroupName = "workers-group-1"
				c.Spec.WorkerGroupSpecs = append(c.Spec.WorkerGroupSpecs, second)
				return c
			}()),
			wantPodSets: func(rayJob *RayCluster) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("workers-group-1", 4).
						PodSpec(*rayJob.Spec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false, features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.rayCluster.PodSets(ctx, nil)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets(tc.rayCluster), gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestApplyReclaimTargets(t *testing.T) {
	wlWithTarget := func(admitted int32, target *int32) *kueue.Workload {
		return utiltestingapi.MakeWorkload("wl", "ns").
			Annotation(constants.PartialPreemptionAnnotation, "true").
			PodSets(
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 4).SetMinimumCount(1).Obj(),
			).
			Admission(&kueue.Admission{PodSetAssignments: []kueue.PodSetAssignment{
				{Name: headGroupPodSetName, Count: new(int32(1))},
				{Name: "workers-group-0", Count: new(admitted), ReclaimTargetCount: target},
			}}).
			Obj()
	}
	testCases := map[string]struct {
		workload     *kueue.Workload
		replicas     int32
		numOfHosts   int32
		autoscaling  bool
		shrunk       string
		wantChanged  bool
		wantReplicas int32
		wantShrunk   string
	}{
		"target below replicas": {
			workload:     wlWithTarget(4, new(int32(2))),
			wantChanged:  true,
			wantReplicas: 2,
			wantShrunk:   "4/2",
		},
		"repeated shrink keeps the first original": {
			workload:     wlWithTarget(3, new(int32(2))),
			replicas:     3,
			shrunk:       "4/3",
			wantChanged:  true,
			wantReplicas: 2,
			wantShrunk:   "4/2",
		},
		"user edit between shrinks becomes the new original": {
			workload:     wlWithTarget(8, new(int32(5))),
			replicas:     8,
			shrunk:       "10/6",
			wantChanged:  true,
			wantReplicas: 5,
			wantShrunk:   "8/5",
		},
		"malformed annotation is replaced": {
			workload:     wlWithTarget(4, new(int32(2))),
			shrunk:       "x/3",
			wantChanged:  true,
			wantReplicas: 2,
			wantShrunk:   "4/2",
		},
		"PodSet name mismatch": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Annotation(constants.PartialPreemptionAnnotation, "true").
				Admission(&kueue.Admission{PodSetAssignments: []kueue.PodSetAssignment{
					{Name: headGroupPodSetName, Count: new(int32(1))},
					{Name: "other-group", Count: new(int32(4)), ReclaimTargetCount: new(int32(2))},
				}}).
				Obj(),
			wantReplicas: 4,
		},
		"target at replicas": {
			workload:     wlWithTarget(4, new(int32(4))),
			wantReplicas: 4,
		},
		"no target": {
			workload:     wlWithTarget(4, nil),
			wantReplicas: 4,
		},
		"partially admitted, no target": {
			workload:     wlWithTarget(3, nil),
			wantChanged:  true,
			wantReplicas: 3,
			wantShrunk:   "4/3",
		},
		"multi-host worker group": {
			workload:     wlWithTarget(4, new(int32(2))),
			numOfHosts:   2,
			wantReplicas: 4,
		},
		"in-tree autoscaling enabled": {
			workload:     wlWithTarget(4, new(int32(2))),
			autoscaling:  true,
			wantReplicas: 4,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			replicas := tc.replicas
			if replicas == 0 {
				replicas = 4
			}
			builder := testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation(constants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(replicas, 1, 8).
				WithNumOfHosts("workers-group-0", tc.numOfHosts)
			if tc.autoscaling {
				builder.WithEnableAutoscaling(new(true))
			}
			if tc.shrunk != "" {
				builder.SetAnnotation(RayClusterShrunkReplicasAnnotation, tc.shrunk)
			}
			rc := (*RayCluster)(builder.Obj())
			if got := rc.ApplyReclaimTargets(tc.workload); got != tc.wantChanged {
				t.Errorf("ApplyReclaimTargets() = %v, want %v", got, tc.wantChanged)
			}
			if got := *rc.Spec.WorkerGroupSpecs[0].Replicas; got != tc.wantReplicas {
				t.Errorf("worker replicas = %d, want %d", got, tc.wantReplicas)
			}
			wantShrunk := tc.wantShrunk
			if !tc.wantChanged {
				wantShrunk = tc.shrunk
			}
			if got := rc.Annotations[RayClusterShrunkReplicasAnnotation]; got != wantShrunk {
				t.Errorf("shrunk annotation = %q, want %q", got, wantShrunk)
			}
		})
	}
}

func TestRestoreShrunkReplicas(t *testing.T) {
	testCases := map[string]struct {
		gateOff      bool
		replicas     int32
		shrunk       string
		wantChanged  bool
		wantReplicas int32
	}{
		"no user edit restores the original": {
			replicas:     2,
			shrunk:       "4/2",
			wantChanged:  true,
			wantReplicas: 4,
		},
		"user edit between shrinks is restored to the new original": {
			replicas:     5,
			shrunk:       "8/5",
			wantChanged:  true,
			wantReplicas: 8,
		},
		"user scale-up while shrunk is kept": {
			replicas:     4,
			shrunk:       "4/2",
			wantChanged:  true,
			wantReplicas: 4,
		},
		"user scale-up above the original while shrunk is kept": {
			replicas:     6,
			shrunk:       "4/2",
			wantChanged:  true,
			wantReplicas: 6,
		},
		"user scale-down while shrunk is kept": {
			replicas:     1,
			shrunk:       "4/2",
			wantChanged:  true,
			wantReplicas: 1,
		},
		"capped at maxReplicas": {
			replicas:     2,
			shrunk:       "10/2",
			wantChanged:  true,
			wantReplicas: 8,
		},
		"negative original is clamped to zero": {
			replicas:     2,
			shrunk:       "-3/2",
			wantChanged:  true,
			wantReplicas: 0,
		},
		"malformed value is dropped": {
			replicas:     2,
			shrunk:       "4",
			wantChanged:  true,
			wantReplicas: 2,
		},
		"out of range value is dropped": {
			replicas:     2,
			shrunk:       "99999999999/2",
			wantChanged:  true,
			wantReplicas: 2,
		},
		"leftover annotation with the gate off": {
			gateOff:      true,
			replicas:     2,
			shrunk:       "4/2",
			wantChanged:  true,
			wantReplicas: 4,
		},
		"no annotation": {
			replicas:     2,
			wantReplicas: 2,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, !tc.gateOff)
			builder := testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation(constants.PartialPreemptionAnnotation, "true").
				FirstWorkerGroupReplicas(tc.replicas, 1, 8)
			if tc.shrunk != "" {
				builder.SetAnnotation(RayClusterShrunkReplicasAnnotation, tc.shrunk)
			}
			rc := (*RayCluster)(builder.Obj())
			if got := rc.RestorePodSetsInfo(t.Context(), make([]podset.PodSetInfo, 2)); got != tc.wantChanged {
				t.Errorf("RestorePodSetsInfo() = %v, want %v", got, tc.wantChanged)
			}
			if got := *rc.Spec.WorkerGroupSpecs[0].Replicas; got != tc.wantReplicas {
				t.Errorf("worker replicas = %d, want %d", got, tc.wantReplicas)
			}
			if _, found := rc.Annotations[RayClusterShrunkReplicasAnnotation]; found {
				t.Errorf("shrunk annotation not removed")
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

	partialPreemptionJobWrapper := baseJobWrapper.Clone().
		Suspend(false).
		SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
		SetAnnotation(constants.PartialPreemptionAnnotation, "true").
		FirstWorkerGroupReplicas(4, 1, 8)
	partialPreemptionWorkload := func(admitted int32, reclaimTargetCount *int32) *kueue.Workload {
		workers := utiltestingapi.MakePodSetAssignment("workers-group-0").Count(admitted).Obj()
		workers.ReclaimTargetCount = reclaimTargetCount
		return utiltestingapi.MakeWorkload("test", "ns").
			Queue(localQueueName).
			Annotation(constants.PartialPreemptionAnnotation, "true").
			Finalizers(kueue.ResourceInUseFinalizerName).
			PodSets(
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 4).SetMinimumCount(1).Obj(),
			).
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
					PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), workers).
					Obj(), now,
			).
			AdmittedAt(true, now).
			Obj()
	}

	cases := map[string]struct {
		featureGates      map[featuregate.Feature]bool
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
		"partial preemption: running cluster is shrunk to the reclaim target": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job:          *partialPreemptionJobWrapper.Clone().Obj(),
			wantJob: *partialPreemptionJobWrapper.Clone().
				FirstWorkerGroupReplicas(2, 1, 8).
				Obj(),
			workloads:     []kueue.Workload{*partialPreemptionWorkload(4, new(int32(2)))},
			wantWorkloads: []kueue.Workload{*partialPreemptionWorkload(4, new(int32(2)))},
		},
		"partial preemption: running cluster is not changed without a reclaim target": {
			featureGates:  map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job:           *partialPreemptionJobWrapper.Clone().Obj(),
			wantJob:       *partialPreemptionJobWrapper.Clone().Obj(),
			workloads:     []kueue.Workload{*partialPreemptionWorkload(4, nil)},
			wantWorkloads: []kueue.Workload{*partialPreemptionWorkload(4, nil)},
		},
		"partial preemption: partially admitted running cluster is shrunk to the admitted count": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job:          *partialPreemptionJobWrapper.Clone().Obj(),
			wantJob: *partialPreemptionJobWrapper.Clone().
				FirstWorkerGroupReplicas(3, 1, 8).
				Obj(),
			workloads:     []kueue.Workload{*partialPreemptionWorkload(3, nil)},
			wantWorkloads: []kueue.Workload{*partialPreemptionWorkload(3, nil)},
		},
		"partial preemption: running cluster is not changed with the PartialPreemption gate disabled": {
			featureGates:  map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: false},
			job:           *partialPreemptionJobWrapper.Clone().Obj(),
			wantJob:       *partialPreemptionJobWrapper.Clone().Obj(),
			workloads:     []kueue.Workload{*partialPreemptionWorkload(4, new(int32(2)))},
			wantWorkloads: []kueue.Workload{*partialPreemptionWorkload(4, new(int32(2)))},
		},
		"partial preemption: evicted shrunk cluster is suspended and its worker replicas are restored to the original": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true, features.PartialPreemption: true},
			job: *partialPreemptionJobWrapper.Clone().
				FirstWorkerGroupReplicas(2, 1, 8).
				SetAnnotation(RayClusterShrunkReplicasAnnotation, "4/2").
				Obj(),
			wantJob: *partialPreemptionJobWrapper.Clone().
				Suspend(true).
				FirstWorkerGroupReplicas(4, 1, 8).
				Obj(),
			workloads: []kueue.Workload{*utiltestingapi.MakeWorkload("test", "ns").
				Queue(localQueueName).
				Annotation(constants.PartialPreemptionAnnotation, "true").
				Finalizers(kueue.ResourceInUseFinalizerName).
				PodSets(
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 2).SetMinimumCount(1).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
						PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), utiltestingapi.MakePodSetAssignment("workers-group-0").Count(2).Obj()).
						Obj(), now,
				).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					Reason:             kueue.WorkloadDeactivated,
					Message:            "The workload was deactivated",
					ObservedGeneration: 1,
				}).
				AdmittedAt(true, now).
				Obj()},
			wantWorkloads: []kueue.Workload{*utiltestingapi.MakeWorkload("test", "ns").
				Queue(localQueueName).
				Annotation(constants.PartialPreemptionAnnotation, "true").
				Finalizers(kueue.ResourceInUseFinalizerName).
				PodSets(
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
					*utiltestingapi.MakePodSet("workers-group-0", 2).SetMinimumCount(1).Obj(),
				).
				ReserveQuotaAt(
					utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
						PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), utiltestingapi.MakePodSetAssignment("workers-group-0").Count(2).Obj()).
						Obj(), now,
				).
				Generation(1).
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
					Reason:             workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
				PastAdmittedTime(0).
				Obj()},
		},
		"when workload is admitted, cluster is unsuspended": {
			initObjects: []client.Object{
				utiltestingapi.MakeResourceFlavor("unit-test-flavor").NodeLabel(corev1.LabelArchStable, "arm64").Obj(),
			},
			job: *baseJobWrapper.DeepCopy(),
			wantJob: *baseJobWrapper.Clone().
				Suspend(false).
				NodeSelectorHeadGroup(corev1.LabelArchStable, "arm64").
				NodeLabel(rayv1.HeadNode, constants.PodSetLabel, "head").
				NodeLabel(rayv1.HeadNode, constants.LocalQueueLabel, localQueueName).
				NodeLabel(rayv1.HeadNode, constants.ClusterQueueLabel, clusterQueueName).
				NodeLabel(rayv1.WorkerNode, constants.PodSetLabel, "workers-group-0").
				NodeLabel(rayv1.WorkerNode, constants.LocalQueueLabel, localQueueName).
				NodeLabel(rayv1.WorkerNode, constants.ClusterQueueLabel, clusterQueueName).
				NodeAnnotation(rayv1.HeadNode, kueue.WorkloadAnnotation, "test").
				NodeAnnotation(rayv1.WorkerNode, kueue.WorkloadAnnotation, "test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("test", "ns").
					Queue(localQueueName).
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
						utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
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
					Queue(localQueueName).
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
						utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), utiltestingapi.MakePodSetAssignment("workers-group-0").Obj()).Obj(), now).
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
					ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).PodSets(utiltestingapi.MakePodSetAssignment("head").Obj(), utiltestingapi.MakePodSetAssignment("workers-group-0").Obj()).Obj(), now).
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
						Reason:             workload.UnadmittedWorkloadReasonWithFallback(kueue.WorkloadQuotaReservedReasonPendingEvaluation, kueue.WorkloadPending), //nolint:staticcheck // SA1019: fallback
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
				NodeLabel(rayv1.HeadNode, constants.LocalQueueLabel, localQueueName).
				NodeLabel(rayv1.HeadNode, constants.ClusterQueueLabel, clusterQueueName).
				NodeLabel(rayv1.WorkerNode, constants.PodSetLabel, "workers-group-0").
				NodeLabel(rayv1.WorkerNode, constants.LocalQueueLabel, localQueueName).
				NodeLabel(rayv1.WorkerNode, constants.ClusterQueueLabel, clusterQueueName).
				NodeAnnotation(rayv1.HeadNode, kueue.WorkloadAnnotation, "test").
				NodeAnnotation(rayv1.WorkerNode, kueue.WorkloadAnnotation, "test").
				Obj(),
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("test", "ns").
					Queue(localQueueName).
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
						utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
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
					Queue(localQueueName).
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
						utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueueName)).
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
				features.SetFeatureGatesDuringTest(t, tc.featureGates)
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				ctx, _ := utiltesting.ContextWithLog(t)
				clientBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
				})
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
				recorder := &utiltesting.EventRecorder{}
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

func TestCustomEquivalenceOptions(t *testing.T) {
	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		wantEmpty    bool
	}{
		"feature gate disabled (default): returns WithIgnoreTopologyIndexLabels": {
			featureGates: map[featuregate.Feature]bool{features.KubeRayEvictOnInconsistentTopologyRequest: false},
			wantEmpty:    false,
		},
		"feature gate enabled: returns nil": {
			featureGates: map[featuregate.Feature]bool{features.KubeRayEvictOnInconsistentTopologyRequest: true},
			wantEmpty:    true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			cluster := &RayCluster{}
			opts := cluster.CustomEquivalenceOptions(t.Context(), nil, nil)
			if tc.wantEmpty && len(opts) != 0 {
				t.Errorf("CustomEquivalenceOptions() = %v, want nil", opts)
			}
			if !tc.wantEmpty && len(opts) == 0 {
				t.Errorf("CustomEquivalenceOptions() = nil, want non-empty")
			}
		})
	}
}
