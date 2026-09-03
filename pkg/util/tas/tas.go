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

import (
	"strings"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type TopologyDomainID string

const topologyDomainIDSeparator = ","

func DomainID(levelValues []string) TopologyDomainID {
	return TopologyDomainID(strings.Join(levelValues, topologyDomainIDSeparator))
}

// BelongsTo reports whether d identifies targetDomain itself or one of its
// descendants.
func (d TopologyDomainID) BelongsTo(targetDomain TopologyDomainID) bool {
	if targetDomain == "" {
		return true
	}
	return d == targetDomain || strings.HasPrefix(string(d), string(targetDomain)+topologyDomainIDSeparator)
}

// NodeNameFromDomainID returns the node name identified by the domain ID. It
// reports false when the domain does not identify a single node, i.e. when the
// lowest level of levels is not the hostname label.
//
// When the hostname is the lowest level, an assignment is built for that level
// alone, so the domain ID holds the node name verbatim with no level values
// concatenated into it.
func NodeNameFromDomainID(levels []string, domainID TopologyDomainID) (string, bool) {
	if len(levels) == 0 || !IsLowestLevelHostname(levels) {
		return "", false
	}
	return string(domainID), true
}

func IsTAS(pod *corev1.Pod) bool {
	if IsExplicitTAS(pod.Annotations) {
		return true
	}
	if _, ok := pod.Annotations[kueue.PodSetUnconstrainedTopologyAnnotation]; ok {
		return true
	}
	return false
}

func IsExplicitTAS(annots map[string]string) bool {
	if _, ok := annots[kueue.PodSetPreferredTopologyAnnotation]; ok {
		return true
	}
	if _, ok := annots[kueue.PodSetRequiredTopologyAnnotation]; ok {
		return true
	}
	if _, ok := annots[kueue.PodSetSliceRequiredTopologyAnnotation]; ok {
		return true
	}
	if _, ok := annots[kueue.PodSetSliceRequiredTopologyConstraintsAnnotation]; ok {
		return true
	}
	return false
}

// HasTopologyConstraint reports whether the request contains a field that
// explicitly opts the PodSet into topology-aware scheduling. Derived indexing
// fields alone don't constitute a topology constraint.
func HasTopologyConstraint(tr *kueue.PodSetTopologyRequest) bool {
	return tr != nil && (tr.Unconstrained != nil ||
		tr.Required != nil ||
		tr.Preferred != nil ||
		tr.PodSetSliceRequiredTopology != nil ||
		tr.PodSetSliceSize != nil ||
		len(tr.PodsetSliceRequiredTopologyConstraints) > 0)
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

func Levels(topology *kueue.Topology) []string {
	result := make([]string, len(topology.Spec.Levels))
	for i, level := range topology.Spec.Levels {
		result[i] = level.NodeLabel
	}
	return result
}

func IsNodeStatusConditionTrue(conditions []corev1.NodeCondition, conditionType corev1.NodeConditionType) bool {
	for _, cond := range conditions {
		if cond.Type == conditionType {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func GetNodeCondition(node *corev1.Node, conditionType corev1.NodeConditionType) *corev1.NodeCondition {
	for i := range node.Status.Conditions {
		if node.Status.Conditions[i].Type == conditionType {
			return &node.Status.Conditions[i]
		}
	}
	return nil
}

// IsLowestLevelHostname checks if the lowest (last) level in the provided topology levels is node
func IsLowestLevelHostname(levels []string) bool {
	return levels[len(levels)-1] == corev1.LabelHostname
}

// PodSetSliceRequiredTopologyConstraints returns the unified slice topology
// constraints for a PodSetTopologyRequest, regardless of whether they were
// specified via the new multi-layer PodsetSliceRequiredTopologyConstraints
// annotation or the old single-layer PodSetSliceRequiredTopology/
// PodSetSliceSize fields.
//
// This is necessary to handle Workload objects that were persisted before the
// unification, which only populate the legacy fields. Callers should use this
// function instead of reading PodsetSliceRequiredTopologyConstraints directly
// to ensure both annotation forms are handled consistently.
func PodSetSliceRequiredTopologyConstraints(tr *kueue.PodSetTopologyRequest) []kueue.PodsetSliceRequiredTopologyConstraint {
	if tr == nil {
		return nil
	}
	if len(tr.PodsetSliceRequiredTopologyConstraints) > 0 {
		return tr.PodsetSliceRequiredTopologyConstraints
	}
	if tr.PodSetSliceRequiredTopology == nil {
		return nil
	}
	size := int32(0)
	if tr.PodSetSliceSize != nil {
		size = *tr.PodSetSliceSize
	}
	return []kueue.PodsetSliceRequiredTopologyConstraint{
		{Topology: *tr.PodSetSliceRequiredTopology, Size: size},
	}
}
