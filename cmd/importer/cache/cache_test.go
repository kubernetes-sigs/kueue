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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
		"returns only resources covered by resource groups": {
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

func TestValidateFlavors(t *testing.T) {
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("spot").
				Resource(corev1.ResourceCPU, "10", "0").
				Obj(),
			*utiltestingapi.MakeFlavorQuotas("on-demand").
				Resource(corev1.ResourceCPU, "10", "0").
				Obj(),
			*utiltestingapi.MakeFlavorQuotas("reserved").
				Resource(corev1.ResourceCPU, "10", "0").
				Obj(),
		).Obj()

	rgs := resourceGroupsFrom(cq)

	t.Run("returns nil when all referenced flavors exist", func(t *testing.T) {
		err := validateFlavors(cq.Name, rgs, map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
			"on-demand": {ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
			"reserved":  {ObjectMeta: metav1.ObjectMeta{Name: "reserved"}},
			"spot":      {ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
		})
		if err != nil {
			t.Fatalf("validateFlavors() = %v, want nil", err)
		}
	})

	t.Run("returns all missing flavors in deterministic order", func(t *testing.T) {
		err := validateFlavors(cq.Name, rgs, map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
			"spot": {ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
		})
		if !errors.Is(err, ErrCQInvalid) {
			t.Fatalf("validateFlavors() error = %v, want wrapped %v", err, ErrCQInvalid)
		}
		want := "\"cq\" missing flavors [on-demand reserved]: clusterqueue invalid"
		if got := err.Error(); got != want {
			t.Fatalf("validateFlavors() error = %q, want %q", got, want)
		}
	})
}
