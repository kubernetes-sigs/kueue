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
	"sigs.k8s.io/kueue/pkg/controller/constants"
)

// IsNodeAvoided checks if the node has the specified avoided label.
func IsNodeAvoided(node *corev1.Node, avoidedLabel string) bool {
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
	return wl.Annotations[constants.NodeAvoidancePolicyAnnotation]
}

// ConstructNodeAffinity returns a NodeAffinity based on the policy and avoidance label.
// It returns nil if the policy is not supported or if the avoidance label is empty.
func ConstructNodeAffinity(policy string, avoidanceLabel string) *corev1.NodeAffinity {
	if avoidanceLabel == "" {
		return nil
	}

	nodeAffinity := &corev1.NodeAffinity{}
	if policy == constants.NodeAvoidancePolicyRequired {
		nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{
			NodeSelectorTerms: []corev1.NodeSelectorTerm{
				{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      avoidanceLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				},
			},
		}
	} else if policy == constants.NodeAvoidancePolicyPreferNoSchedule {
		nodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = []corev1.PreferredSchedulingTerm{
			{
				Weight: 100,
				Preference: corev1.NodeSelectorTerm{
					MatchExpressions: []corev1.NodeSelectorRequirement{
						{
							Key:      avoidanceLabel,
							Operator: corev1.NodeSelectorOpDoesNotExist,
						},
					},
				},
			},
		}
	}
	if nodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil && len(nodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution) == 0 {
		return nil
	}
	return nodeAffinity
}

// MergeNodeAffinity merges the node avoidance affinity into the existing affinity.
// It modifies the existing affinity in place if it's not nil, or returns a new one.
func MergeNodeAffinity(existing *corev1.NodeAffinity, policy, avoidanceLabel string) *corev1.NodeAffinity {
	avoidanceAffinity := ConstructNodeAffinity(policy, avoidanceLabel)
	if avoidanceAffinity == nil {
		return existing
	}
	if existing == nil {
		return avoidanceAffinity
	}

	if policy == constants.NodeAvoidancePolicyRequired {
		// For Required, we append the avoidance requirement to ALL existing terms (AND logic)
		// If there are no existing terms but existing is not nil (empty struct), we just add ours.
		if existing.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			existing.RequiredDuringSchedulingIgnoredDuringExecution = avoidanceAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		} else {
			for i := range existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
				existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i].MatchExpressions = append(
					existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i].MatchExpressions,
					avoidanceAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0].MatchExpressions...,
				)
			}
		}
	} else if policy == constants.NodeAvoidancePolicyPreferNoSchedule {
		// Merge PreferredDuringSchedulingIgnoredDuringExecution
		if len(avoidanceAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
			existing.PreferredDuringSchedulingIgnoredDuringExecution = append(
				existing.PreferredDuringSchedulingIgnoredDuringExecution,
				avoidanceAffinity.PreferredDuringSchedulingIgnoredDuringExecution...,
			)
		}
	}

	return existing
}
