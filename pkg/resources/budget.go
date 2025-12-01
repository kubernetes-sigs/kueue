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
	"fmt"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type FlavorWallTimeResource struct {
	Flavor kueue.ResourceFlavorReference
}

func (fr FlavorWallTimeResource) String() string {
	return fmt.Sprintf(`{"Flavor":"%s"}`, string(fr.Flavor))
}

type FlavorWallTimeQuantities map[FlavorWallTimeResource]int32

type FlavorWallTimeUsage int32

func (frq FlavorWallTimeQuantities) FlattenFlavors() int32 {
	result := int32(0)
	for key := range frq {
		result += frq[key]
	}
	return result
}
