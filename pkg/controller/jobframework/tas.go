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

package jobframework

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func PodSetTopologyRequest(meta *metav1.ObjectMeta) *kueue.PodSetTopologyRequest {
	requiredValue, requiredFound := meta.Annotations[kueuealpha.PodSetRequiredTopologyAnnotation]
	if requiredFound {
		return &kueue.PodSetTopologyRequest{
			Required: ptr.To(requiredValue),
		}
	}
	preferredValue, preferredFound := meta.Annotations[kueuealpha.PodSetPreferredTopologyAnnotation]
	if preferredFound {
		return &kueue.PodSetTopologyRequest{
			Preferred: ptr.To(preferredValue),
		}
	}
	return nil
}
