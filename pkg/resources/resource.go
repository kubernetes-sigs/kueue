/*
Copyright 2024 The Kubernetes Authors.

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

package resources

import (
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type FlavorResource struct {
	Flavor   kueue.ResourceFlavorReference
	Resource corev1.ResourceName
}

type FlavorResourceQuantities map[kueue.ResourceFlavorReference]Requests
type FlavorResourceQuantitiesFlat map[FlavorResource]int64

func (f FlavorResourceQuantitiesFlat) Unflatten() FlavorResourceQuantities {
	out := make(FlavorResourceQuantities)
	for flavorResource, value := range f {
		if _, ok := out[flavorResource.Flavor]; !ok {
			out[flavorResource.Flavor] = make(Requests)
		}
		out[flavorResource.Flavor][flavorResource.Resource] = value
	}
	return out
}

// For attempts to access nested value, returning 0 if absent.
func (f FlavorResourceQuantities) For(fr FlavorResource) int64 {
	return f[fr.Flavor][fr.Resource]
}

// Add adds the Quantity v for the FlavorResource fr, allocating
// as needed.
func (f *FlavorResourceQuantities) Add(fr FlavorResource, v int64) {
	if *f == nil {
		*f = make(FlavorResourceQuantities)
	}
	if (*f)[fr.Flavor] == nil {
		(*f)[fr.Flavor] = make(Requests)
	}
	(*f)[fr.Flavor][fr.Resource] += v
}
