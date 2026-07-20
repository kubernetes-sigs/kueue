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

package scheduler

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utiltaints "sigs.k8s.io/kueue/pkg/util/taints"
)

type defaultSimulator struct{}

func NewDefaultSimulator() simulator.SchedulingSimulator {
	return &defaultSimulator{}
}

func (s *defaultSimulator) NewFeasibilityChecker(ctx context.Context, nodes []*corev1.Node) (simulator.NodeFeasibilityChecker, error) {
	log := log.FromContext(ctx)
	return &defaultChecker{log: log}, nil
}

type defaultChecker struct {
	log logr.Logger
}

func (c *defaultChecker) FindFeasibleNodes(
	candidates []simulator.Candidate,
	requirements *simulator.TopologyAssignmentPodRequirements,
) ([]simulator.MatchedCandidate, *simulator.ExclusionStats, error) {
	exclusionStats := simulator.NewExclusionStats()
	var feasibleCandidates = make([]simulator.MatchedCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		nodeObj := candidate.GetNode()
		domainID := candidate.GetID()
		if nodeObj == nil {
			feasibleCandidates = append(feasibleCandidates, simulator.MatchedCandidate{Candidate: candidate})
			continue
		}
		// 1. Check Tolerations against Node Taints
		nodeTaints := nodeObj.Spec.Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(c.log, nodeTaints, requirements.Tolerations, utiltaints.IsSchedulingTaint, true)
		if untolerated {
			c.log.V(5).Info("excluding node with untolerated taint", "domainID", domainID, "taint", taint)
			exclusionStats.RecordExclusion(simulator.ExclusionTaints, &taint)
			continue
		}

		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if nodeLabels := nodeObj.Labels; nodeLabels != nil {
			nodeLabelSet = nodeLabels
		}

		if requirements.Selector != nil && !requirements.Selector.Matches(nodeLabelSet) {
			c.log.V(5).Info("excluding node that doesn't match nodeSelectors", "domainID", domainID, "nodeLabels", nodeLabelSet)
			exclusionStats.RecordExclusion(simulator.ExclusionNodeSelector, nil)
			continue
		}

		// 3. Check Node against Affinity Node Selector
		if requirements.AffinitySelector != nil && !requirements.AffinitySelector.Match(nodeObj) {
			c.log.V(5).Info("excluding node due to an affinity mismatch", "domainID", domainID)
			exclusionStats.RecordExclusion(simulator.ExclusionAffinity, nil)
			continue
		}

		// 4. Calculate Affinity Score
		var affinityScore int64
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && requirements.PreferredSchedulingTerms != nil {
			affinityScore = requirements.PreferredSchedulingTerms.Score(nodeObj)
		}

		// 5. Track the matching candidate as feasible
		feasibleCandidates = append(feasibleCandidates, simulator.MatchedCandidate{Candidate: candidate, AffinityScore: affinityScore})
	}
	return feasibleCandidates, exclusionStats, nil
}
