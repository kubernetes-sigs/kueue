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

func (frq FlavorResourceQuantities) MarshalJSON() ([]byte, error) {
	temp := make(map[string]int64, len(frq))
	for flavorResource, num := range frq {
		temp[flavorResource.String()] = num.Int64()
	}
	return json.Marshal(temp)
}

func (frq FlavorResourceQuantities) FlattenFlavors() Requests {
	result := Requests{}
	for key, val := range frq {
		result[key.Resource] += val.Int64()
	}
	return result
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
