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
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
			warns, err := webhook.ValidateCreate(t.Context(), tc.service)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateCreate() error = %v, wantErr %v", err, tc.wantErr)
			}
			if diff := cmp.Diff(admission.Warnings(nil), warns); diff != "" {
				t.Errorf("ValidateCreate() warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		oldService     *rayv1.RayService
		newService     *rayv1.RayService
		defaultLqExist bool
		featureGates   map[featuregate.Feature]bool
		wantErr        error
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
		"queue name unchanged while unsuspended": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Label("test-label", "test-value").
				Obj(),
			wantErr: nil,
		},
		"queue name should not change while unsuspended": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue2").
				Suspend(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), kueue.LocalQueueName("queue2"), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"queue name can change while suspended": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue2").
				Suspend(true).
				Obj(),
			wantErr: nil,
		},
		"queue name removal is rejected when the job is unsuspended and ValidateRayAndSparkJobUpdates is enabled": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(false).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), kueue.LocalQueueName(""), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"queue name removal is allowed when the job is unsuspended and ValidateRayAndSparkJobUpdates is disabled": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(false).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: false},
			wantErr:      nil,
		},
		"queue name removal is rejected when the job is suspended in a namespace with a default queue and ValidateRayAndSparkJobUpdates is enabled": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue(string(constants.DefaultLocalQueueName)).
				Suspend(true).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(true).
				Obj(),
			defaultLqExist: true,
			featureGates:   map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), "", "queue-name must not be empty in namespace with default queue"),
			}.ToAggregate(),
		},
		"queue name removal is allowed when the job is suspended in a namespace with a default queue and ValidateRayAndSparkJobUpdates is disabled": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue(string(constants.DefaultLocalQueueName)).
				Suspend(true).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(true).
				Obj(),
			defaultLqExist: true,
			featureGates:   map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: false},
			wantErr:        nil,
		},
		"queue name removal is allowed when the job is suspended in a namespace without a default queue and ValidateRayAndSparkJobUpdates is enabled": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(true).
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr:      nil,
		},
		"prebuilt workload name change is not validated when the job is unmanaged and ValidateRayAndSparkJobUpdates is enabled": {
			oldService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(false).
				PrebuiltWorkloadLabel("wl1").
				Obj(),
			newService: testingrayservice.MakeService("rayservice", "ns").
				Suspend(false).
				PrebuiltWorkloadLabel("wl2").
				Obj(),
			featureGates: map[featuregate.Feature]bool{features.ValidateRayAndSparkJobUpdates: true},
			wantErr:      nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			ctx, _ := utiltesting.ContextWithLog(t)
			cli := utiltesting.NewClientBuilder().Build()
			cqCache := schdcache.New(cli)
			queueManager := qcache.NewManagerForUnitTests(cli, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue(
					string(constants.DefaultLocalQueueName), "ns").ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("Failed to add the default LocalQueue: %v", err)
				}
			}
			webhook := &RayServiceWebhook{
				queues: queueManager,
				cache:  cqCache,
			}
			warnings, err := webhook.ValidateUpdate(ctx, tc.oldService, tc.newService)
			if diff := cmp.Diff(tc.wantErr, err); diff != "" {
				t.Errorf("ValidateUpdate() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(admission.Warnings(nil), warnings); diff != "" {
				t.Errorf("ValidateUpdate() warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
