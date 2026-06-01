// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package queue

import (
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

func AllFlavors(rgs []kueue.ResourceGroup) sets.Set[kueue.ResourceFlavorReference] {
	return utilslices.Reduce(
		rgs,
		func(acc sets.Set[kueue.ResourceFlavorReference], rg kueue.ResourceGroup) sets.Set[kueue.ResourceFlavorReference] {
			for _, flavor := range rg.Flavors {
				acc.Insert(flavor.Name)
			}
			return acc
		},
		sets.New[kueue.ResourceFlavorReference](),
	)
}

func GetEffectiveResourceGroup(cq *kueue.ClusterQueue) []kueue.ResourceGroup {
	if features.Enabled(features.EffectiveResourceQuotas) {
		return cq.Status.EffectiveResourceGroups
	}
	return cq.Spec.ResourceGroups
}

// Should be accessed only in MK Cluster Reconciler.
func SetEffectiveResourceGroup(cq *kueue.ClusterQueue, rgs *[]kueue.ResourceGroup) {
	cq.Status.EffectiveResourceGroups = *rgs
}

// Should be accessed only in:
//   * MK ClusterQueue controller,
//   * Core ClusterQueue controller.
func SyncEffectiveResourceGroupsToSpec(cq *kueue.ClusterQueue) (needsUpdate bool) {
	if needsUpdate = !equality.Semantic.DeepEqual(cq.Status.EffectiveResourceGroups, cq.Spec.ResourceGroups); needsUpdate {
		cq.Status.EffectiveResourceGroups = cq.Spec.ResourceGroups
	}
	return
}

// Should be accessed only in MK ClusterQueue controller.
func HasResourceGroupSpec(cq *kueue.ClusterQueue) bool {
	return len(cq.Spec.ResourceGroups) > 0
}
