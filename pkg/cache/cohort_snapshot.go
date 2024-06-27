package cache

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/resources"
)

type CohortSnapshot struct {
	Name    string
	Members sets.Set[*ClusterQueueSnapshot]

	// RequestableResources equals to the sum of LendingLimit when feature LendingLimit enabled.
	RequestableResources resources.FlavorResourceQuantities
	Usage                resources.FlavorResourceQuantities
	Lendable             map[corev1.ResourceName]int64

	// AllocatableResourceGeneration equals to
	// the sum of allocatable generation among its members.
	AllocatableResourceGeneration int64
}
