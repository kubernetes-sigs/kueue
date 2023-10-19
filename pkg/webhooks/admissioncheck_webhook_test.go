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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestAdmissionCheckValidation(t *testing.T) {
	testcases := map[string]struct {
		ac      *kueue.AdmissionCheck
		wantErr field.ErrorList
	}{
		"no controller name": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "controllerName"), "", "")},
		},
		"bad ref api group": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group/Bad",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "apiGroup"), "ref.api.group/Bad", "")},
		},
		"no ref api group": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						Kind: "RefKind",
						Name: "ref-name",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "apiGroup"), "", "")},
		},
		"bad ref kind": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind/Bad",
						Name:     "ref-name",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "kind"), "RefKind/Bad", "")},
		},
		"no ref kind": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Name:     "ref-name",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "kind"), "", "")},
		},
		"bad ref name": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name/Bad",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "name"), "ref-name/Bad", "")},
		},
		"no ref name": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "name"), "", "")},
		},
		"no parameters": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
				},
			},
		},
		"valid": {
			ac: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			gotErr := validateAdmissionCheck(tc.ac)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
				t.Errorf("validateAdmissionCheck() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAdmissionCheckUpdateValidation(t *testing.T) {
	testcases := map[string]struct {
		oldAc   *kueue.AdmissionCheck
		newAc   *kueue.AdmissionCheck
		wantErr field.ErrorList
	}{
		"can change parameters": {
			oldAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			newAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group2",
						Kind:     "RefKind2",
						Name:     "ref-name2",
					},
				},
			},
		},
		"can remove parameters": {
			oldAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			newAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
				},
			},
		},
		"cannot break parameters": {
			oldAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			newAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "parameters", "name"), "", "")},
		},
		"cannot change the controller": {
			oldAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			newAc: &kueue.AdmissionCheck{
				Spec: kueue.AdmissionCheckSpec{
					ControllerName: "controller-name2",
					Parameters: &kueue.AdmissionCheckParametersReference{
						APIGroup: "ref.api.group",
						Kind:     "RefKind",
						Name:     "ref-name",
					},
				},
			},
			wantErr: field.ErrorList{field.Invalid(field.NewPath("spec", "controllerName"), "controller-name", "")},
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			gotErr := validateAdmissionCheckUpdate(tc.oldAc, tc.newAc)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
				t.Errorf("validateAdmissionCheckUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
