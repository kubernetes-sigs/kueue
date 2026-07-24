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
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
)

func TestValidateCreate(t *testing.T) {
	tooManyWorkerGroups := testingraycluster.MakeWorkerGroups(jobframework.MaxPodSets)

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
						WorkerGroupSpecs: tooManyWorkerGroups,
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
						EnableInTreeAutoscaling: new(true),
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
			_, err := webhook.ValidateCreate(t.Context(), tc.service)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		oldService *rayv1.RayService
		newService *rayv1.RayService
		wantErr    error
	}{
		"valid update": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			wantErr: nil,
		},
		"queue name should not be removed while unsuspended": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), kueue.LocalQueueName(""), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cli := utiltesting.NewClientBuilder().Build()
			cqCache := schdcache.New(cli)
			queueManager := qcache.NewManagerForUnitTests(cli, cqCache)
			webhook := &RayServiceWebhook{
				queues: queueManager,
				cache:  cqCache,
			}
			_, err := webhook.ValidateUpdate(ctx, tc.oldService, tc.newService)
			if diff := cmp.Diff(tc.wantErr, err); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
