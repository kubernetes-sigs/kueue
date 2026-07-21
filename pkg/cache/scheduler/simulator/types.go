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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// Candidate represents an abstract scheduleable domain.
type Candidate interface {
	GetID() utiltas.TopologyDomainID
	GetNode() *corev1.Node
}

// Candidate represents an abstract scheduleable domain.
type MatchedCandidate interface {
	Candidate
	GetAffinityScore() int64
	SetAffinityScore(int64)
}

// NodeExclusionStats tracks why nodes were excluded during TAS scheduling.
type NodeExclusionStats struct {
	Taints       map[string]int
	NodeSelector int
	Affinity     int
	TotalNodes   int
}

// PodRequirements stores pod-driven scheduling filters and
// resource inputs that are only needed while filling per-domain counts.
type PodRequirements struct {
	Tolerations              []corev1.Toleration
	Selector                 labels.Selector
	AffinitySelector         *nodeaffinity.NodeSelector
	PreferredSchedulingTerms *nodeaffinity.PreferredSchedulingTerms
}

type NodeExclusionType int

const (
	ExclusionTaints NodeExclusionType = iota
	ExclusionNodeSelector
	ExclusionAffinity
)

func (s *NodeExclusionStats) RecordExclusion(exclusionType NodeExclusionType, taint *corev1.Taint) {
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
