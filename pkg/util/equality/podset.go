/*
Copyright 2023 The Kubernetes Authors.

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

package equality

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// TODO: Revisit this, maybe we should extend the check to everything that could potentially impact
// the workload scheduling (priority, nodeSelectors(when suspended), tolerations and maybe more)
func comparePodTemplate(a, b *corev1.PodSpec, ignoreTolerations bool) bool {
	if !ignoreTolerations && !equality.Semantic.DeepEqual(a.Tolerations, b.Tolerations) {
		return false
	}
	if !equality.Semantic.DeepEqual(a.InitContainers, b.InitContainers) {
		return false
	}
	return equality.Semantic.DeepEqual(a.Containers, b.Containers)
}

func ComparePodSets(a, b *kueue.PodSet, ignoreTolerations bool) bool {
	if a.Count != b.Count {
		return false
	}
	if ptr.Deref(a.MinCount, -1) != ptr.Deref(b.MinCount, -1) {
		return false
	}

	return comparePodTemplate(&a.Template.Spec, &b.Template.Spec, ignoreTolerations)
}

func ComparePodSetSlices(a, b []kueue.PodSet, ignoreTolerations bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !ComparePodSets(&a[i], &b[i], ignoreTolerations) {
			return false
		}
	}
	return true
}
