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

package deployment

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		deployment *appsv1.Deployment
		want       *appsv1.Deployment
	}{
		"deployment without queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			want:       testingdeployment.MakeDeployment("test-pod", "").Obj(),
		},
		"deployment with queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()

			w := &Webhook{
				client: client,
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			if err := w.Default(ctx, tc.deployment); err != nil {
				t.Errorf("failed to set defaults for v1/deployment: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.deployment); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		deployment *appsv1.Deployment
		wantErr    error
		wantWarns  admission.Warnings
	}{
		"without queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
		},
		"valid queue name": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
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

			warns, err := w.ValidateCreate(ctx, tc.deployment)
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
		oldDeployment *appsv1.Deployment
		newDeployment *appsv1.Deployment
		wantErr       error
		wantWarns     admission.Warnings
	}{
		"without queue": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
		},
		"with queue (no changes)": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"with queue": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
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

			warns, err := w.ValidateUpdate(ctx, tc.oldDeployment, tc.newDeployment)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}
