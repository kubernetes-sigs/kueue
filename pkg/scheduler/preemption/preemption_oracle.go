package preemption

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

func NewOracle(preemptor *Preemptor, snapshot *cache.Snapshot) *PreemptionOracle {
	return &PreemptionOracle{preemptor, snapshot}
}

type PreemptionOracle struct {
	preemptor *Preemptor
	snapshot  *cache.Snapshot
}

// IsReclaimPossible determines if a ClusterQueue can fit this
// FlavorResource by reclaiming its nominal quota which it lent to its
// Cohort.
func (p *PreemptionOracle) IsReclaimPossible(log logr.Logger, cq *cache.ClusterQueueSnapshot, wl workload.Info, fr resources.FlavorResource, quantity int64) bool {
	if cq.Usage[fr]+quantity > cq.QuotaFor(fr).Nominal {
		return false
	}

	for _, candidate := range p.preemptor.getTargets(log, wl, resources.FlavorResourceQuantities{fr: quantity}, sets.New(fr), p.snapshot) {
		if candidate.WorkloadInfo.ClusterQueue == cq.Name {
			return false
		}
	}
	return true
}
