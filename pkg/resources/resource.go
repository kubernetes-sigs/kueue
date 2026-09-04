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

package resources

import (
	"encoding/json"
	"fmt"
	"maps"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type FlavorResource struct {
	Flavor   kueue.ResourceFlavorReference
	Resource corev1.ResourceName
}

func (fr FlavorResource) String() string {
	return fmt.Sprintf(`{"Flavor":"%s","Resource":"%s"}`, string(fr.Flavor), string(fr.Resource))
}

type FlavorResourceQuantities map[FlavorResource]Amount

// MarshalJSON writes the int64 projection of each amount, clamped at the ends.
// It is a diagnostic shape rather than a round trip: two amounts that differ
// only past int64 marshal to the same number, and unmarshalling does not
// recover either. Nothing reads it back to reconstruct accounting.
func (frq FlavorResourceQuantities) MarshalJSON() ([]byte, error) {
	temp := make(map[string]int64, len(frq))
	for flavorResource, num := range frq {
		temp[flavorResource.String()] = num.asSaturatedInt64()
	}
	return json.Marshal(temp)
}

// FlattenFlavors converts into the int64-limited Requests domain. Use
// ToResourceList for anything reported through the API, which needs the
// resource scale applied before a total is narrowed.
func (frq FlavorResourceQuantities) FlattenFlavors() Requests {
	if len(frq) == 0 {
		return NewRequests()
	}
	// Summed as Amounts so a total past int64 is not reached by wrapping one,
	// then narrowed once per resource where Requests can only hold an int64.
	exact := map[corev1.ResourceName]Amount{}
	for key, val := range frq {
		exact[key.Resource] = exact[key.Resource].Add(val)
	}
	result := make(map[corev1.ResourceName]int64, len(exact))
	for name, a := range exact {
		result[name] = a.asSaturatedInt64()
	}
	return NewRequestsFromMap(result)
}

// ToResourceList sums the flavors of each resource as Amounts and converts each
// total once, at the boundary. Going through FlattenFlavors instead would
// narrow a CPU total to int64 milli before the scale is applied, which reports
// a 10P aggregate as roughly 9.2P.
func (frq FlavorResourceQuantities) ToResourceList(formatter *ResourceFormatter) corev1.ResourceList {
	if len(frq) == 0 {
		return nil
	}
	exact := make(map[corev1.ResourceName]Amount, len(frq))
	for fr, amount := range frq {
		exact[fr.Resource] = exact[fr.Resource].Add(amount)
	}
	out := make(corev1.ResourceList, len(exact))
	for name, amount := range exact {
		out[name] = formatter.AmountQuantity(name, amount)
	}
	return out
}

// Clone returns a shallow copy of the map.
func (frq FlavorResourceQuantities) Clone() FlavorResourceQuantities {
	if frq == nil {
		return nil
	}
	out := make(FlavorResourceQuantities, len(frq))
	maps.Copy(out, frq)
	return out
}

// Sub returns a new map with element-wise subtraction. Missing keys on either
// side are treated as bounded zero, except the result is omitted only when
// the operand is missing on the receiver side. (Symmetric difference is not
// the goal here; this matches the semantics of the prior map Sub.)
func (frq FlavorResourceQuantities) Sub(other FlavorResourceQuantities) FlavorResourceQuantities {
	result := make(FlavorResourceQuantities, len(frq))
	for fr, qty := range frq {
		result[fr] = qty.Sub(other[fr])
	}
	return result
}
