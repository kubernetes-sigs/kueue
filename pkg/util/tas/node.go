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

package tas

// NodeMatchesFlavor checks if a node's labels match the required labels
// and contains all required topology levels. Returns true if matches.
func NodeMatchesFlavor(nodeLabels map[string]string, requiredLabels map[string]string, requiredLevels []string) bool {
	for k, v := range requiredLabels {
		if nodeLabels[k] != v {
			return false
		}
	}
	for _, level := range requiredLevels {
		if _, ok := nodeLabels[level]; !ok {
			return false
		}
	}
	return true
}
