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
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestBuildPodSets(t *testing.T) {
	testCases := map[string]struct {
		rayClusterSpec *rayv1.RayClusterSpec
		wantPodSets    []kueue.PodSet
		wantErr        bool
	}{
		"basic spec with head and single worker group": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Replicas:  ptr.To[int32](3),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "head"}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("workers", 3).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker"}},
					}).
					Obj(),
			},
		},
		"spec with multiple worker groups": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "group1",
						Replicas:  ptr.To[int32](2),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker1"}},
							},
						},
					},
					{
						GroupName: "group2",
						Replicas:  ptr.To[int32](5),
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker2"}},
							},
						},
					},
				},
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "head"}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("group1", 2).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker1"}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("group2", 5).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker2"}},
					}).
					Obj(),
			},
		},
		"spec with worker group without replicas specified": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "head"}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("workers", 1).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker"}},
					}).
					Obj(),
			},
		},
		"spec with NumOfHosts > 1": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName:  "workers",
						Replicas:   ptr.To[int32](3),
						NumOfHosts: 2,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).
					PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "head"}},
					}).
					Obj(),
				*utiltestingapi.MakePodSet("workers", 6). // 3 replicas * 2 hosts = 6
										PodSpec(corev1.PodSpec{
						Containers: []corev1.Container{{Name: "worker"}},
					}).
					Obj(),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotPodSets, err := BuildPodSets(tc.rayClusterSpec)

			if tc.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if diff := cmp.Diff(tc.wantPodSets, gotPodSets, cmpopts.IgnoreFields(kueue.PodSet{}, "TopologyRequest")); diff != "" {
				t.Errorf("Unexpected podSets (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdatePodSets(t *testing.T) {
	testCases := map[string]struct {
		podSets                 []kueue.PodSet
		object                  client.Object
		enableInTreeAutoscaling *bool
		rayClusterName          string
		rayClusterInClient      *rayv1.RayCluster
		wantPodSets             []kueue.PodSet
		wantErr                 bool
	}{
		"workload slicing disabled - no update": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(true),
			rayClusterName:          "raycluster",
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
		},
		"autoscaling disabled - no update": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(false)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(false),
			rayClusterName:          "raycluster",
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
		},
		"empty rayClusterName - no update": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(true),
			rayClusterName:          "",
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
		},
		"RayCluster not found - no update": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(true),
			rayClusterName:          "nonexistent-raycluster",
			rayClusterInClient:      nil,
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers", 3).Obj(),
			},
		},
		"successful update from RayCluster": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(true),
			rayClusterName:          "target-raycluster",
			rayClusterInClient: testingrayutil.MakeCluster("target-raycluster", "ns").
				ScaleFirstWorkerGroup(5).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 5).Obj(), // Updated from 3 to 5
			},
		},
		"successful update with NumOfHosts": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(true),
			rayClusterName:          "target-raycluster",
			rayClusterInClient: testingrayutil.MakeCluster("target-raycluster", "ns").
				ScaleFirstWorkerGroup(4).
				WithNumOfHosts("workers-group-0", 2).
				Obj(),
			wantPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 8).Obj(), // 4 replicas * 2 hosts = 8
			},
		},
		"podset name mismatch": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
				*utiltestingapi.MakePodSet("different-group-name", 3).Obj(),
			},
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			enableInTreeAutoscaling: ptr.To(true),
			rayClusterName:          "target-raycluster",
			rayClusterInClient: testingrayutil.MakeCluster("target-raycluster", "ns").
				ScaleFirstWorkerGroup(5).
				Obj(),
			wantErr: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)

			scheme := runtime.NewScheme()
			_ = rayv1.AddToScheme(scheme)

			var objs []client.Object
			if tc.rayClusterInClient != nil {
				objs = append(objs, tc.rayClusterInClient)
			}

			c := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objs...).
				Build()

			gotPodSets, err := UpdatePodSets(t.Context(), tc.podSets, c, tc.object, tc.enableInTreeAutoscaling, tc.rayClusterName)

			if tc.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if diff := cmp.Diff(tc.wantPodSets, gotPodSets, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")); diff != "" {
				t.Errorf("Unexpected podSets (-want +got):\n%s", diff)
			}
		})
	}
}

