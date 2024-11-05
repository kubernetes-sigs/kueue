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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/podset"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

func TestPodSets(t *testing.T) {
	testCases := map[string]struct {
		rayJob      *RayJob
		wantPodSets func(rayJob *RayJob) []kueue.PodSet
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
					{
						Name:     headGroupPodSetName,
						Count:    1,
						Template: *rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
					},
					{
						Name:     "group1",
						Count:    1,
						Template: *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.DeepCopy(),
					},
					{
						Name:     "group2",
						Count:    3,
						Template: *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.DeepCopy(),
					},
				}
			},
		},
		"with required topology annotation": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
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
					{
						Name:            headGroupPodSetName,
						Count:           1,
						Template:        *rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/block")},
					},
					{
						Name:            rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName,
						Count:           1,
						Template:        *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.DeepCopy(),
						TopologyRequest: &kueue.PodSetTopologyRequest{Required: ptr.To("cloud.com/block")},
					},
					{
						Name:     rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].GroupName,
						Count:    3,
						Template: *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.DeepCopy(),
					},
				}
			},
		},
		"with preferred topology annotation": {
			rayJob: (*RayJob)(testingrayutil.MakeJob("rayjob", "ns").
				WithHeadGroupSpec(
					rayv1.HeadGroupSpec{
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
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
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
							Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "group2_c"}}},
						},
					},
				).
				Obj()),
			wantPodSets: func(rayJob *RayJob) []kueue.PodSet {
				return []kueue.PodSet{
					{
						Name:            headGroupPodSetName,
						Count:           1,
						Template:        *rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
						TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block")},
					},
					{
						Name:     rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName,
						Count:    1,
						Template: *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.DeepCopy(),
					},
					{
						Name:            rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].GroupName,
						Count:           3,
						Template:        *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.DeepCopy(),
						TopologyRequest: &kueue.PodSetTopologyRequest{Preferred: ptr.To("cloud.com/block")},
					},
				}
			},
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
					{
						Name:     headGroupPodSetName,
						Count:    1,
						Template: *rayJob.Spec.RayClusterSpec.HeadGroupSpec.Template.DeepCopy(),
					},
					{
						Name:     "group1",
						Count:    4,
						Template: *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[0].Template.DeepCopy(),
					},
					{
						Name:     "group2",
						Count:    12,
						Template: *rayJob.Spec.RayClusterSpec.WorkerGroupSpecs[1].Template.DeepCopy(),
					},
				}
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantPodSets(tc.rayJob), tc.rayJob.PodSets()); diff != "" {
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
