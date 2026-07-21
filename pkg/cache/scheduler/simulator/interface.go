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
	"context"
	"iter"

	corev1 "k8s.io/api/core/v1"
)

// NodeFeasibilityChecker determines which topology leaves can satisfy pod requirements.
type NodeFeasibilityChecker interface {
	FindFeasibleNodes(ctx context.Context, candidates iter.Seq[Candidate], requirements *PodRequirements, stats *NodeExclusionStats) ([]MatchedCandidate, error)
}

// SchedulingSimulator acts as a factory for the feasibility checker.
type SchedulingSimulator interface {
	NewFeasibilityChecker(nodes []*corev1.Node) (NodeFeasibilityChecker, error)
}

func AsCandidates[C Candidate](seq iter.Seq[C]) iter.Seq[Candidate] {
	return func(yield func(Candidate) bool) {
		for candidate := range seq {
			if !yield(candidate) {
				return
			}
		}
	}
}