func TestUpdateRayClusterSpecToRunWithPodSetsInfo(t *testing.T) {
	testCases := map[string]struct {
		rayClusterSpec *rayv1.RayClusterSpec
		podSetsInfo    []podset.PodSetInfo
		wantSpec       *rayv1.RayClusterSpec
		wantErr        bool
	}{
		"basic update with node selector": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
			podSetsInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{"node-type": "head"},
				},
				{
					NodeSelector: map[string]string{"node-type": "worker"},
				},
			},
			wantSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:   []corev1.Container{{Name: "head"}},
							NodeSelector: map[string]string{"node-type": "head"},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers:   []corev1.Container{{Name: "worker"}},
								NodeSelector: map[string]string{"node-type": "worker"},
							},
						},
					},
				},
			},
		},
		"update with tolerations and labels": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
			podSetsInfo: []podset.PodSetInfo{
				{
					Labels: map[string]string{"head-label": "value1"},
					Tolerations: []corev1.Toleration{
						{
							Key:      "key1",
							Operator: corev1.TolerationOpEqual,
							Value:    "value1",
							Effect:   corev1.TaintEffectNoSchedule,
						},
					},
				},
				{
					Labels: map[string]string{"worker-label": "value2"},
					Tolerations: []corev1.Toleration{
						{
							Key:      "key2",
							Operator: corev1.TolerationOpEqual,
							Value:    "value2",
							Effect:   corev1.TaintEffectNoExecute,
						},
					},
				},
			},
			wantSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"head-label": "value1"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
							Tolerations: []corev1.Toleration{
								{
									Key:      "key1",
									Operator: corev1.TolerationOpEqual,
									Value:    "value1",
									Effect:   corev1.TaintEffectNoSchedule,
								},
							},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Labels: map[string]string{"worker-label": "value2"},
							},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
								Tolerations: []corev1.Toleration{
									{
										Key:      "key2",
										Operator: corev1.TolerationOpEqual,
										Value:    "value2",
										Effect:   corev1.TaintEffectNoExecute,
									},
								},
							},
						},
					},
				},
			},
		},
		"update multiple worker groups": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers1",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker1"}},
							},
						},
					},
					{
						GroupName: "workers2",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker2"}},
							},
						},
					},
				},
			},
			podSetsInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{"node-type": "head"},
				},
				{
					NodeSelector: map[string]string{"node-type": "worker1"},
				},
				{
					NodeSelector: map[string]string{"node-type": "worker2"},
				},
			},
			wantSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:   []corev1.Container{{Name: "head"}},
							NodeSelector: map[string]string{"node-type": "head"},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers1",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers:   []corev1.Container{{Name: "worker1"}},
								NodeSelector: map[string]string{"node-type": "worker1"},
							},
						},
					},
					{
						GroupName: "workers2",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers:   []corev1.Container{{Name: "worker2"}},
								NodeSelector: map[string]string{"node-type": "worker2"},
							},
						},
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := UpdateRayClusterSpecToRunWithPodSetsInfo(tc.rayClusterSpec, tc.podSetsInfo)

			if tc.wantErr {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("Unexpected error: %v", err)
				return
			}

			if diff := cmp.Diff(tc.wantSpec, tc.rayClusterSpec); diff != "" {
				t.Errorf("Unexpected spec (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRestorePodSetsInfo(t *testing.T) {
	testCases := map[string]struct {
		rayClusterSpec *rayv1.RayClusterSpec
		podSetsInfo    []podset.PodSetInfo
		wantChanged    bool
		wantSpec       *rayv1.RayClusterSpec
	}{
		"restore with different node selector": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:   []corev1.Container{{Name: "head"}},
							NodeSelector: map[string]string{"node-type": "head"},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers:   []corev1.Container{{Name: "worker"}},
								NodeSelector: map[string]string{"node-type": "worker"},
							},
						},
					},
				},
			},
			podSetsInfo: []podset.PodSetInfo{
				{
					NodeSelector: map[string]string{},
				},
				{
					NodeSelector: map[string]string{},
				},
			},
			wantChanged: true,
			wantSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers:   []corev1.Container{{Name: "head"}},
							NodeSelector: map[string]string{},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers:   []corev1.Container{{Name: "worker"}},
								NodeSelector: map[string]string{},
							},
						},
					},
				},
			},
		},
		"no changes when no restoration needed": {
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
			podSetsInfo: []podset.PodSetInfo{
				{},
				{},
			},
			wantChanged: false,
			wantSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "head"}},
						},
					},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{
						GroupName: "workers",
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{Name: "worker"}},
							},
						},
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotChanged := RestorePodSetsInfo(tc.rayClusterSpec, tc.podSetsInfo)

			if gotChanged != tc.wantChanged {
				t.Errorf("Expected changed=%v, got changed=%v", tc.wantChanged, gotChanged)
			}

			if diff := cmp.Diff(tc.wantSpec, tc.rayClusterSpec); diff != "" {
				t.Errorf("Unexpected spec (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreateRayClusterSpec(t *testing.T) {
	testCases := map[string]struct {
		object         client.Object
		rayClusterSpec *rayv1.RayClusterSpec
		wantErrors     field.ErrorList
	}{
		"valid spec": {
			object: testingrayutil.MakeCluster("raycluster", "ns").Obj(),
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{GroupName: "workers"},
				},
			},
			wantErrors: nil,
		},
		"autoscaling enabled without workload slicing": {
			object: testingrayutil.MakeCluster("raycluster", "ns").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayClusterSpec: &rayv1.RayClusterSpec{
				EnableInTreeAutoscaling: ptr.To(true),
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{GroupName: "workers"},
				},
			},
			wantErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec", "enableInTreeAutoscaling"), ptr.To(true), "a kueue managed job should only use autoscaling when workload slicing is enabled"),
			},
		},
		"autoscaling enabled with workload slicing": {
			object: testingrayutil.MakeCluster("raycluster", "ns").
				SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayClusterSpec: &rayv1.RayClusterSpec{
				EnableInTreeAutoscaling: ptr.To(true),
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{GroupName: "workers"},
				},
			},
			wantErrors: nil,
		},
		"too many worker groups": {
			object: testingrayutil.MakeCluster("raycluster", "ns").Obj(),
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{GroupName: "workers1"},
					{GroupName: "workers2"},
					{GroupName: "workers3"},
					{GroupName: "workers4"},
					{GroupName: "workers5"},
					{GroupName: "workers6"},
					{GroupName: "workers7"},
					{GroupName: "workers8"}, // 8 worker groups is too many
				},
			},
			wantErrors: field.ErrorList{
				field.TooMany(field.NewPath("spec", "workerGroupSpecs"), 8, 7),
			},
		},
		"worker group named 'head'": {
			object: testingrayutil.MakeCluster("raycluster", "ns").Obj(),
			rayClusterSpec: &rayv1.RayClusterSpec{
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{GroupName: "head"},
				},
			},
			wantErrors: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)),
			},
		},
		"multiple errors": {
			object: testingrayutil.MakeCluster("raycluster", "ns").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			rayClusterSpec: &rayv1.RayClusterSpec{
				EnableInTreeAutoscaling: ptr.To(true),
				HeadGroupSpec: rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{},
				},
				WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
					{GroupName: "head"},
					{GroupName: "workers2"},
					{GroupName: "workers3"},
					{GroupName: "workers4"},
					{GroupName: "workers5"},
					{GroupName: "workers6"},
					{GroupName: "workers7"},
					{GroupName: "workers8"},
				},
			},
			wantErrors: field.ErrorList{
				field.Invalid(field.NewPath("spec", "enableInTreeAutoscaling"), ptr.To(true), "a kueue managed job should only use autoscaling when workload slicing is enabled"),
				field.TooMany(field.NewPath("spec", "workerGroupSpecs"), 8, 7),
				field.Forbidden(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			gotErrors := ValidateCreate(tc.object, tc.rayClusterSpec, field.NewPath("spec"))

			if diff := cmp.Diff(tc.wantErrors, gotErrors, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("Unexpected errors (-want +got):\n%s", diff)
			}

			// Verify error count
			if len(gotErrors) != len(tc.wantErrors) {
				t.Errorf("Expected %d errors, got %d", len(tc.wantErrors), len(gotErrors))
			}
		})
	}
}

