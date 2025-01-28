/*
Copyright 2025 The Kubernetes Authors.

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

package jobframework

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
)

var (
	testPath = field.NewPath("spec")
)

func TestValidateImmutablePodSpec(t *testing.T) {
	testCases := map[string]struct {
		newPodSpec corev1.PodSpec
		oldPodSpec corev1.PodSpec
		wantErr    error
	}{
		"add container": {
			oldPodSpec: corev1.PodSpec{},
			newPodSpec: corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("containers").String(),
				},
			}.ToAggregate(),
		},
		"remove container": {
			oldPodSpec: corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}},
			newPodSpec: corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("containers").String(),
				},
			}.ToAggregate(),
		},
		"change image on container": {
			oldPodSpec: corev1.PodSpec{Containers: []corev1.Container{{Image: "other"}}},
			newPodSpec: corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}},
		},
		"change request on container": {
			oldPodSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("100m"),
							},
						},
					},
				},
			},
			newPodSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("200m"),
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"add init container": {
			oldPodSpec: corev1.PodSpec{},
			newPodSpec: corev1.PodSpec{InitContainers: []corev1.Container{{Image: "busybox"}}},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("initContainers").String(),
				},
			}.ToAggregate(),
		},
		"remove init container": {
			oldPodSpec: corev1.PodSpec{InitContainers: []corev1.Container{{Image: "busybox"}}},
			newPodSpec: corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("initContainers").String(),
				},
			}.ToAggregate(),
		},
		"change request on init container": {
			oldPodSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("100m"),
							},
						},
					},
				},
			},
			newPodSpec: corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("initContainers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"change nodeTemplate": {
			oldPodSpec: corev1.PodSpec{},
			newPodSpec: corev1.PodSpec{
				NodeSelector: map[string]string{"key": "value"},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("nodeSelector").String(),
				},
			}.ToAggregate(),
		},
		"add toleration": {
			oldPodSpec: corev1.PodSpec{},
			newPodSpec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("tolerations").String(),
				},
			}.ToAggregate(),
		},
		"change toleration": {
			oldPodSpec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			newPodSpec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "new",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("tolerations").String(),
				},
			}.ToAggregate(),
		},
		"delete toleration": {
			oldPodSpec: corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			newPodSpec: corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("tolerations").String(),
				},
			}.ToAggregate(),
		},
		"change runtimeClassName": {
			oldPodSpec: corev1.PodSpec{
				RuntimeClassName: ptr.To("new"),
			},
			newPodSpec: corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("runtimeClassName").String(),
				},
			}.ToAggregate(),
		},
		"change priority": {
			oldPodSpec: corev1.PodSpec{},
			newPodSpec: corev1.PodSpec{
				Priority: ptr.To[int32](1),
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("priority").String(),
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := ValidateImmutablePodGroupPodSpec(tc.newPodSpec, tc.oldPodSpec, testPath)
			if diff := cmp.Diff(tc.wantErr, gotErr.ToAggregate(), cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
