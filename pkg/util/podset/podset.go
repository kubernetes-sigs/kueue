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

package podset

import kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"

func FindPodSetByName(podSets []kueue.PodSet, name kueue.PodSetReference) *kueue.PodSet {
	for _, podSet := range podSets {
		if podSet.Name == name {
			return &podSet
		}
	}
	return nil
}
