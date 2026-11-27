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

package kubeflowjob

import (
	"testing"

	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"go.uber.org/mock/gomock"
	corev1 "k8s.io/api/core/v1"

	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobs/kubeflow/kubeflowjob"
)

func TestIsActive(t *testing.T) {
	testCases := map[string]struct {
		jobStatus *kftraining.JobStatus
		want      bool
	}{
		"nil job status": {
			jobStatus: nil,
			want:      false,
		},
		"no active replicas": {
			jobStatus: &kftraining.JobStatus{
				ReplicaStatuses: map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.ReplicaType("Worker"): {
						Active: 0,
					},
				},
			},
			want: false,
		},
		"has active replicas": {
			jobStatus: &kftraining.JobStatus{
				ReplicaStatuses: map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.ReplicaType("Worker"): {
						Active: 2,
					},
				},
			},
			want: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockKFJobControl := mocks.NewMockKFJobControl(ctrl)
			mockKFJobControl.EXPECT().JobStatus().Return(tc.jobStatus)

			job := &KubeflowJob{
				KFJobControl: mockKFJobControl,
			}
			if got := job.IsActive(); got != tc.want {
				t.Errorf("IsActive() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodsReady(t *testing.T) {
	testCases := map[string]struct {
		jobStatus *kftraining.JobStatus
		want      bool
	}{
		"nil job status": {
			jobStatus: nil,
			want:      false,
		},
		"no conditions": {
			jobStatus: &kftraining.JobStatus{},
			want:      false,
		},
		"job running condition false": {
			jobStatus: &kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
						Status: corev1.ConditionFalse,
					},
				},
			},
			want: false,
		},
		"job running condition true": {
			jobStatus: &kftraining.JobStatus{
				Conditions: []kftraining.JobCondition{
					{
						Type:   kftraining.JobRunning,
						Status: corev1.ConditionTrue,
					},
				},
			},
			want: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			mockKFJobControl := mocks.NewMockKFJobControl(ctrl)
			mockKFJobControl.EXPECT().JobStatus().Return(tc.jobStatus)

			job := &KubeflowJob{
				KFJobControl: mockKFJobControl,
			}
			if got := job.PodsReady(t.Context(), nil); got != tc.want {
				t.Errorf("PodsReady() = %v, want %v", got, tc.want)
			}
		})
	}
}
