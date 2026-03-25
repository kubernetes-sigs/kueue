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

package rayjob

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		rayJob       *RayJob
		wantPodSets  func(rayJob *RayJob) []kueue.PodSet
		featureGates map[featuregate.Feature]bool
	}{
		"no annotations": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
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
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with required topology annotation": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
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
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName), 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].GroupName), 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"with preferred topology annotation": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
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
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName), 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].GroupName), 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"without required and preferred topology annotation if TAS is disabled": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
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
				).
				Obj()),
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Annotations).
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName), 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Annotations).
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].GroupName), 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Annotations).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with submitter topology annotation not specified": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				WithSubmissionMode(rayv1.K8sJobMode).
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "group1",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Annotations).
						RequiredTopologyRequest("cloud.com/rack").
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName), 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet("submitter", 1).
						PodSpec(*getSubmitterTemplate(rayJob).Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"with submitter topology annotation specified": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				WithSubmissionMode(rayv1.K8sJobMode).
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetRequiredTopologyAnnotation: "cloud.com/rack",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
				).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "group1",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueue.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
				).
				WithSubmitterPodTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "submitter_c",
							},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj()),
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Annotations).
						RequiredTopologyRequest("cloud.com/rack").
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName), 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Annotations).
						PreferredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet("submitter", 1).
						PodSpec(*rayJob.Spec.SubmitterPodTemplate.Spec.DeepCopy()).
						Annotations(rayJob.Spec.SubmitterPodTemplate.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"with NumOfHosts > 1": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
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
						NumOfHosts: 4,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName:  "group2",
						Replicas:   ptr.To[int32](3),
						NumOfHosts: 4,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName), 4).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet(kueue.NewPodSetReference(rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].GroupName), 12).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with default job submitter": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				WithSubmissionMode(rayv1.K8sJobMode).
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
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("submitter", 1).
						PodSpec(getSubmitterTemplate(rayJob).Spec).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with submitter job pod template override": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				WithSubmissionMode(rayv1.K8sJobMode).
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
				WithSubmitterPodTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "ray-job-submitter",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("50m"),
										corev1.ResourceMemory: resource.MustParse("100Mi"),
									},
								},
							},
						},
						RestartPolicy: corev1.RestartPolicyNever,
					},
				}).
				Obj()),
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("submitter", 1).
						PodSpec(corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyNever,
							Containers: []corev1.Container{
								*utiltesting.MakeContainer().
									Name("ray-job-submitter").
									WithResourceReq(corev1.ResourceCPU, "50m").
									WithResourceReq(corev1.ResourceMemory, "100Mi").
									Obj(),
							},
						}).
						Obj(),
				}
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.rayJob.PodSets(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets(tc.rayJob), gotPodSets); diff != "" {
				t.Errorf("pod sets mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNodeSelectors(t *testing.T) {
	baseJob := testingrayutil.MakeJob("job", "ns").
		WithHeadGroupSpec(rayv1.HeadGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{},
				},
			},
		}).
		WithWorkerGroups(rayv1.WorkerGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
			},
		}, rayv1.WorkerGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"key-wg2": "value-wg2",
					},
				},
			},
		}).
		Obj()

	cases := map[string]struct {
		job          *rayv1.RayJob
		runInfo      []podset.PodSetInfo
		restoreInfo  []podset.PodSetInfo
		wantRunError error
		wantAfterRun *rayv1.RayJob
		wantFinal    *rayv1.RayJob
	}{
		"valid configuration": {
			job: baseJob.DeepCopy(),
			runInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"newKey": "newValue",
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
				{
					NodeSelector: map[string]string{
						// don't add anything
					},
				},
			},
			restoreInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						// clean it all
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg2": "value-wg2",
					},
				},
			},
			wantAfterRun: testingrayutil.MakeJob("job", "ns").
				Suspend(false).
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"newKey": "newValue",
							},
						},
					},
				}).
				WithWorkerGroups(rayv1.WorkerGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"key-wg1": "value-wg1",
							},
						},
					},
				}, rayv1.WorkerGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"key-wg2": "value-wg2",
							},
						},
					},
				}).
				Obj(),

			wantFinal: baseJob.DeepCopy(),
		},
		"invalid runInfo": {
			job: baseJob.DeepCopy(),
			runInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{
						"newKey": "newValue",
					},
				},
				{
					NodeSelector: map[string]string{
						"key-wg1": "updated-value-wg1",
					},
				},
			},
			wantRunError: podset.ErrInvalidPodsetInfo,
			wantAfterRun: baseJob.DeepCopy(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			genJob := (*RayJob)(tc.job)
			gotRunError := genJob.RunWithPodSetsInfo(ctx, tc.runInfo)

			if diff := cmp.Diff(tc.wantRunError, gotRunError, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected run error (-want/+got): %s", diff)
			}
			if diff := cmp.Diff(tc.wantAfterRun, tc.job); diff != "" {
				t.Errorf("Unexpected job after run (-want/+got): %s", diff)
			}

			if tc.wantRunError == nil {
				genJob.Suspend()
				genJob.RestorePodSetsInfo(tc.restoreInfo)
				if diff := cmp.Diff(tc.wantFinal, tc.job); diff != "" {
					t.Errorf("Unexpected job after restore (-want/+got): %s", diff)
				}
			}
		})
	}
}

