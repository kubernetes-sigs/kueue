package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/labels"

	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestSortedDomainsWithPreferredAffinity(t *testing.T) {
	levels := []string{"kubernetes.io/hostname"}
	nodes := []corev1.Node{
		*node.MakeNode("node-preferred").
			Label("kubernetes.io/hostname", "node-preferred").
			Label("region", "us-west").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}).Obj(),
		*node.MakeNode("node-other").
			Label("kubernetes.io/hostname", "node-other").
			Label("region", "us-east").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("1"),
			}).Obj(),
	}

	preferredAffinity := []corev1.PreferredSchedulingTerm{
		{
			Weight: 10,
			Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      "region",
						Operator: corev1.NodeSelectorOpIn,
						Values:   []string{"us-west"},
					},
				},
			},
		},
	}

	_, log := utiltesting.ContextWithLog(t)
	s := newTASFlavorSnapshot(log, "dummy", levels, nil, "")
	for _, node := range nodes {
		s.addNode(node)
	}
	s.initialize()

	// Simulate resource availability
	s.leaves[tas.DomainID([]string{"node-preferred"})].sliceState = 1
	s.leaves[tas.DomainID([]string{"node-other"})].sliceState = 1

	// Manually trigger fillInCounts logic for affinity score (since we can't easily call fillInCounts directly without mocking everything)
	// Or we can just manually set affinityScore to verify sorting logic.
	// But better to verify fillInCounts logic too.
	// Let's call fillInCounts via findTopologyAssignment? No, too complex.
	// Let's just call fillInCounts directly.

	requests := resources.Requests{corev1.ResourceCPU: 1}

	s.fillInCounts(
		requests,
		nil,
		nil,
		1,
		0,
		false,
		nil,
		labels.Everything(), // selector
		nil,                 // affinityNodeSelector
		preferredAffinity,
		"",
	)

	domains := []*domain{
		s.domainsPerLevel[0][tas.DomainID([]string{"node-other"})],
		s.domainsPerLevel[0][tas.DomainID([]string{"node-preferred"})],
	}

	gotDomains := s.sortedDomains(domains, false)
	gotValues := make([]string, len(gotDomains))
	for i, d := range gotDomains {
		gotValues[i] = d.levelValues[0]
	}

	want := []string{"node-preferred", "node-other"}
	if diff := cmp.Diff(want, gotValues); diff != "" {
		t.Errorf("unexpected sorted domains (-want,+got): %s", diff)
	}
}
