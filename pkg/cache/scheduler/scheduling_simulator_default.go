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
	"errors"
	"iter"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utiltaints "sigs.k8s.io/kueue/pkg/util/taints"
)

type defaultSimulator struct{}

func newDefaultSimulator() simulator.SchedulingSimulator {
	return &defaultSimulator{}
}

func (s *defaultSimulator) NewFeasibilityChecker(nodes []*corev1.Node) (simulator.NodeFeasibilityChecker, error) {
	return &defaultChecker{}, nil
}

type defaultChecker struct{}

func (c *defaultChecker) FindFeasibleNodes(
	ctx context.Context,
	candidates iter.Seq[simulator.Candidate],
	requirements *simulator.PodRequirements,
	exclusionStats *simulator.NodeExclusionStats,
) ([]simulator.MatchedCandidate, error) {
	logger := log.FromContext(ctx)

	var feasibleCandidates []simulator.MatchedCandidate
	respectNodeAffinityPreferredEnabled := features.Enabled(features.TASRespectNodeAffinityPreferred)

	for candidate := range candidates {
		exclusionStats.TotalNodes++
		nodeObj := candidate.GetNode()
		domainID := candidate.GetID()

		// 0. Cast to matching candidate
		matchedCandidate, ok := candidate.(simulator.MatchedCandidate)
		if !ok {
			return nil, errors.New("failed to cast")
		}

		if nodeObj == nil {
			feasibleCandidates = append(feasibleCandidates, matchedCandidate)
			continue
		}
		// 1. Check Tolerations against Node Taints
		nodeTaints := nodeObj.Spec.Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(logger, nodeTaints, requirements.Tolerations, utiltaints.IsSchedulingTaint, true)
		if untolerated {
			logger.V(5).Info("excluding node with untolerated taint", "domainID", domainID, "taint", taint)
			exclusionStats.RecordExclusion(simulator.ExclusionTaints, &taint)
			continue
		}

		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if nodeLabels := nodeObj.Labels; nodeLabels != nil {
			nodeLabelSet = nodeLabels
		}

		if requirements.Selector != nil && !requirements.Selector.Matches(nodeLabelSet) {
			logger.V(5).Info("excluding node that doesn't match nodeSelectors", "domainID", domainID, "nodeLabels", nodeLabelSet)
			exclusionStats.RecordExclusion(simulator.ExclusionNodeSelector, nil)
			continue
		}

		// 3. Check Node against Affinity Node Selector
		if requirements.AffinitySelector != nil && !requirements.AffinitySelector.Match(nodeObj) {
			logger.V(5).Info("excluding node due to an affinity mismatch", "domainID", domainID)
			exclusionStats.RecordExclusion(simulator.ExclusionAffinity, nil)
			continue
		}

		// 4. Calculate Affinity Score
		var affinityScore int64
		if respectNodeAffinityPreferredEnabled && requirements.PreferredSchedulingTerms != nil {
			affinityScore = requirements.PreferredSchedulingTerms.Score(nodeObj)
		}

		matchedCandidate.SetAffinityScore(affinityScore)
		feasibleCandidates = append(feasibleCandidates, matchedCandidate)
	}
	return feasibleCandidates, nil
}