func TestUpdatePodSetsFayCluster_GetError(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	scheme := runtime.NewScheme()
	_ = rayv1.AddToScheme(scheme)

	// Create a client that returns an error for Get operations
	c := fake.NewClientBuilder().
		WithScheme(scheme).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*rayv1.RayCluster); ok {
					return apierrors.NewInternalError(errors.New("simulated get error"))
				}
				return client.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	podSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet(headGroupPodSetName, 1).Obj(),
		*utiltestingapi.MakePodSet("workers", 3).Obj(),
	}

	object := testingrayutil.MakeCluster("raycluster", "ns").
		SetAnnotation("kueue.x-k8s.io/elastic-job", "true").
		WithEnableAutoscaling(ptr.To(true)).
		Obj()

	_, err := UpdatePodSets(t.Context(), podSets, c, object, ptr.To(true), "target-raycluster")

	if err == nil {
		t.Error("Expected error but got none")
		return
	}

	if !apierrors.IsInternalError(err) {
		t.Errorf("Expected InternalError, got: %v", err)
	}
}

func TestComparePodSetCounts(t *testing.T) {
	testCases := map[string]struct {
		podSets         []kueue.PodSet
		referenceCounts map[kueue.PodSetReference]int32
		wantChanged     bool
	}{
		"equal counts": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 3},
			},
			referenceCounts: map[kueue.PodSetReference]int32{
				"head":   1,
				"worker": 3,
			},
			wantChanged: false,
		},
		"different count": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 5},
			},
			referenceCounts: map[kueue.PodSetReference]int32{
				"head":   1,
				"worker": 3,
			},
			wantChanged: true,
		},
		"different length": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 3},
			},
			referenceCounts: map[kueue.PodSetReference]int32{
				"head": 1,
			},
			wantChanged: true,
		},
		"missing podset in reference": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 3},
			},
			referenceCounts: map[kueue.PodSetReference]int32{
				"head":    1,
				"worker2": 3,
			},
			wantChanged: true,
		},
		"empty reference": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
			},
			referenceCounts: map[kueue.PodSetReference]int32{},
			wantChanged:     true,
		},
		"both empty": {
			podSets:         []kueue.PodSet{},
			referenceCounts: map[kueue.PodSetReference]int32{},
			wantChanged:     false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := ComparePodSetCounts(tc.podSets, tc.referenceCounts)
			if got != tc.wantChanged {
				t.Errorf("ComparePodSetCounts() = %v, want %v", got, tc.wantChanged)
			}
		})
	}
}

