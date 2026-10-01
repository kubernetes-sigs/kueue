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

package v1beta2

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestMatchExpressionWrapper(t *testing.T) {
	testCases := map[string]struct {
		wrapper *MatchExpressionWrapper
		want    metav1.LabelSelectorRequirement
	}{
		"empty": {
			wrapper: MakeMatchExpression(),
			want:    metav1.LabelSelectorRequirement{},
		},
		"key, operator and values": {
			wrapper: MakeMatchExpression().
				Key(corev1.LabelMetadataName).
				Operator(metav1.LabelSelectorOpNotIn).
				Values("unmanaged-ns", "kube-system"),
			want: metav1.LabelSelectorRequirement{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"unmanaged-ns", "kube-system"},
			},
		},
		"operator without values": {
			wrapper: MakeMatchExpression().
				Key("foo").
				Operator(metav1.LabelSelectorOpExists),
			want: metav1.LabelSelectorRequirement{
				Key:      "foo",
				Operator: metav1.LabelSelectorOpExists,
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.wrapper.Obj()); diff != "" {
				t.Errorf("unexpected requirement (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestManagedJobsNamespaceSelectorWrapper(t *testing.T) {
	notInKubeSystem := MakeMatchExpression().
		Key(corev1.LabelMetadataName).
		Operator(metav1.LabelSelectorOpNotIn).
		Values("kube-system").
		Obj()
	existsFoo := MakeMatchExpression().
		Key("foo").
		Operator(metav1.LabelSelectorOpExists).
		Obj()

	testCases := map[string]struct {
		wrapper *ManagedJobsNamespaceSelectorWrapper
		want    *metav1.LabelSelector
	}{
		"no expressions": {
			wrapper: MakeManagedJobsNamespaceSelector(),
			want:    &metav1.LabelSelector{},
		},
		"single expression": {
			wrapper: MakeManagedJobsNamespaceSelector().MatchExpressions(notInKubeSystem),
			want: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{notInKubeSystem},
			},
		},
		"multiple expressions in one call": {
			wrapper: MakeManagedJobsNamespaceSelector().MatchExpressions(notInKubeSystem, existsFoo),
			want: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{notInKubeSystem, existsFoo},
			},
		},
		"expressions accumulate across calls": {
			wrapper: MakeManagedJobsNamespaceSelector().
				MatchExpressions(notInKubeSystem).
				MatchExpressions(existsFoo),
			want: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{notInKubeSystem, existsFoo},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.want, tc.wrapper.Obj()); diff != "" {
				t.Errorf("unexpected selector (-want,+got):\n%s", diff)
			}
		})
	}
}
