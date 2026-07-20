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

func (c *defaultChecker) FindFeasibleNodes(leaves []*simulator.LeafDomain, requirements *simulator.TopologyAssignmentPodRequirements) ([]simulator.MatchedLeaf, *simulator.ExclusionStats, error) {
	exclusionStats := simulator.NewExclusionStats()
	var feasibleLeaves = make([]simulator.MatchedLeaf, 0, len(leaves))
	for _, leaf := range leaves {
		if leaf.Node == nil {
			feasibleLeaves = append(feasibleLeaves, simulator.MatchedLeaf{Leaf: leaf})
			continue
		}
		// 1. Check Tolerations against Node Taints
		nodeTaints := leaf.Node.Spec.Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(c.log, nodeTaints, requirements.Tolerations, utiltaints.IsSchedulingTaint, true)
		if untolerated {
			c.log.V(5).Info("excluding node with untolerated taint", "domainID", leaf.ID, "taint", taint)
			exclusionStats.RecordExclusion(simulator.ExclusionTaints, &taint)
			continue
		}

		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if nodeLabels := leaf.Node.Labels; nodeLabels != nil {
			nodeLabelSet = nodeLabels
		}

		if requirements.Selector != nil && !requirements.Selector.Matches(nodeLabelSet) {
			c.log.V(5).Info("excluding node that doesn't match nodeSelectors", "domainID", leaf.ID, "nodeLabels", nodeLabelSet)
			exclusionStats.RecordExclusion(simulator.ExclusionNodeSelector, nil)
			continue
		}

		// 3. Check Node against Affinity Node Selector
		nodeObj := leaf.Node
		if requirements.AffinitySelector != nil && !requirements.AffinitySelector.Match(nodeObj) {
			c.log.V(5).Info("excluding node due to an affinity mismatch", "domainID", leaf.ID)
			exclusionStats.RecordExclusion(simulator.ExclusionAffinity, nil)
			continue
		}

		// 4. Calculate Affinity Score
		var affinityScore int64
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && requirements.PreferredSchedulingTerms != nil {
			affinityScore = requirements.PreferredSchedulingTerms.Score(nodeObj)
		}

		// 5. Track the matching leaf as feasible
		feasibleLeaves = append(feasibleLeaves, simulator.MatchedLeaf{Leaf: leaf, AffinityScore: affinityScore})
	}
	return feasibleLeaves, exclusionStats, nil
}
