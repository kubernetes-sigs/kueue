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

package simulator

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// Candidate represents an abstract scheduleable domain.
type Candidate interface {
	GetID() utiltas.TopologyDomainID
	GetNode() *corev1.Node
}

// MatchedCandidate represents a Candidate that matched the scheduling requirements
// along with its calculated affinity score.
type MatchedCandidate struct {
	Candidate     Candidate
	AffinityScore int64
}

// ExclusionStats tracks why nodes were excluded during TAS scheduling.
type ExclusionStats struct {
	Taints         map[string]int
	NodeSelector   int
	Affinity       int
	TopologyDomain int
	Resources      map[corev1.ResourceName]int
	TotalNodes     int
}

func NewExclusionStats() *ExclusionStats {
	return &ExclusionStats{}
}

// HasExclusions returns true if any exclusion reasons were recorded.
func (s *ExclusionStats) HasExclusions() bool {
	return s.NodeSelector > 0 || s.Affinity > 0 || s.TopologyDomain > 0 ||
		len(s.Taints) > 0 || len(s.Resources) > 0
}

// FormatReasons returns a sorted, comma-separated string of exclusion reasons.
func (s *ExclusionStats) FormatReasons() string {
	var reasons []string
	if s.NodeSelector > 0 {
		reasons = append(reasons, fmt.Sprintf("nodeSelector: %d", s.NodeSelector))
	}
	if s.Affinity > 0 {
		reasons = append(reasons, fmt.Sprintf("affinity: %d", s.Affinity))
	}
	if s.TopologyDomain > 0 {
		reasons = append(reasons, fmt.Sprintf("topologyDomain: %d", s.TopologyDomain))
	}
	for _, taint := range slices.Sorted(maps.Keys(s.Taints)) {
		reasons = append(reasons, fmt.Sprintf("taint %q: %d", taint, s.Taints[taint]))
	}
	for _, resource := range slices.Sorted(maps.Keys(s.Resources)) {
		reasons = append(reasons, fmt.Sprintf("resource %q: %d", resource, s.Resources[resource]))
	}
	slices.Sort(reasons)
	return strings.Join(reasons, ", ")
}

// RecordResourceExclusion tracks excluding a domain by resource.
func (s *ExclusionStats) RecordResourceExclusion(res corev1.ResourceName) {
	if s.Resources == nil {
		s.Resources = make(map[corev1.ResourceName]int)
	}
	s.Resources[res]++
}

// Add merges another ExclusionStats into this one.
func (s *ExclusionStats) Add(other *ExclusionStats) {
	s.TotalNodes += other.TotalNodes
	s.NodeSelector += other.NodeSelector
	s.Affinity += other.Affinity
	s.TopologyDomain += other.TopologyDomain
	for k, v := range other.Taints {
		if s.Taints == nil {
			s.Taints = make(map[string]int)
		}
		s.Taints[k] += v
	}
	for k, v := range other.Resources {
		if s.Resources == nil {
			s.Resources = make(map[corev1.ResourceName]int)
		}
		s.Resources[k] += v
	}
}

// TopologyAssignmentPodRequirements stores pod-driven scheduling filters and
// resource inputs that are only needed while filling per-domain counts.
type TopologyAssignmentPodRequirements struct {
	Requests                  resources.Requests
	LeaderRequests            *resources.Requests
	AssumedUsage              map[utiltas.TopologyDomainID]resources.Requests
	Tolerations               []corev1.Toleration
	Selector                  labels.Selector
	AffinitySelector          *nodeaffinity.NodeSelector
	PreferredSchedulingTerms  *nodeaffinity.PreferredSchedulingTerms
	RequiredReplacementDomain utiltas.TopologyDomainID
	SimulateEmpty             bool
	MatchKey                  *PodSetMatchKey
}

type NodeExclusionType int

const (
	ExclusionTaints NodeExclusionType = iota
	ExclusionNodeSelector
	ExclusionAffinity
)

func (s *ExclusionStats) RecordExclusion(exclusionType NodeExclusionType, taint *corev1.Taint) {
	switch exclusionType {
	case ExclusionTaints:
		if taint != nil {
			if s.Taints == nil {
				s.Taints = make(map[string]int)
			}
			s.Taints[taint.ToString()]++
		}
	case ExclusionNodeSelector:
		s.NodeSelector++
	case ExclusionAffinity:
		s.Affinity++
	}
}

type PodSetMatchKey struct {
	WorkloadUID types.UID
	PodSetName  string
}
