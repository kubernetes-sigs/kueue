/*
Copyright 2023 The Kubernetes Authors.

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
	rayjobapi "github.com/ray-project/kuberay/ray-operator/apis/ray/v1alpha1"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

func TestPodSets(t *testing.T) {
	job := testingrayutil.MakeJob("job", "ns").
		WithHeadGroupSpec(
			rayjobapi.HeadGroupSpec{
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "head_c",
							},
						},
					},
				},
			},
		).
		WithWorkerGroups(
			rayjobapi.WorkerGroupSpec{
				GroupName: "group1",
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "group1_c",
							},
						},
					},
				},
			},
			rayjobapi.WorkerGroupSpec{
				GroupName: "group2",
				Replicas:  pointer.Int32(3),
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "group2_c",
							},
						},
					},
				},
			},
		).
		Obj()

	wantPodSets := []kueue.PodSet{
		{
			Name:  "head",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "head_c",
						},
					},
				},
			},
		},
		{
			Name:  "group1",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "group1_c",
						},
					},
				},
			},
		},
		{
			Name:  "group2",
			Count: 3,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "group2_c",
						},
					},
				},
			},
		},
	}

	result := ((*RayJob)(job)).PodSets()

	if diff := cmp.Diff(wantPodSets, result); diff != "" {
		t.Errorf("PodSets() mismatch (-want +got):\n%s", diff)
	}
}

func TestPriorityClass(t *testing.T) {
	cases := map[string]struct {
		job           *rayjobapi.RayJob
		wantClassName string
	}{
		"none": {
			job: testingrayutil.MakeJob("job", "ns").Obj(),
		},
		"from head": {
			job:           testingrayutil.MakeJob("job", "ns").WithPriorityClassName("head-prio-class").WithWorkerPriorityClassName("worker-prio-class").Obj(),
			wantClassName: "head-prio-class",
		},
		"from worker": {
			job:           testingrayutil.MakeJob("job", "ns").WithWorkerPriorityClassName("worker-prio-class").Obj(),
			wantClassName: "worker-prio-class",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := ((*RayJob)(tc.job)).PriorityClass()
			if diff := cmp.Diff(tc.wantClassName, got); diff != "" {
				t.Errorf("PriorityClass() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestNodeSelectors(t *testing.T) {

	job := (*RayJob)(testingrayutil.MakeJob("job", "ns").
		WithHeadGroupSpec(rayjobapi.HeadGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{},
				},
			},
		}).
		WithWorkerGroups(rayjobapi.WorkerGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"key-wg1": "value-wg1",
					},
				},
			},
		}, rayjobapi.WorkerGroupSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector: map[string]string{
						"key-wg2": "value-wg2",
					},
				},
			},
		}).
		Obj())

	// RunWithPodSetsInfo should append or update the node selectors
	job.RunWithPodSetsInfo([]jobframework.PodSetInfo{
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
		{
			NodeSelector: map[string]string{
				// don't add anything
			},
		},
	})

	if diff := cmp.Diff(
		map[string]string{
			"newKey": "newValue",
		},
		job.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector); diff != "" {
		t.Errorf("head node selectors mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(
		map[string]string{
			"key-wg1": "updated-value-wg1",
		},
		job.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector); diff != "" {
		t.Errorf("wg1 node selectors mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(
		map[string]string{
			"key-wg2": "value-wg2",
		},
		job.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.NodeSelector); diff != "" {
		t.Errorf("wg2 node selectors mismatch (-want +got):\n%s", diff)
	}

	// restore should replace node selectors
	job.RestorePodSetsInfo([]jobframework.PodSetInfo{
		{
			NodeSelector: map[string]string{
				// clean it all
			},
		},
		{
			NodeSelector: map[string]string{
				"key-wg1": "restored-value-wg1",
			},
		},
		{
			NodeSelector: map[string]string{
				"key-wg2-2": "value-wg2-2",
			},
		},
	})

	if diff := cmp.Diff(
		map[string]string{},
		job.Spec.RayClusterSpec.HeadGroupSpec.Template.Spec.NodeSelector); diff != "" {
		t.Errorf("head node selectors mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(
		map[string]string{
			"key-wg1": "restored-value-wg1",
		},
		job.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.Spec.NodeSelector); diff != "" {
		t.Errorf("wg1 node selectors mismatch (-want +got):\n%s", diff)
	}

	if diff := cmp.Diff(
		map[string]string{
			"key-wg2-2": "value-wg2-2",
		},
		job.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.Spec.NodeSelector); diff != "" {
		t.Errorf("wg2 node selectors mismatch (-want +got):\n%s", diff)
	}
}
