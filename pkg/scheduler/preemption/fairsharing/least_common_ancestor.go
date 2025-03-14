// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fairsharing

import "sigs.k8s.io/kueue/pkg/cache"

// almostLca is defined on two ClusterQueues, as the two nodes before
// the lowest shared node - the LeastCommonAncestor (LCA). While LCA
// is always a Cohort, almostLca may be a ClusterQueue or a Cohort.
type almostLca interface {
	DominantResourceShare() int
}

// getAlmostLcas returns almostLCAs of (preemptor, target).
func getAlmostLcas(t *TargetClusterQueue) (almostLca, almostLca) {
	lca := getLca(t)
	return getAlmostLca(t.ordering.preemptorCq, lca), getAlmostLca(t.targetCq, lca)
}

// getLca traverses from a ClusterQueue towards the root Cohort,
// returning the first Cohort which contains the preemptor
// ClusterQueue in its subtree.
func getLca(t *TargetClusterQueue) *cache.CohortSnapshot {
	cohort := t.targetCq.Parent()
	for {
		if t.ordering.onPathToPreemptorCQ(cohort) {
			return cohort
		}
		cohort = cohort.Parent()
	}
}

// getAlmostLca traverses from a ClusterQueue towards the root,
// returning the first Cohort or ClusterQueue that has the
// LeastCommonAncestor as its parent.
func getAlmostLca(cq *cache.ClusterQueueSnapshot, lca *cache.CohortSnapshot) almostLca {
	if cq.Parent() == lca {
		return cq
	}
	cohort := cq.Parent()
	for {
		if cohort.Parent() == lca {
			return cohort
		}
		cohort = cohort.Parent()
	}
}
