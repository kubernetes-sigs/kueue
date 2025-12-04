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

package nodeavoidance

import (
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// ShouldNodeBeAvoided checks if the node has the specified avoided label.
func ShouldNodeBeAvoided(node *corev1.Node, avoidedLabel string) bool {
	if node == nil || node.Labels == nil {
		return false
	}
	_, ok := node.Labels[avoidedLabel]
	return ok
}

// GetNodeAvoidancePolicy returns the node avoidance policy from the workload annotations.
// It returns an empty string if the annotation is not present.
func GetNodeAvoidancePolicy(wl *kueue.Workload) string {
	if wl == nil || wl.Annotations == nil {
		return ""
	}
	return wl.Annotations[kueue.NodeAvoidancePolicyAnnotation]
}
