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
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
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
