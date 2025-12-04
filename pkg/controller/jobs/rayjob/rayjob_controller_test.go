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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta1"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		rayJob                        *RayJob
		wantPodSets                   func(rayJob *RayJob) []kueue.PodSet
		enableTopologyAwareScheduling bool
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
			enableTopologyAwareScheduling: false,
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
			enableTopologyAwareScheduling: true,
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
			enableTopologyAwareScheduling: true,
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
			enableTopologyAwareScheduling: false,
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
			enableTopologyAwareScheduling: true,
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
			enableTopologyAwareScheduling: true,
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
			enableTopologyAwareScheduling: false,
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
			enableTopologyAwareScheduling: false,
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
			enableTopologyAwareScheduling: false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			gotPodSets, err := tc.rayJob.PodSets()
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
			genJob := (*RayJob)(tc.job)
			gotRunError := genJob.RunWithPodSetsInfo(tc.runInfo)

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
			rayJob := testingrayutil.MakeJob("job", "ns").Obj()
			rayJob.Status = testcase.status

			_, success, finished := ((*RayJob)(rayJob)).Finished()
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

func TestGetWorkload(t *testing.T) {
	testCases := map[string]struct {
		rayJob           *RayJob
		rayCluster       *rayv1.RayCluster
		workload         *kueue.Workload
		enableFeature    bool
		wantWorkload     *kueue.Workload
		wantError        bool
		workloadOnRayJob *kueue.Workload
	}{
		"default case: without InTreeAutoscaling, returns workload for RayJob": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").Obj()),
			workloadOnRayJob: utiltesting.MakeWorkload("wl-rayjob", "ns").
				OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "rayjob", "").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl-rayjob", "ns").
				OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "rayjob", "").
				Obj(),
		},
		"InTreeAutoscaling enabled but workload slicing disabled": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				EnableInTreeAutoscaling().
				Obj()),
			workloadOnRayJob: utiltesting.MakeWorkload("wl-rayjob", "ns").
				OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "rayjob", "").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl-rayjob", "ns").
				OwnerReference(rayv1.GroupVersion.WithKind("RayJob"), "rayjob", "").
				Obj(),
		},
		"InTreeAutoscaling enabled with workload slicing but RayClusterName is empty": {
			enableFeature: true,
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				EnableInTreeAutoscaling().
				AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
				Obj()),
			wantWorkload: nil,
		},
		"InTreeAutoscaling enabled with workload slicing but RayCluster not found": {
			enableFeature: true,
			rayJob: func() *RayJob {
				job := testingrayutil.MakeJob("rayjob", "ns").
					EnableInTreeAutoscaling().
					AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
					Obj()
				job.Status.RayClusterName = "test-cluster"
				return (*RayJob)(job)
			}(),
			wantError: true,
		},
		"InTreeAutoscaling enabled with workload slicing, RayCluster exists with workload": {
			enableFeature: true,
			rayJob: func() *RayJob {
				job := testingrayutil.MakeJob("rayjob", "ns").
					EnableInTreeAutoscaling().
					AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
					Obj()
				job.Status.RayClusterName = "test-cluster"
				return (*RayJob)(job)
			}(),
			rayCluster: func() *rayv1.RayCluster {
				cluster := &rayv1.RayCluster{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test-cluster",
						Namespace: "ns",
					},
				}
				return cluster
			}(),
			workload: utiltesting.MakeWorkload("wl-cluster", "ns").
				OwnerReference(rayv1.GroupVersion.WithKind("RayCluster"), "test-cluster", "").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl-cluster", "ns").
				OwnerReference(rayv1.GroupVersion.WithKind("RayCluster"), "test-cluster", "").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tc.enableFeature)

			clientBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme)
			clientBuilder = clientBuilder.WithObjects(tc.rayJob.Object())

			if tc.rayCluster != nil {
				clientBuilder = clientBuilder.WithObjects(tc.rayCluster)
			}
			if tc.workload != nil {
				clientBuilder = clientBuilder.WithObjects(tc.workload)
			}
			if tc.workloadOnRayJob != nil {
				clientBuilder = clientBuilder.WithObjects(tc.workloadOnRayJob)
			}

			client := clientBuilder.Build()

			gotWorkload, err := tc.rayJob.GetWorkload(ctx, client)

			if tc.wantError {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.wantWorkload, gotWorkload,
				cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected workload (-want +got):\n%s", diff)
			}
		})
	}
}

func TestSkip(t *testing.T) {
	testCases := map[string]struct {
		rayJob        *RayJob
		enableFeature bool
		wantSkip      bool
	}{
		"default case: no cluster selector, no autoscaling": {
			rayJob:   (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").Obj()),
			wantSkip: false,
		},
		"has cluster selector": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				ClusterSelector(map[string]string{
					"key": "value",
				}).
				Obj()),
			wantSkip: true,
		},
		"InTreeAutoscaling enabled without workload slicing annotation": {
			enableFeature: true,
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				EnableInTreeAutoscaling().
				Obj()),
			wantSkip: false,
		},
		"InTreeAutoscaling enabled with workload slicing annotation": {
			enableFeature: true,
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				EnableInTreeAutoscaling().
				AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
				Obj()),
			wantSkip: true,
		},
		"InTreeAutoscaling disabled with workload slicing annotation": {
			enableFeature: true,
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
				Obj()),
			wantSkip: false,
		},
		"workload slicing feature disabled, has annotation and autoscaling": {
			enableFeature: false,
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				EnableInTreeAutoscaling().
				AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
				Obj()),
			wantSkip: false,
		},
		"has cluster selector with autoscaling and workload slicing": {
			enableFeature: true,
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				ClusterSelector(map[string]string{
					"key": "value",
				}).
				EnableInTreeAutoscaling().
				AddAnnotation("kueue.x-k8s.io/elastic-job", "true").
				Obj()),
			wantSkip: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tc.enableFeature)

			gotSkip := tc.rayJob.Skip()

			if gotSkip != tc.wantSkip {
				t.Errorf("Skip() = %v, want %v", gotSkip, tc.wantSkip)
			}
		})
	}
}
