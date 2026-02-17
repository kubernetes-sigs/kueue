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

package rayservice

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		rayService                    *RayService
		rayCluster                    *rayv1.RayCluster
		wantPodSets                   func(rayService *RayService) []kueue.PodSet
		enableTopologyAwareScheduling bool
		enableElasticJobsFeature      bool
	}{
		"no annotations": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
							{
								GroupName: "group2",
								Replicas:  ptr.To[int32](3),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
								},
							},
						},
					},
				},
			}),
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group2", 3).
						PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.DeepCopy()).
						Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
		"with required topology annotation": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{
									Annotations: map[string]string{
										kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block",
									},
								},
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
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
						},
					},
				},
			}),
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Annotations(rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
					*utiltestingapi.MakePodSet("group1", 1).
						PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
						Annotations(rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Annotations).
						RequiredTopologyRequest("cloud.com/block").
						Obj(),
				}
			},
			enableTopologyAwareScheduling: true,
		},
		"with NumOfHosts > 1": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName:  "group1",
								Replicas:   ptr.To[int32](2),
								NumOfHosts: 3,
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
			}),
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 6). // 2 replicas * 3 NumOfHosts
											PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
											Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
		},
		"with workload slicing and autoscaling enabled, update from RayCluster": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Annotations: map[string]string{
						workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						EnableInTreeAutoscaling: ptr.To(true),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Replicas:  ptr.To[int32](1),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
				Status: rayv1.RayServiceStatuses{
					ActiveServiceStatus: rayv1.RayServiceStatus{
						RayClusterName: "rayservice-cluster",
					},
				},
			}),
			rayCluster: &rayv1.RayCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice-cluster",
					Namespace: "ns",
				},
				Spec: rayv1.RayClusterSpec{
					HeadGroupSpec: rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
						},
					},
					WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
						{
							GroupName: "group1",
							Replicas:  ptr.To[int32](5), // RayCluster has scaled to 5 replicas
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
							},
						},
					},
				},
			},
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 5). // Updated from RayCluster
											PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
											Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
			enableElasticJobsFeature:      true,
		},
		"with workload slicing enabled but autoscaling disabled, use spec count": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Annotations: map[string]string{
						workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						EnableInTreeAutoscaling: ptr.To(false), // Autoscaling disabled
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Replicas:  ptr.To[int32](2),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
				Status: rayv1.RayServiceStatuses{
					ActiveServiceStatus: rayv1.RayServiceStatus{
						RayClusterName: "rayservice-cluster",
					},
				},
			}),
			rayCluster: &rayv1.RayCluster{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice-cluster",
					Namespace: "ns",
				},
				Spec: rayv1.RayClusterSpec{
					WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
						{
							GroupName: "group1",
							Replicas:  ptr.To[int32](10), // RayCluster has different count
						},
					},
				},
			},
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 2). // Uses spec count, not RayCluster
											PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
											Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
			enableElasticJobsFeature:      true,
		},
		"with workload slicing and autoscaling enabled, RayCluster not found fallback to spec": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Annotations: map[string]string{
						workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						EnableInTreeAutoscaling: ptr.To(true),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Replicas:  ptr.To[int32](3),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
				Status: rayv1.RayServiceStatuses{
					ActiveServiceStatus: rayv1.RayServiceStatus{
						RayClusterName: "nonexistent-cluster",
					},
				},
			}),
			rayCluster: nil, // No RayCluster exists
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 3). // Fallback to spec count
											PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
											Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
			enableElasticJobsFeature:      true,
		},
		"with workload slicing and autoscaling enabled, no RayClusterName in status": {
			rayService: (*RayService)(&rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Annotations: map[string]string{
						workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						EnableInTreeAutoscaling: ptr.To(true),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Replicas:  ptr.To[int32](2),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
				Status: rayv1.RayServiceStatuses{
					ActiveServiceStatus: rayv1.RayServiceStatus{
						RayClusterName: "", // No cluster name yet
					},
				},
			}),
			rayCluster: nil,
			wantPodSets: func(rayService *RayService) []kueue.PodSet {
				return []kueue.PodSet{
					*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
						PodSpec(*rayService.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.DeepCopy()).
						Obj(),
					*utiltestingapi.MakePodSet("group1", 2). // Uses spec count
											PodSpec(*rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.DeepCopy()).
											Obj(),
				}
			},
			enableTopologyAwareScheduling: false,
			enableElasticJobsFeature:      true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.enableTopologyAwareScheduling)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, tc.enableElasticJobsFeature)

			// Set up fake client with optional RayCluster
			objs := []client.Object{}
			if tc.rayCluster != nil {
				objs = append(objs, tc.rayCluster)
			}
			fakeClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(objs...).Build()

			// Set up the reconciler with the fake client
			reconciler = rayServiceReconciler{
				client: fakeClient,
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.rayService.PodSets(ctx)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			wantPodSets := tc.wantPodSets(tc.rayService)
			if diff := cmp.Diff(wantPodSets, gotPodSets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestIsSuspended(t *testing.T) {
	testCases := map[string]struct {
		rayService *RayService
		want       bool
	}{
		"not suspended": {
			rayService: (*RayService)(&rayv1.RayService{
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						Suspend: ptr.To(false),
					},
				},
			}),
			want: false,
		},
		"suspended": {
			rayService: (*RayService)(&rayv1.RayService{
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						Suspend: ptr.To(true),
					},
				},
			}),
			want: true,
		},
		"suspend is nil": {
			rayService: (*RayService)(&rayv1.RayService{
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{},
				},
			}),
			want: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := tc.rayService.IsSuspended()
			if got != tc.want {
				t.Errorf("IsSuspended() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestIsActive(t *testing.T) {
	testCases := map[string]struct {
		rayService *RayService
		want       bool
	}{
		"active - RayServiceReady condition is true": {
			rayService: (*RayService)(&rayv1.RayService{
				Status: rayv1.RayServiceStatuses{
					Conditions: []metav1.Condition{
						{
							Type:   string(rayv1.RayServiceReady),
							Status: metav1.ConditionTrue,
						},
					},
				},
			}),
			want: true,
		},
		"not active - RayServiceReady condition is false": {
			rayService: (*RayService)(&rayv1.RayService{
				Status: rayv1.RayServiceStatuses{
					Conditions: []metav1.Condition{
						{
							Type:   string(rayv1.RayServiceReady),
							Status: metav1.ConditionFalse,
						},
					},
				},
			}),
			want: false,
		},
		"not active - no conditions": {
			rayService: (*RayService)(&rayv1.RayService{
				Status: rayv1.RayServiceStatuses{},
			}),
			want: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := tc.rayService.IsActive()
			if got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodsReady(t *testing.T) {
	testCases := map[string]struct {
		rayService *RayService
		want       bool
	}{
		"pods ready - RayServiceReady condition is true": {
			rayService: (*RayService)(&rayv1.RayService{
				Status: rayv1.RayServiceStatuses{
					Conditions: []metav1.Condition{
						{
							Type:   string(rayv1.RayServiceReady),
							Status: metav1.ConditionTrue,
						},
					},
				},
			}),
			want: true,
		},
		"pods not ready - RayServiceReady condition is false": {
			rayService: (*RayService)(&rayv1.RayService{
				Status: rayv1.RayServiceStatuses{
					Conditions: []metav1.Condition{
						{
							Type:   string(rayv1.RayServiceReady),
							Status: metav1.ConditionFalse,
						},
					},
				},
			}),
			want: false,
		},
		"pods not ready - no conditions": {
			rayService: (*RayService)(&rayv1.RayService{
				Status: rayv1.RayServiceStatuses{},
			}),
			want: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			got := tc.rayService.PodsReady(ctx)
			if got != tc.want {
				t.Errorf("PodsReady() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestGVK(t *testing.T) {
	rayService := &RayService{}
	gvk := rayService.GVK()

	if gvk.Group != "ray.io" {
		t.Errorf("GVK().Group = %v, want ray.io", gvk.Group)
	}
	if gvk.Version != "v1" {
		t.Errorf("GVK().Version = %v, want v1", gvk.Version)
	}
	if gvk.Kind != "RayService" {
		t.Errorf("GVK().Kind = %v, want RayService", gvk.Kind)
	}
}
