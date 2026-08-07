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

package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestFlavorsByResourceFrom(t *testing.T) {
	cases := map[string]struct {
		clusterQueue *kueue.ClusterQueue
		want         map[corev1.ResourceName]kueue.ResourceFlavorReference
	}{
		"returns the flavor from each matching resource group": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("cpu-flavor").
						Resource(corev1.ResourceCPU, "10", "0").
						Resource(corev1.ResourceMemory, "10Gi", "0").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("gpu-flavor").
						Resource(corev1.ResourceName("nvidia.com/gpu"), "10", "0").
						Obj(),
				).Obj(),
			want: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU:                    "cpu-flavor",
				corev1.ResourceMemory:                 "cpu-flavor",
				corev1.ResourceName("nvidia.com/gpu"): "gpu-flavor",
			},
		},
		"returns the first flavor from a resource group with several flavors": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceName("nvidia.com/gpu"), "10", "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("spot").
						Resource(corev1.ResourceName("nvidia.com/gpu"), "10", "0").
						Obj(),
				).Obj(),
			want: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceName("nvidia.com/gpu"): "on-demand",
			},
		},
		"omits resources not covered by any resource group": {
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("cpu-flavor").
						Resource(corev1.ResourceCPU, "10", "0").
						Obj(),
				).Obj(),
			want: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "cpu-flavor",
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := flavorsByResourceFrom(resourceGroupsFrom(tc.clusterQueue))
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected flavors (-want/+got)\n%s", diff)
			}
		})
	}
}