func Test_RayJobFinished(t *testing.T) {
	testcases := []struct {
		name             string
		status           rayv1.RayJobStatus
		expectedSuccess  bool
		expectedFinished bool
	}{
		{
			name: "jobStatus=Running, jobDeploymentStatus=Running",
			status: rayv1.RayJobStatus{
				JobStatus:           rayv1.JobStatusRunning,
				JobDeploymentStatus: rayv1.JobDeploymentStatusRunning,
			},
			expectedSuccess:  false,
			expectedFinished: false,
		},
		{
			name: "jobStatus=Succeeded, jobDeploymentStatus=Complete",
			status: rayv1.RayJobStatus{
				JobStatus:           rayv1.JobStatusSucceeded,
				JobDeploymentStatus: rayv1.JobDeploymentStatusComplete,
			},
			expectedSuccess:  true,
			expectedFinished: true,
		},
		{
			name: "jobStatus=Failed, jobDeploymentStatus=Complete",
			status: rayv1.RayJobStatus{
				JobStatus:           rayv1.JobStatusFailed,
				JobDeploymentStatus: rayv1.JobDeploymentStatusComplete,
			},
			expectedSuccess:  false,
			expectedFinished: true,
		},
		{
			name: "jobStatus=Failed, jobDeploymentStatus=Failed",
			status: rayv1.RayJobStatus{
				JobStatus:           rayv1.JobStatusFailed,
				JobDeploymentStatus: rayv1.JobDeploymentStatusFailed,
			},
			expectedSuccess:  false,
			expectedFinished: true,
		},
		{
			name: "jobStatus=Running, jobDeploymentStatus=Failed (when activeDeadlineSeconds is exceeded)",
			status: rayv1.RayJobStatus{
				JobStatus:           rayv1.JobStatusRunning,
				JobDeploymentStatus: rayv1.JobDeploymentStatusFailed,
			},
			expectedSuccess:  false,
			expectedFinished: true,
		},
	}

	for _, testcase := range testcases {
		t.Run(testcase.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			rayJob := testingrayutil.MakeJob("job", "ns").Obj()
			rayJob.Status = testcase.status

			_, success, finished := ((*RayJob)(rayJob)).Finished(ctx)
			if success != testcase.expectedSuccess {
				t.Logf("actual success: %v", success)
				t.Logf("expected success: %v", testcase.expectedSuccess)
				t.Error("unexpected result for 'success'")
			}

			if finished != testcase.expectedFinished {
				t.Logf("actual finished: %v", finished)
				t.Logf("expected finished: %v", testcase.expectedFinished)
				t.Error("unexpected result for 'finished'")
			}
		})
	}
}