func TestParsePodSetReplicaSizes(t *testing.T) {
	testCases := map[string]struct {
		annotation string
		wantCounts map[kueue.PodSetReference]int32
		wantErr    bool
	}{
		"empty annotation": {
			annotation: "",
			wantCounts: map[kueue.PodSetReference]int32{},
		},
		"valid annotation": {
			annotation: `[{"name":"head","count":1},{"name":"worker","count":3}]`,
			wantCounts: map[kueue.PodSetReference]int32{
				"head":   1,
				"worker": 3,
			},
		},
		"single podset": {
			annotation: `[{"name":"head","count":1}]`,
			wantCounts: map[kueue.PodSetReference]int32{
				"head": 1,
			},
		},
		"invalid json": {
			annotation: `invalid`,
			wantErr:    true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := ParsePodSetReplicaSizes(tc.annotation)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ParsePodSetReplicaSizes() error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr {
				if diff := cmp.Diff(tc.wantCounts, got); diff != "" {
					t.Errorf("ParsePodSetReplicaSizes() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestSerializePodSetCounts(t *testing.T) {
	testCases := map[string]struct {
		podSets  []kueue.PodSet
		wantJSON string
	}{
		"single podset": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
			},
			wantJSON: `[{"name":"head","count":1}]`,
		},
		"multiple podsets": {
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 5},
			},
			wantJSON: `[{"name":"head","count":1},{"name":"worker","count":5}]`,
		},
		"empty podsets": {
			podSets:  []kueue.PodSet{},
			wantJSON: `[]`,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := SerializePodSetCounts(tc.podSets)
			if err != nil {
				t.Fatalf("SerializePodSetCounts() unexpected error: %v", err)
			}
			if string(got) != tc.wantJSON {
				t.Errorf("SerializePodSetCounts() = %s, want %s", string(got), tc.wantJSON)
			}
		})
	}
}

