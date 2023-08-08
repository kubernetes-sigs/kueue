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
	"github.com/google/go-cmp/cmp/cmpopts"
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

func TestNodeSelectors(t *testing.T) {
	baseJob := testingrayutil.MakeJob("job", "ns").
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
		Obj()

	cases := map[string]struct {
		job          *rayjobapi.RayJob
		runInfo      []jobframework.PodSetInfo
		restoreInfo  []jobframework.PodSetInfo
		wantRunError error
		wantAfterRun *rayjobapi.RayJob
		wantFinal    *rayjobapi.RayJob
	}{
		"valid configuration": {
			job: baseJob.DeepCopy(),
			runInfo: []jobframework.PodSetInfo{
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
			},
			restoreInfo: []jobframework.PodSetInfo{
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
				WithHeadGroupSpec(rayjobapi.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"newKey": "newValue",
							},
						},
					},
				}).
				WithWorkerGroups(rayjobapi.WorkerGroupSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							NodeSelector: map[string]string{
								"key-wg1": "updated-value-wg1",
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
				Obj(),

			wantFinal: baseJob.DeepCopy(),
		},
		"invalid runInfo": {
			job: baseJob.DeepCopy(),
			runInfo: []jobframework.PodSetInfo{
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
			wantRunError: jobframework.ErrInvalidPodsetInfo,
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
