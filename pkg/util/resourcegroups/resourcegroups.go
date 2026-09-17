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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
)

type ResourceGroup struct {
	CoveredResources sets.Set[corev1.ResourceName]
	Flavors          []kueue.ResourceFlavorReference
}

func (rg *ResourceGroup) Clone() ResourceGroup {
	return ResourceGroup{
		CoveredResources: rg.CoveredResources.Clone(),
		Flavors:          rg.Flavors,
	}
}

// CoversAnyResource reports whether any ResourceGroup in rgs covers
// at least one of the given resource names.
func CoversAnyResource(rgs []ResourceGroup, resourceNames sets.Set[corev1.ResourceName]) bool {
	for _, rg := range rgs {
		for name := range resourceNames {
			if rg.CoveredResources.Has(name) {
				return true
			}
		}
	}
	return false
}

// AllCoveredResources returns the union of CoveredResources across all ResourceGroups.
func AllCoveredResources(rgs []ResourceGroup) sets.Set[corev1.ResourceName] {
	covered := sets.New[corev1.ResourceName]()
	for _, rg := range rgs {
		covered = covered.Union(rg.CoveredResources)
	}
	return covered
}

// RGByResource returns the ResourceGroup that covers the given resource, or nil.
func RGByResource(rgs []ResourceGroup, resource corev1.ResourceName) *ResourceGroup {
	for i := range rgs {
		if rgs[i].CoveredResources.Has(resource) {
			return &rgs[i]
		}
	}
	return nil
}

func AllFlavors(rgs []ResourceGroup) sets.Set[kueue.ResourceFlavorReference] {
	return utilslices.Reduce(
		rgs,
		func(acc sets.Set[kueue.ResourceFlavorReference], rg ResourceGroup) sets.Set[kueue.ResourceFlavorReference] {
			return acc.Insert(rg.Flavors...)
		},
		sets.New[kueue.ResourceFlavorReference](),
	)
}
