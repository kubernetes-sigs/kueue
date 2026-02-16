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
	"context"
	"testing"

	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
)

func TestValidateCreate(t *testing.T) {
	ctx := context.Background()
	testCases := map[string]struct {
		service   *rayv1.RayService
		manageAll bool
		wantErr   bool
	}{
		"valid rayservice": {
			service: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Labels: map[string]string{
						constants.QueueLabel: "queue",
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
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
			},
			manageAll: false,
			wantErr:   false,
		},
		"too many worker groups": {
			service: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Labels: map[string]string{
						constants.QueueLabel: "queue",
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "head"}},
								},
							},
						},
						WorkerGroupSpecs: []rayv1.WorkerGroupSpec{
							{GroupName: "w1"},
							{GroupName: "w2"},
							{GroupName: "w3"},
							{GroupName: "w4"},
							{GroupName: "w5"},
							{GroupName: "w6"},
							{GroupName: "w7"},
							{GroupName: "w8"}, // 8th worker group - too many
						},
					},
				},
			},
			manageAll: false,
			wantErr:   true,
		},
		"autoscaling without elastic jobs feature": {
			service: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Labels: map[string]string{
						constants.QueueLabel: "queue",
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						EnableInTreeAutoscaling: ptr.To(true),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "head"}},
								},
							},
						},
					},
				},
			},
			manageAll: false,
			wantErr:   true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			webhook := &RayServiceWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, err := webhook.ValidateCreate(ctx, tc.service)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	ctx := context.Background()
	testCases := map[string]struct {
		oldService *rayv1.RayService
		newService *rayv1.RayService
		wantErr    bool
	}{
		"valid update": {
			oldService: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Labels: map[string]string{
						constants.QueueLabel: "queue",
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						Suspend: ptr.To(true),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "head"}},
								},
							},
						},
					},
				},
			},
			newService: &rayv1.RayService{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "rayservice",
					Namespace: "ns",
					Labels: map[string]string{
						constants.QueueLabel: "queue",
					},
				},
				Spec: rayv1.RayServiceSpec{
					RayClusterSpec: rayv1.RayClusterSpec{
						Suspend: ptr.To(false),
						HeadGroupSpec: rayv1.HeadGroupSpec{
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{{Name: "head"}},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			webhook := &RayServiceWebhook{}
			_, err := webhook.ValidateUpdate(ctx, tc.oldService, tc.newService)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateUpdate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
