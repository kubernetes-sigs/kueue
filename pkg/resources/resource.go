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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type FlavorResource struct {
	Flavor   kueue.ResourceFlavorReference
	Resource corev1.ResourceName
}

func (fr FlavorResource) String() string {
	return fmt.Sprintf(`{"Flavor":"%s","Resource":"%s"}`, string(fr.Flavor), string(fr.Resource))
}

type FlavorResourceQuantities map[FlavorResource]int64

func (q FlavorResourceQuantities) MarshalJSON() ([]byte, error) {
	temp := make(map[string]int64, len(q))
	for flavourResource, num := range q {
		temp[flavourResource.String()] = num
	}
	return json.Marshal(temp)
}

func (frq FlavorResourceQuantities) FlattenFlavors() map[corev1.ResourceName]int64 {
	resources := sets.New[corev1.ResourceName]()
	for key := range frq {
		resources.Insert(key.Resource)
	}
	result := make(map[corev1.ResourceName]int64, resources.Len())
	for key, val := range frq {
		result[key.Resource] += val
	}
	return result
}

// // func MulResources(resources map[corev1.ResourceName]int64, multiplier float64) map[corev1.ResourceName]int64 {
// //
// // }

// func AddResources(a, b map[corev1.ResourceName]int64) map[corev1.ResourceName]int64 {
// 	result := make(map[corev1.ResourceName]int64)

// 	for rN, aVal := range a {
// 		result[rN] = aVal
// 	}
// 	for rN, bVal := range b {
// 		if existingValue, ok := result[rN]; ok {
// 			result[rN] = existingValue + bVal
// 		} else {
// 			result[rN] = bVal
// 		}
// 	}

// 	return result
// }
