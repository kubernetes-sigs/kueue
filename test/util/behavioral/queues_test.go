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

package behavioral

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestEffectiveQuotas(t *testing.T) {
	eq := &kueue.EffectiveQuotaStatus{
		ResourceGroups: []kueue.ResourceGroup{
			{
				CoveredResources: []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
				Flavors: []kueue.FlavorQuotas{
					*utiltestingapi.MakeFlavorQuotas("a").Resource(corev1.ResourceCPU, "2").Resource(corev1.ResourceMemory, "1Gi").Obj(),
					*utiltestingapi.MakeFlavorQuotas("b").Resource(corev1.ResourceCPU, "0").Obj(),
				},
			},
		},
	}
	testCases := map[string]struct {
		eq         *kueue.EffectiveQuotaStatus
		flavor     kueue.ResourceFlavorReference
		wantQuotas map[corev1.ResourceName]string
		wantSet    sets.Set[corev1.ResourceName]
	}{
		"nil effective quotas": {
			flavor:     "a",
			wantQuotas: map[corev1.ResourceName]string{},
			wantSet:    sets.New[corev1.ResourceName](),
		},
		"flavor with several resources": {
			eq:         eq,
			flavor:     "a",
			wantQuotas: map[corev1.ResourceName]string{corev1.ResourceCPU: "2", corev1.ResourceMemory: "1Gi"},
			wantSet:    sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
		},
		"zero quota is still reported": {
			eq:         eq,
			flavor:     "b",
			wantQuotas: map[corev1.ResourceName]string{corev1.ResourceCPU: "0"},
			wantSet:    sets.New(corev1.ResourceCPU),
		},
		"unknown flavor": {
			eq:         eq,
			flavor:     "c",
			wantQuotas: map[corev1.ResourceName]string{},
			wantSet:    sets.New[corev1.ResourceName](),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantQuotas, EffectiveNominalQuotas(tc.eq, tc.flavor)); diff != "" {
				t.Errorf("Unexpected nominal quotas (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantSet, EffectiveQuotaResources(tc.eq, tc.flavor)); diff != "" {
				t.Errorf("Unexpected resources (-want,+got):\n%s", diff)
			}
		})
	}
}
