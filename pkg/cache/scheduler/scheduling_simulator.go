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

// nodeFeasibilityChecker determines which topology leaves can satisfy pod requirements.
type nodeFeasibilityChecker interface {
	FindFeasibleNodes(ctx context.Context, log logr.Logger, leaves []*leafDomain, reqs *topologyAssignmentPodRequirements, stats *ExclusionStats) ([]matchedLeaf, error)
}

// SchedulingSimulator acts as a factory for the feasibility checker.
type SchedulingSimulator interface {
	NewFeasibilityChecker(ctx context.Context, nodes []*corev1.Node) (nodeFeasibilityChecker, error)
}

type defaultSimulator struct{}

func NewDefaultSimulator() SchedulingSimulator {
	return &defaultSimulator{}
}

func (s *defaultSimulator) NewFeasibilityChecker(ctx context.Context, nodes []*corev1.Node) (nodeFeasibilityChecker, error) {
	return &defaultChecker{}, nil
}

type defaultChecker struct{}

func (c *defaultChecker) FindFeasibleNodes(ctx context.Context, log logr.Logger, leaves []*leafDomain, reqs *topologyAssignmentPodRequirements, stats *ExclusionStats) ([]matchedLeaf, error) {
	var feasibleLeaves = make([]matchedLeaf, 0, len(leaves))
	for _, leaf := range leaves {
		if leaf.node == nil {
			feasibleLeaves = append(feasibleLeaves, matchedLeaf{leaf: leaf, affinityScore: leaf.affinityScore})
			continue
		}
		// 1. Check Tolerations against Node Taints
		nodeTaints := leaf.node.Spec.Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(log, nodeTaints, reqs.tolerations, utiltaints.IsSchedulingTaint, true)
		if untolerated {
			log.V(5).Info("excluding node with untolerated taint", "domainID", leaf.domain.id, "taint", taint)
			stats.recordExclusion(exclusionTaints, &taint)
			continue
		}

		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if nodeLabels := leaf.node.Labels; nodeLabels != nil {
			nodeLabelSet = nodeLabels
		}

		if !reqs.selector.Matches(nodeLabelSet) {
			log.V(5).Info("excluding node due to a node label selector mismatch", "domainID", leaf.domain.id)
			stats.recordExclusion(exclusionNodeSelector, nil)
			continue
		}

		// 3. Check Node against Affinity Node Selector
		nodeObj := leaf.node
		if reqs.affinitySelector != nil && !reqs.affinitySelector.Match(nodeObj) {
			log.V(5).Info("excluding node due to an affinity mismatch", "domainID", leaf.domain.id)
			stats.recordExclusion(exclusionAffinity, nil)
			continue
		}

		// 4. Calculate Affinity Score
		var affinityScore int64
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && reqs.preferredSchedulingTerms != nil {
			affinityScore = reqs.preferredSchedulingTerms.Score(nodeObj)
		}

		// 5. Track the matching leaf as feasible
		feasibleLeaves = append(feasibleLeaves, matchedLeaf{leaf: leaf, affinityScore: affinityScore})
	}
	return feasibleLeaves, nil
}
