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
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestEffectiveResourceGroups(t *testing.T) {
	specFlavor := utiltestingapi.MakeFlavorQuotas("spec-flavor").Resource(corev1.ResourceCPU, "10").FlavorQuotas
	effectiveFlavor := utiltestingapi.MakeFlavorQuotas("effective-flavor").Resource(corev1.ResourceCPU, "20").FlavorQuotas

	cases := map[string]struct {
		dynamicQuota bool
		cq           *kueue.ClusterQueue
		wantRGs      []kueue.ResourceGroup
	}{
		"DynamicQuotaOrchestration disabled returns spec": {
			dynamicQuota: false,
			cq: utiltestingapi.MakeClusterQueue("test-cq").
				ResourceGroup(specFlavor).
				EffectiveQuotas(effectiveFlavor).
				Obj(),
			wantRGs: []kueue.ResourceGroup{
				utiltestingapi.ResourceGroup(specFlavor),
			},
		},
		"DynamicQuotaOrchestration enabled returns status effectiveQuotas when set": {
			dynamicQuota: true,
			cq: utiltestingapi.MakeClusterQueue("test-cq").
				ResourceGroup(specFlavor).
				EffectiveQuotas(effectiveFlavor).
				Obj(),
			wantRGs: []kueue.ResourceGroup{
				utiltestingapi.ResourceGroup(effectiveFlavor),
			},
		},
		"DynamicQuotaOrchestration enabled returns spec when effectiveQuotas is nil": {
			dynamicQuota: true,
			cq: utiltestingapi.MakeClusterQueue("test-cq").
				ResourceGroup(specFlavor).
				Obj(),
			wantRGs: []kueue.ResourceGroup{
				utiltestingapi.ResourceGroup(specFlavor),
			},
		},
		"DynamicQuotaOrchestration enabled returns empty status effectiveQuotas when set to empty": {
			dynamicQuota: true,
			cq: func() *kueue.ClusterQueue {
				cq := utiltestingapi.MakeClusterQueue("test-cq").ResourceGroup(specFlavor).Obj()
				cq.Status.EffectiveQuotas = &kueue.EffectiveQuotaStatus{ResourceGroups: []kueue.ResourceGroup{}}
				return cq
			}(),
			wantRGs: []kueue.ResourceGroup{},
		},
		"nil cluster queue returns nil": {
			dynamicQuota: true,
			cq:           nil,
			wantRGs:      nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, tc.dynamicQuota)

			if diff := cmp.Diff(tc.wantRGs, EffectiveResourceGroups(tc.cq)); diff != "" {
				t.Errorf("Unexpected EffectiveResourceGroups (-want +got):\n%s", diff)
			}
		})
	}
}
