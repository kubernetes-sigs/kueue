/*
Copyright 2022 The Kubernetes Authors.
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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

const (
	testPodTemplateName      = "test-podtemplate"
	testPodTemplateNamespace = "test-ns"
)

func TestValidatePodTemplate(t *testing.T) {
	specPath := field.NewPath("template").Child("spec")
	testCases := map[string]struct {
		podtemplate *corev1.PodTemplate
		wantErr     field.ErrorList
	}{
		"should have valid priorityClassName": {
			podtemplate: testingutil.MakePodTemplate(testPodTemplateName, testPodTemplateNamespace).
				PriorityClass("invalid_class").
				Priority(0).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("priorityClassName"), nil, ""),
			},
		},
		"should pass validation when priorityClassName is empty": {
			podtemplate: testingutil.MakePodTemplate(testPodTemplateName, testPodTemplateNamespace).Obj(),
			wantErr:     nil,
		},
		"should have priority once priorityClassName is set": {
			podtemplate: testingutil.MakePodTemplate(testPodTemplateName, testPodTemplateNamespace).
				PriorityClass("priority").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("priority"), nil, ""),
			},
		},
		"should not request num-pods resource": {
			podtemplate: testingutil.MakePodTemplate(testPodTemplateName, testPodTemplateNamespace).
				Containers([]corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourcePods: resource.MustParse("1"),
							},
						},
					},
				}...).
				InitContainers([]corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{
								corev1.ResourcePods: resource.MustParse("1"),
							},
						},
					},
				}...).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specPath.Child("initContainers").Index(0).Child("resources", "requests").Key(string(corev1.ResourcePods)), nil, ""),
				field.Invalid(specPath.Child("containers").Index(0).Child("resources", "requests").Key(string(corev1.ResourcePods)), nil, ""),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := ValidatePodTemplate(tc.podtemplate)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateWorkload() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
