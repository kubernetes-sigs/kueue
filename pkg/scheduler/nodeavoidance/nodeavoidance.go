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

// IsNodeUnhealthy checks if the node has the specified unhealthy label.
func IsNodeUnhealthy(node *corev1.Node, unhealthyLabel string) bool {
	if node == nil || node.Labels == nil {
		return false
	}
	_, ok := node.Labels[unhealthyLabel]
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

// ConstructNodeAffinity returns a NodeAffinity based on the policy and unhealthy label.
// It returns nil if the policy is not supported or if the unhealthy label is empty.
func ConstructNodeAffinity(policy string, unhealthyLabel string) *corev1.NodeAffinity {
	if unhealthyLabel == "" {
		return nil
	}

	switch policy {
	case constants.NodeAvoidancePolicyDisallowUnhealthy:
		return &corev1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
				NodeSelectorTerms: []corev1.NodeSelectorTerm{
					{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      unhealthyLabel,
								Operator: corev1.NodeSelectorOpDoesNotExist,
							},
						},
					},
				},
			},
		}
	case constants.NodeAvoidancePolicyPreferHealthy:
		return &corev1.NodeAffinity{
			PreferredDuringSchedulingIgnoredDuringExecution: []corev1.PreferredSchedulingTerm{
				{
					Weight: 100, // High weight to prefer healthy nodes
					Preference: corev1.NodeSelectorTerm{
						MatchExpressions: []corev1.NodeSelectorRequirement{
							{
								Key:      unhealthyLabel,
								Operator: corev1.NodeSelectorOpDoesNotExist,
							},
						},
					},
				},
			},
		}
	}
	return nil
}

// MergeNodeAffinity merges the node avoidance affinity into the existing affinity.
// It modifies the existing affinity in place if it's not nil, or returns a new one.
func MergeNodeAffinity(existing *corev1.NodeAffinity, policy string, unhealthyLabel string) *corev1.NodeAffinity {
	avoidanceAffinity := ConstructNodeAffinity(policy, unhealthyLabel)
	if avoidanceAffinity == nil {
		return existing
	}
	if existing == nil {
		return avoidanceAffinity
	}

	// Merge RequiredDuringSchedulingIgnoredDuringExecution
	if avoidanceAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		if existing.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			existing.RequiredDuringSchedulingIgnoredDuringExecution = avoidanceAffinity.RequiredDuringSchedulingIgnoredDuringExecution
		} else {
			// Append avoidance requirements to each existing term to ensure the avoidance policy is enforced
			// across all ORed terms.
			// (T1 OR T2) AND Avoid -> (T1 AND Avoid) OR (T2 AND Avoid)
			avoidanceTerm := avoidanceAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[0]
			if len(existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms) == 0 {
				existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []corev1.NodeSelectorTerm{avoidanceTerm}
			} else {
				for i := range existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
					existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i].MatchExpressions = append(
						existing.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms[i].MatchExpressions,
						avoidanceTerm.MatchExpressions...,
					)
				}
			}
		}
	}

	// Merge PreferredDuringSchedulingIgnoredDuringExecution
	if len(avoidanceAffinity.PreferredDuringSchedulingIgnoredDuringExecution) > 0 {
		existing.PreferredDuringSchedulingIgnoredDuringExecution = append(
			existing.PreferredDuringSchedulingIgnoredDuringExecution,
			avoidanceAffinity.PreferredDuringSchedulingIgnoredDuringExecution...,
		)
	}

	return existing
}
