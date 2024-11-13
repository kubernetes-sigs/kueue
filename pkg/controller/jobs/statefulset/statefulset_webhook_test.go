/*
Copyright 2024 The Kubernetes Authors.

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

package statefulset

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		statefulset                *appsv1.StatefulSet
		manageJobsWithoutQueueName bool
		enableIntegrations         []string
		want                       *appsv1.StatefulSet
	}{
		"statefulset with queue": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(10).
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				Obj(),
		},
		"statefulset without replicas": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(1).
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()

			w := &Webhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			if err := w.Default(ctx, tc.statefulset); err != nil {
				t.Errorf("failed to set defaults for v1/statefulset: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.statefulset); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		sts       *appsv1.StatefulSet
		wantErr   error
		wantWarns admission.Warnings
	}{
		"without queue": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
		},
		"valid queue name": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			w := &Webhook{client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateCreate(ctx, tc.sts)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		oldObj  *appsv1.StatefulSet
		newObj  *appsv1.StatefulSet
		wantErr error
	}{
		"no changes": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
						pod.GroupNameLabel:   "group1",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
						pod.GroupNameLabel:   "group1",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			wantErr: nil,
		},
		"change in queue label": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel: "queue2",
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in pod template queue label": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue2",
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: podSpecQueueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in group name label": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						pod.GroupNameLabel: "group1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						pod.GroupNameLabel: "group2",
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: groupNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale down to zero)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
			},
		},
		"change in replicas (scale up from zero)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
		},
		"change in replicas (scale up while the previous scaling operation is still in progress)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
				Status: appsv1.StatefulSetStatus{
					Replicas: 3,
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
				Status: appsv1.StatefulSetStatus{
					Replicas: 1,
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale up)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(4)),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			wh := &Webhook{}

			_, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
