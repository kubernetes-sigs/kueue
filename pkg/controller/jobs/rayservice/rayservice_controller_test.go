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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	rayutils "github.com/ray-project/kuberay/ray-operator/controllers/ray/utils"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func childRayCluster(name, rayServiceName, namespace, groupName string, replicas int32, enableAutoscaling ...bool) rayv1.RayCluster {
	cluster := rayv1.RayCluster{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels: map[string]string{
				rayutils.RayOriginatedFromCRNameLabelKey: rayServiceName,
				rayutils.RayOriginatedFromCRDLabelKey:    rayutils.RayOriginatedFromCRDLabelValue(rayutils.RayServiceCRD),
			},
		},
		Spec: rayv1.RayClusterSpec{
			HeadGroupSpec: rayv1.HeadGroupSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
				},
			},
			WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
				{
					GroupName: groupName,
					Replicas:  new(replicas),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: groupName + "_c"}}},
					},
				},
			},
		},
	}
	if len(enableAutoscaling) > 0 {
		cluster.Spec.EnableInTreeAutoscaling = ptr.To(enableAutoscaling[0])
	}
	return cluster
}

func TestPodSets(t *testing.T) {
	collectorImage := "quay.io/kuberay/collector:v1.7.0"
	autoscaler := corev1.Container{
		Name: "autoscaler",
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("500m"),
				corev1.ResourceMemory: resource.MustParse("512Mi"),
			},
		},
	}
	// collector mirrors the History Server collector KubeRay injects into every
	// head and worker Pod, with KubeRay's default resources.
	collector := corev1.Container{
		Name:  rayutils.CollectorContainerName,
		Image: collectorImage,
		Resources: corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("50m"),
				corev1.ResourceMemory: resource.MustParse("64Mi"),
			},
			Limits: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("200m"),
				corev1.ResourceMemory: resource.MustParse("256Mi"),
			},
		},
	}

	testCases := map[string]struct {
		rayService   *RayService
		children     []rayv1.RayCluster
		wantPodSets  []kueue.PodSet
		featureGates map[featuregate.Feature]bool
	}{
		"no annotations": {
			rayService: (*RayService)(&rayv1.RayService{
				Name:      "rayservice",
				Namespace: "ns",
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
								Replicas:  new(int32(3)),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
								},
							},
						},
					},
				},
			}),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
				*utiltestingapi.MakePodSet("group2", 3).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with history server collector": {
			rayService: (*RayService)(testingrayservice.MakeService("rayservice", "ns").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
					},
				}).
				WithWorkerGroups(rayv1.WorkerGroupSpec{
					GroupName: "group1",
					Replicas:  new(int32(2)),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
					},
				}).
				WithHistoryServerOptions(&rayv1.HistoryServerOptions{
					CollectorOptions: &rayv1.CollectorOptions{
						Image: &collectorImage,
					},
				}).
				Obj()),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}, collector}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 2).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}, collector}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with required topology annotation": {
			rayService: (*RayService)(&rayv1.RayService{
				Name:      "rayservice",
				Namespace: "ns",
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
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					RequiredTopologyRequest("cloud.com/block").
					Obj(),
				*utiltestingapi.MakePodSet("group1", 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Annotations(map[string]string{kueue.PodSetRequiredTopologyAnnotation: "cloud.com/block"}).
					RequiredTopologyRequest("cloud.com/block").
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
		},
		"with NumOfHosts > 1": {
			rayService: (*RayService)(&rayv1.RayService{
				Name:      "rayservice",
				Namespace: "ns",
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
								Replicas:   new(int32(2)),
								NumOfHosts: 3,
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
			}),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Obj(),
				// 2 replicas * 3 NumOfHosts
				*utiltestingapi.MakePodSet("group1", 6).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"with gcs fault tolerance": {
			rayService: (*RayService)(&rayv1.RayService{
				Name:      "rayservice",
				Namespace: "ns",
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						GcsFaultToleranceOptions: &rayv1.GcsFaultToleranceOptions{
							RedisAddress: "redis:6379",
						},
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								ObjectMeta: metav1.ObjectMeta{
									Labels: map[string]string{"ray.io/cluster": "rayservice"},
								},
								Spec: corev1.PodSpec{Containers: []corev1.Container{{
									Name:  "head_c",
									Image: "rayproject/ray:2.0.0",
								}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Replicas:  new(int32(1)),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
			}),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{
							Name:  "head_c",
							Image: "rayproject/ray:2.0.0",
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("200m"),
									corev1.ResourceMemory: resource.MustParse("256Mi"),
								},
							},
						}},
					}).
					Labels(map[string]string{"ray.io/cluster": "rayservice"}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"steady state: single child, PodSets reflect the child's live spec": {
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
			}),
			children: []rayv1.RayCluster{
				childRayCluster("rayservice-active", "rayservice", "ns", "group1", 5, true),
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}, autoscaler}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 5).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.ElasticJobsViaWorkloadSlices: true,
			},
		},
		"workload slicing with autoscaling disabled uses the RayService spec": {
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
						EnableInTreeAutoscaling: ptr.To(false),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{{
							GroupName: "group1",
							Replicas:  ptr.To[int32](2),
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
							},
						}},
					},
				},
				Status: rayv1.RayServiceStatuses{
					ActiveServiceStatus: rayv1.RayServiceStatus{RayClusterName: "rayservice-cluster"},
				},
			}),
			children: []rayv1.RayCluster{{
				ObjectMeta: metav1.ObjectMeta{Name: "rayservice-cluster", Namespace: "ns"},
				Spec: rayv1.RayClusterSpec{
					WorkerGroupSpecs: []rayv1.WorkerGroupSpec{{
						GroupName: "group1",
						Replicas:  ptr.To[int32](10),
					}},
				},
			}},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 2).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.ElasticJobsViaWorkloadSlices: true,
			},
		},
		"zero-downtime upgrade: two children, counts are summed": {
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
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{
								GroupName: "group1",
								Replicas:  new(int32(2)),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
			}),
			children: []rayv1.RayCluster{
				childRayCluster("rayservice-active", "rayservice", "ns", "group1", 2),
				childRayCluster("rayservice-pending", "rayservice", "ns", "group1", 2),
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 2).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 4).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.ElasticJobsViaWorkloadSlices: true,
			},
		},
		"bootstrap: no children yet, build from the RayService template": {
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
								Replicas:  new(int32(2)),
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
			}),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}, autoscaler}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 2).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling:      false,
				features.ElasticJobsViaWorkloadSlices: true,
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			objs := []client.Object{}
			for i := range tc.children {
				objs = append(objs, &tc.children[i])
			}
			fakeClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(objs...).Build()

			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.rayService.PodSets(ctx, fakeClient)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
				t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPodSetsRejectsDifferentResourceRequestsDuringUpgrade(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.TopologyAwareScheduling:      false,
		features.ElasticJobsViaWorkloadSlices: true,
	})

	rayService := (*RayService)(&rayv1.RayService{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "rayservice",
			Namespace: "ns",
			Annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
			},
		},
	})
	active := childRayCluster("rayservice-active", "rayservice", "ns", "group1", 1)
	pending := childRayCluster("rayservice-pending", "rayservice", "ns", "group1", 1)
	pending.Spec.WorkerGroupSpecs[0].Template.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("1"),
	}
	fakeClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).
		WithObjects(&active, &pending).
		Build()

	ctx, _ := utiltesting.ContextWithLog(t)
	_, err := rayService.PodSets(ctx, fakeClient)
	if err == nil || !strings.Contains(err.Error(), "incompatible resource requests") {
		t.Fatalf("PodSets() error = %v, want incompatible resource requests error", err)
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
					Suspend: false,
				},
			}),
			want: false,
		},
		"suspended": {
			rayService: (*RayService)(&rayv1.RayService{
				Spec: rayv1.RayServiceSpec{
					Suspend: true,
				},
			}),
			want: true,
		},
		"default (unset) - not suspended": {
			rayService: (*RayService)(&rayv1.RayService{
				Spec: rayv1.RayServiceSpec{},
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

func TestSuspendDoesNotSuspendRayClusterTemplate(t *testing.T) {
	rayService := (*RayService)(&rayv1.RayService{
		Spec: rayv1.RayServiceSpec{
			RayClusterSpec: rayv1.RayClusterSpec{
				Suspend: ptr.To(false),
			},
		},
	})

	rayService.Suspend()

	if !rayService.Spec.Suspend {
		t.Error("Suspend() did not suspend the RayService")
	}
	if got := ptr.Deref(rayService.Spec.RayClusterSpec.Suspend, false); got {
		t.Error("Suspend() suspended the RayCluster template; elastic Pod scheduling gates should control child Pods")
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
			got := tc.rayService.PodsReady(ctx, nil)
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
