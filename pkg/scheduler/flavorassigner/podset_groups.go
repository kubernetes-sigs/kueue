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

type podSetGroups struct {
	psGroups       map[string][]indexedPodSet
	insertionOrder []string
}

func newPodSetGroups() *podSetGroups {
	return &podSetGroups{
		psGroups:       make(map[string][]indexedPodSet),
		insertionOrder: make([]string, 0),
	}
}

func (om *podSetGroups) insert(groupName string, value indexedPodSet) {
	if _, present := om.psGroups[groupName]; !present {
		om.insertionOrder = append(om.insertionOrder, groupName)
	}
	om.psGroups[groupName] = append(om.psGroups[groupName], value)
}

func (om *podSetGroups) orderedPodSetGroups() [][]indexedPodSet {
	orderedPodSetGroups := make([][]indexedPodSet, len(om.insertionOrder))
	for i, key := range om.insertionOrder {
		orderedPodSetGroups[i] = om.psGroups[key]
	}
	return orderedPodSetGroups
}
