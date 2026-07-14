package scheduler

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"sigs.k8s.io/kueue/pkg/features"
)

// NodeFeasibilityChecker determines which topology leaves can satisfy pod requirements.
type NodeFeasibilityChecker interface {
	FindFeasibleLeaves(ctx context.Context, log logr.Logger, leaves []*leafDomain, reqs *topologyAssignmentPodRequirements, stats *ExclusionStats) ([]*leafDomain, error)
}

// SchedulingSimulator acts as a factory for the feasibility checker.
type SchedulingSimulator interface {
	NewFeasibilityChecker(ctx context.Context, nodes []*nodeInfo) (NodeFeasibilityChecker, error)
}

type defaultSimulator struct{}

func NewDefaultSimulator() SchedulingSimulator {
	return &defaultSimulator{}
}

func (s *defaultSimulator) NewFeasibilityChecker(ctx context.Context, nodes []*nodeInfo) (NodeFeasibilityChecker, error) {
	return &defaultChecker{}, nil
}

type defaultChecker struct{}

func (c *defaultChecker) FindFeasibleLeaves(ctx context.Context, log logr.Logger, leaves []*leafDomain, reqs *topologyAssignmentPodRequirements, stats *ExclusionStats) ([]*leafDomain, error) {
	var feasibleLeaves = make([]*leafDomain, 0, len(leaves))
	for _, leaf := range leaves {
		stats.TotalNodes++
		// 1. Check Tolerations against Node Taints
		nodeTaints := leaf.node.Taints
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(log, nodeTaints, reqs.tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}, true)
		if untolerated {
			stats.Taints[taint.ToString()]++
			continue
		}

		// 2. Check Node Labels against Compiled Selector
		var nodeLabelSet labels.Set
		if nodeLabels := leaf.node.Labels; nodeLabels != nil {
			nodeLabelSet = nodeLabels
		}

		if !reqs.selector.Matches(nodeLabelSet) {
			stats.NodeSelector++
			continue
		}

		// 3. Check Node against Affinity Node Selector
		nodeObj := leaf.node.toNode()
		if reqs.affinitySelector != nil && !reqs.affinitySelector.Match(nodeObj) {
			stats.Affinity++
			continue
		}

		// 4. Calculate Affinity Score
		if features.Enabled(features.TASRespectNodeAffinityPreferred) && reqs.preferredSchedulingTerms != nil {
			leaf.affinityScore += reqs.preferredSchedulingTerms.Score(nodeObj)
		}

		// 5. Track the matching leaf as feasible
		feasibleLeaves = append(feasibleLeaves, leaf)
	}
	return feasibleLeaves, nil
}
