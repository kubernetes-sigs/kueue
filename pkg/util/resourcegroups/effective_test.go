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

package resourcegroups

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestEffectiveResourceGroups(t *testing.T) {
	specFlavor := utiltestingapi.MakeFlavorQuotas("spec-flavor").Resource(corev1.ResourceCPU, "10").FlavorQuotas

	cases := map[string]struct {
		cq      *kueue.ClusterQueue
		wantRGs []kueue.ResourceGroup
	}{
		"cluster queue returns spec resource groups": {
			cq: utiltestingapi.MakeClusterQueue("test-cq").
				ResourceGroup(specFlavor).
				Obj(),
			wantRGs: []kueue.ResourceGroup{
				utiltestingapi.ResourceGroup(specFlavor),
			},
		},
		"nil cluster queue returns nil": {
			cq:      nil,
			wantRGs: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantRGs, EffectiveResourceGroups(tc.cq)); diff != "" {
				t.Errorf("Unexpected EffectiveResourceGroups (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEffectiveCohortResourceGroups(t *testing.T) {
	specFlavor := utiltestingapi.MakeFlavorQuotas("spec-flavor").Resource(corev1.ResourceCPU, "10").FlavorQuotas

	cases := map[string]struct {
		cohort  *kueue.Cohort
		wantRGs []kueue.ResourceGroup
	}{
		"cohort returns spec resource groups": {
			cohort: utiltestingapi.MakeCohort("test-cohort").
				ResourceGroup(specFlavor).
				Obj(),
			wantRGs: []kueue.ResourceGroup{
				utiltestingapi.ResourceGroup(specFlavor),
			},
		},
		"nil cohort returns nil": {
			cohort:  nil,
			wantRGs: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if diff := cmp.Diff(tc.wantRGs, EffectiveCohortResourceGroups(tc.cohort)); diff != "" {
				t.Errorf("Unexpected EffectiveCohortResourceGroups (-want +got):\n%s", diff)
			}
		})
	}
}
