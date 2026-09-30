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

package flavorassigner

import (
	"slices"

	corev1 "k8s.io/api/core/v1"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/util/tas"
)

// delegateDRABackedExtendedResources zeroes the PodSet's DRA-backed extended resources so
// a domain's capacity cannot decide them, and returns them with the untouched request. A
// node publishing one through a device plugin has capacity worth counting, so the original
// survives for those. Nil when the PodSet asks for none.
func delegateDRABackedExtendedResources(spec *corev1.PodSpec, erCache *dra.ExtendedResourceCache, requests resources.Requests) *schdcache.DRADelegation {
	if !features.Enabled(features.KueueDRADeviceFeasibility) || erCache == nil {
		return nil
	}
	var names []corev1.ResourceName
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for i := range containers {
			for name, quantity := range containers[i].Resources.Requests {
				if !quantity.IsZero() && utilresource.IsExtendedResourceName(name) && erCache.Has(name) && !slices.Contains(names, name) {
					names = append(names, name)
				}
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	undelegated := requests.Clone()
	for _, name := range names {
		requests.Set(name, 0)
	}
	return &schdcache.DRADelegation{Resources: names, Undelegated: undelegated}
}

// requestsForDomain is the single-Pod request to record against a domain. A domain whose
// nodes publish the delegated resources consumed them, so the usage has to say so.
func requestsForDomain(requests resources.Requests, delegation *schdcache.DRADelegation, tasFlavor *schdcache.TASFlavorSnapshot, domain []string) resources.Requests {
	if !features.Enabled(features.KueueDRADeviceFeasibility) || delegation == nil || tasFlavor == nil {
		return requests
	}
	if !tasFlavor.DomainAdvertises(tas.DomainID(domain), delegation.Resources) {
		return requests
	}
	return delegation.Undelegated
}