func TestGetWorkloadslicingCustomAnnotations(t *testing.T) {
	testCases := map[string]struct {
		annotations      map[string]string
		podSets          []kueue.PodSet
		rayClusterName   string
		registerRayType  bool
		createRayCluster bool
		wantAnnotation   map[string]string
		wantErr          bool
	}{
		"workload slicing disabled returns nil": {
			annotations: map[string]string{},
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
			},
			rayClusterName:   "test-raycluster",
			registerRayType:  true,
			createRayCluster: true,
			wantAnnotation:   nil,
		},
		"first call sets annotation": {
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
			},
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 3},
			},
			rayClusterName:   "test-raycluster",
			registerRayType:  true,
			createRayCluster: true,
			wantAnnotation: map[string]string{
				RayClusterGenerationAnnotation:         "0",
				RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"worker","count":3}]`,
			},
		},
		"returns annotations even when counts match existing annotation": {
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey:   workloadslicing.EnabledAnnotationValue,
				RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"worker","count":3}]`,
			},
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 3},
			},
			rayClusterName:   "test-raycluster",
			registerRayType:  true,
			createRayCluster: true,
			wantAnnotation: map[string]string{
				RayClusterGenerationAnnotation:         "0",
				RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"worker","count":3}]`,
			},
		},
		"updated when counts differ": {
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey:   workloadslicing.EnabledAnnotationValue,
				RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"worker","count":3}]`,
			},
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 5},
			},
			rayClusterName:   "test-raycluster",
			registerRayType:  true,
			createRayCluster: true,
			wantAnnotation: map[string]string{
				RayClusterGenerationAnnotation:         "0",
				RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"worker","count":5}]`,
			},
		},
		"raycluster not found returns annotations with empty generation": {
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
			},
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
				{Name: "worker", Count: 3},
			},
			rayClusterName:  "nonexistent-raycluster",
			registerRayType: true,
			wantAnnotation: map[string]string{
				RayClusterGenerationAnnotation:         "",
				RayClusterPodsetReplicaSizesAnnotation: `[{"name":"head","count":1},{"name":"worker","count":3}]`,
			},
		},
		"other get error returns error": {
			annotations: map[string]string{
				workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
			},
			podSets: []kueue.PodSet{
				{Name: "head", Count: 1},
			},
			rayClusterName: "test-raycluster",
			wantErr:        true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)

			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)

			if tc.registerRayType {
				_ = rayv1.AddToScheme(scheme)
			}

			builder := fake.NewClientBuilder().WithScheme(scheme)

			if tc.createRayCluster {
				rayCluster := testingrayutil.MakeCluster("test-raycluster", "default").Obj()
				builder = builder.WithObjects(rayCluster)
			}

			c := builder.Build()

			obj := &metav1.ObjectMeta{
				Name:        "test-object",
				Namespace:   "default",
				Annotations: tc.annotations,
			}
			// Use a corev1.ConfigMap as a simple client.Object wrapper
			cm := &corev1.ConfigMap{ObjectMeta: *obj}

			got, err := GetWorkloadslicingRayClusterCustomAnnotations(t.Context(), c, cm, tc.podSets, tc.rayClusterName)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("GetWorkloadslicingCustomAnnotations() expected error but got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("GetWorkloadslicingCustomAnnotations() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantAnnotation, got); diff != "" {
				t.Errorf("GetWorkloadslicingCustomAnnotations() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
