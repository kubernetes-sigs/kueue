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

package tas

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

type TopologyDomainID string

func DomainID(levelValues []string) TopologyDomainID {
	return TopologyDomainID(strings.Join(levelValues, ","))
}

func NodeLabelsFromKeysAndValues(keys, values []string) map[string]string {
	result := make(map[string]string, len(keys))
	for i := range keys {
		result[keys[i]] = values[i]
	}
	return result
}

func LevelValues(levelKeys []string, objectLabels map[string]string) []string {
	levelValues := make([]string, len(levelKeys))
	for levelIdx, levelKey := range levelKeys {
		levelValues[levelIdx] = objectLabels[levelKey]
	}
	return levelValues
}

func Levels(topology *kueuealpha.Topology) []string {
	result := make([]string, len(topology.Spec.Levels))
	for i, level := range topology.Spec.Levels {
		result[i] = level.NodeLabel
	}
	return result
}

func IsNodeStatusConditionTrue(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType, conditionStatus corev1.ConditionStatus) bool {
	for _, cond := range conditions {
		if cond.Type == conditionType {
			return cond.Status == conditionStatus
		}
	}
	return false
}
