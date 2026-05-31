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
)

// childRayCluster builds a RayCluster owned by the named RayService, labelled the
// way KubeRay labels children so (*RayService).PodSets discovers it by selector.
// It carries a head group plus a single worker group with the given replica count.
func childRayCluster(name, rayServiceName, namespace, groupName string, replicas int32) rayv1.RayCluster {
	return rayv1.RayCluster{
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
					Replicas:  ptr.To(replicas),
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: groupName + "_c"}}},
					},
				},
			},
		},
	}
}

func TestPodSets(t *testing.T) {
	collectorImage := "quay.io/kuberay/collector:v1.7.0"
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
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
				},
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
			// One admitted child RayCluster. PodSets are built from the child's spec
			// rather than the RayService template, so a worker group the autoscaler
			// has scaled (here group1 1->5) is reserved at its real size.
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
								Replicas:  ptr.To[int32](1), // template; child has scaled to 5
								Template: corev1.PodTemplateSpec{
									Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}},
								},
							},
						},
					},
				},
			}),
			children: []rayv1.RayCluster{
				childRayCluster("rayservice-active", "rayservice", "ns", "group1", 5),
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 5).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"zero-downtime upgrade: two children, counts are summed": {
			// During a zero-downtime upgrade KubeRay runs an active and a pending
			// child side by side. PodSets union by group name and sum counts so the
			// workload reserves quota for both clusters: head 1+1=2, group1 2+2=4.
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
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
		"bootstrap: no children yet, build from the RayService template": {
			// Before KubeRay creates the first child, PodSets fall back to the
			// RayService template so the Workload exists with the right shape.
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
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "head_c"}}}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 2).
					PodSpec(corev1.PodSpec{Containers: []corev1.Container{{Name: "group1_c"}}}).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: false},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			// Seed the fake client with the RayService's child RayClusters.
			objs := []client.Object{}
			for i := range tc.children {
				objs = append(objs, &tc.children[i])
			}
			fakeClient := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithObjects(objs...).Build()

			// Set up the reconciler with the fake client
			reconciler = rayServiceReconciler{
				client: fakeClient,
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			gotPodSets, err := tc.rayService.PodSets(ctx, nil)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantPodSets, gotPodSets); diff != "" {
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
		// IsSuspended reads the top-level Spec.Suspend (KubeRay PR #4841), not the
		// nested RayClusterSpec.Suspend template gate.
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