func TestGetCustomAnnotations(t *testing.T) {
	headSpec := rayv1.HeadGroupSpec{
		Template: corev1.PodTemplateSpec{
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
		},
	}
	workerGroup := func(name string, replicas int32) rayv1.WorkerGroupSpec {
		return rayv1.WorkerGroupSpec{
			GroupName: name,
			Replicas:  ptr.To(replicas),
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: name + "_c"}}},
			},
		}
	}

	testCases := map[string]struct {
		rayJob               *rayv1.RayJob
		rayCluster           *rayv1.RayCluster
		wantCustomAnnotation map[string]string
		wantGroupCount       int32
	}{
		"first call sets annotation with all podset counts": {
			rayJob: testingrayutil.MakeJob("rayjob", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				WithHeadGroupSpec(headSpec).
				WithWorkerGroups(workerGroup("group1", 1)).
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayCluster: testingraycluster.MakeCluster("test-cluster", "ns").
				WithWorkerGroups(workerGroup("group1", 5)).
				Obj(),
			wantCustomAnnotation: map[string]string{
				raycluster.RayClusterGenerationAnnotation:         "0",
				raycluster.RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"group1","count":5}]`,
			},
			wantGroupCount: 5,
		},
		"skip patch when annotation already matches": {
			rayJob: testingrayutil.MakeJob("rayjob", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"head","count":1},{"name":"group1","count":5}]`).
				WithHeadGroupSpec(headSpec).
				WithWorkerGroups(workerGroup("group1", 1)).
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayCluster: testingraycluster.MakeCluster("test-cluster", "ns").
				WithWorkerGroups(workerGroup("group1", 5)).
				Obj(),
			wantCustomAnnotation: map[string]string{
				raycluster.RayClusterGenerationAnnotation:         "0",
				raycluster.RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"group1","count":5}]`,
			},
			wantGroupCount: 5,
		},
		"annotation updated after scale-down": {
			rayJob: testingrayutil.MakeJob("rayjob", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"head","count":1},{"name":"group1","count":6}]`).
				WithHeadGroupSpec(headSpec).
				WithWorkerGroups(workerGroup("group1", 4)).
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayCluster: testingraycluster.MakeCluster("test-cluster", "ns").
				WithWorkerGroups(workerGroup("group1", 4)).
				Obj(),
			wantCustomAnnotation: map[string]string{
				raycluster.RayClusterGenerationAnnotation:         "0",
				raycluster.RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"group1","count":4}]`,
			},
			wantGroupCount: 4,
		},
		"annotation updated when podset count differs from annotation length": {
			rayJob: testingrayutil.MakeJob("rayjob", "ns").
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(raycluster.RayClusterPodsetReplicaSizesAnnotation, `[{"name":"group1","count":5}]`).
				WithHeadGroupSpec(headSpec).
				WithWorkerGroups(workerGroup("group1", 1)).
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayCluster: testingraycluster.MakeCluster("test-cluster", "ns").
				WithWorkerGroups(workerGroup("group1", 5)).
				Obj(),
			wantCustomAnnotation: map[string]string{
				raycluster.RayClusterGenerationAnnotation:         "0",
				raycluster.RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"group1","count":5}]`,
			},
			wantGroupCount: 5,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			ctx, _ := utiltesting.ContextWithLog(t)

			tc.rayJob.Status.RayClusterName = tc.rayCluster.Name

			fakeClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).
				WithObjects(tc.rayJob, tc.rayCluster).
				Build()

			oldReconciler := reconciler
			reconciler = rayJobReconciler{client: fakeClient}
			t.Cleanup(func() { reconciler = oldReconciler })

			job := (*RayJob)(tc.rayJob)
			podSets, err := job.PodSets(ctx)
			if err != nil {
				t.Fatalf("unexpected error from PodSets: %v", err)
			}

			// Verify the worker group count was updated
			for _, ps := range podSets {
				if ps.Name == "group1" {
					if ps.Count != tc.wantGroupCount {
						t.Errorf("group1 count = %d, want %d", ps.Count, tc.wantGroupCount)
					}
				}
			}

			// Verify GetCustomAnnotations returns the expected annotations
			gotAnnotations, err := job.GetCustomAnnotations(ctx, fakeClient, podSets)
			if err != nil {
				t.Fatalf("unexpected error from GetCustomAnnotations: %v", err)
			}

			if diff := cmp.Diff(tc.wantCustomAnnotation, gotAnnotations); diff != "" {
				t.Errorf("GetCustomAnnotations mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
