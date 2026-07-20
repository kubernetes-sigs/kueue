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

	"sigs.k8s.io/kueue/pkg/features"
	utiltaints "sigs.k8s.io/kueue/pkg/util/taints"
)

type defaultSimulator struct{}

func NewDefaultSimulator() SchedulingSimulator {
	return &defaultSimulator{}
}

func (s *defaultSimulator) NewFeasibilityChecker(ctx context.Context, nodes []*corev1.Node) (nodeFeasibilityChecker, error) {
	return &defaultChecker{}, nil
}

type defaultChecker struct{}

func (c *defaultChecker) FindFeasibleNodes(ctx context.Context, log logr.Logger, query *FeasibleNodesQuery) ([]matchedLeaf, error) {
	var feasibleLeaves = make([]matchedLeaf, 0, len(query.Leaves))
	for _, leaf := range query.Leaves {
		if leaf.node == nil {
			feasibleLeaves = append(feasibleLeaves, matchedLeaf{leaf: leaf})
			continue
		}
		// 1. Check Tolerations against Node Taints
		nodeTaints := leaf.node.Spec.Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(log, nodeTaints, query.Requirements.tolerations, utiltaints.IsSchedulingTaint, true)
		if untolerated {
			log.V(5).Info("excluding node with untolerated taint", "domainID", leaf.id, "taint", taint)
			query.Stats.recordExclusion(exclusionTaints, &taint)
			continue
		}

		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if nodeLabels := leaf.node.Labels; nodeLabels != nil {
			nodeLabelSet = nodeLabels
		}

		if !query.Requirements.selector.Matches(nodeLabelSet) {
			log.V(5).Info("excluding node that doesn't match nodeSelectors", "domainID", leaf.id, "nodeLabels", nodeLabelSet)
			query.Stats.recordExclusion(exclusionNodeSelector, nil)
			continue
		}

		// 3. Check Node against Affinity Node Selector
		nodeObj := leaf.node
		if query.Requirements.affinitySelector != nil && !query.Requirements.affinitySelector.Match(nodeObj) {
			log.V(5).Info("excluding node due to an affinity mismatch", "domainID", leaf.id)
			query.Stats.recordExclusion(exclusionAffinity, nil)
			continue
		}

		// 4. Calculate Affinity Score
		var affinityScore int64
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && query.Requirements.preferredSchedulingTerms != nil {
			affinityScore = query.Requirements.preferredSchedulingTerms.Score(nodeObj)
		}

		// 5. Track the matching leaf as feasible
		feasibleLeaves = append(feasibleLeaves, matchedLeaf{leaf: leaf, affinityScore: affinityScore})
	}
	return feasibleLeaves, nil
}
