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

import (
	"slices"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

// AlmostLCA is defined on two ClusterQueues, as the two nodes before
// the lowest shared node - the LeastCommonAncestor (LCA). While LCA
// is always a Cohort, AlmostLCA may be a ClusterQueue or a Cohort.
type AlmostLCA interface {
	DominantResourceShare() schdcache.DRS
}

// AlmostLCAs returns AlmostLCAs of (preemptor, target).
func AlmostLCAs(preemptorCq, targetCq *schdcache.ClusterQueueSnapshot) (AlmostLCA, AlmostLCA) {
	lca := getLCA(preemptorCq, targetCq)
	return getAlmostLCA(preemptorCq, lca), getAlmostLCA(targetCq, lca)
}

// getLCA traverses from a ClusterQueue towards the root Cohort,
// returning the first Cohort which contains the preemptor
// ClusterQueue in its subtree.
func getLCA(preemptorCq, targetCq *schdcache.ClusterQueueSnapshot) *schdcache.CohortSnapshot {
	preemptorAncestors := slices.Collect(preemptorCq.PathParentToRoot())
	for ancestor := range targetCq.PathParentToRoot() {
		if slices.Contains(preemptorAncestors, ancestor) {
			return ancestor
		}
	}

	// to make the compiler happy
	panic("serious bug: could not find LeastCommonAncestor")
}

// getAlmostLCA traverses from a ClusterQueue towards the root,
// returning the first Cohort or ClusterQueue that has the
// LeastCommonAncestor as its parent.
func getAlmostLCA(cq *schdcache.ClusterQueueSnapshot, lca *schdcache.CohortSnapshot) AlmostLCA {
	var aLca AlmostLCA = cq
	for ancestor := range cq.PathParentToRoot() {
		if ancestor == lca {
			return aLca
		}
		aLca = ancestor
	}
	// to make the compiler happy
	panic("serious bug: could not find AlmostLeastCommonAncestor")
}
