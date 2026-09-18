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
	"k8s.io/apimachinery/pkg/util/sets"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TargetClusterQueue is a ClusterQueue which yields candidate
// workloads for preemption.
type TargetClusterQueue struct {
	ordering *TargetClusterQueueOrdering
	targetCq *schdcache.ClusterQueueSnapshot
}

// InClusterQueuePreemption indicates that the TargetClusterQueue is
// the preemptor ClusterQueue; i.e. the preemptor ClusterQueue is
// considering its own workloads for priority based preemption.
func (t *TargetClusterQueue) InClusterQueuePreemption() bool {
	return t.targetCq == t.ordering.preemptorCq
}

func (t *TargetClusterQueue) PopWorkload() *workload.Info {
	cqt := t.ordering.clusterQueueToTarget
	cqName := t.targetCq.GetName()

	head := cqt[cqName][0]
	cqt[cqName] = cqt[cqName][1:]
	return head
}

func (t *TargetClusterQueue) HasWorkload() bool {
	return t.ordering.hasWorkload(t.targetCq)
}

// ComputeShares computes the DominantResourceShares of the premptor
// and target ClusterQueues' AlmostLeastCommonAncestors. These shares
// do not depend on the removal of the workload being considered for
// preemption.
func (t *TargetClusterQueue) ComputeShares() (PreemptorNewShare, TargetOldShare) {
	preemptorAlmostLCA, targetAlmostLCA := getAlmostLCAs(t)
	return PreemptorNewShare(preemptorAlmostLCA.DominantResourceShare()), TargetOldShare(targetAlmostLCA.DominantResourceShare())
}

// PreemptorWithinNominal reports whether the preemptor has a nominal
// claim on the contested flavor-resources, entitling it to reclaim them
// from the candidate target regardless of DominantResourceShare.
//
// The claim holds when any node on the path from the preemptor
// ClusterQueue up to its almostLCA with the target stays within nominal
// quota for every contested flavor-resource. The ClusterQueue itself
// qualifying covers a queue reclaiming its own nominal quota; an
// ancestor Cohort qualifying gives that Cohort's descendants
// preferential access to the Cohort's own nominal quota, even when the
// ClusterQueue is borrowing. Nodes above the almostLCA are shared with
// the target, so their quota is not contested between the two.
//
// The incoming workload must already be simulated before calling this
// method.
func (t *TargetClusterQueue) PreemptorWithinNominal(frs sets.Set[resources.FlavorResource]) bool {
	for _, node := range preemptorPathToAlmostLCA(t) {
		if !borrowsAny(node, frs) {
			return true
		}
	}
	return false
}

// borrowsAny reports whether node's usage exceeds its nominal quota for
// any of the provided flavor-resources.
func borrowsAny(node almostLCA, frs sets.Set[resources.FlavorResource]) bool {
	for fr := range frs {
		if node.BorrowingWith(fr, resources.NewAmount(0)) {
			return true
		}
	}
	return false
}

// ComputeTargetShareAfterRemoval returns DominantResourceShare of the
// TargetClusterQueue's AlmostLeastCommonAncestor, after removing
// provided workload.
//
// This simulation is required so that new usage is accounted for in
// each of the ClusterQueue's parent Cohorts.  We can't trivially do
// this operation on just the almostLCA, as usage stored at almostLCA
// will depend on LendingLimits of the children. See
// cache.resource_node.go.
func (t *TargetClusterQueue) ComputeTargetShareAfterRemoval(wl *workload.Info) TargetNewShare {
	revertSimulation := t.targetCq.SimulateUsageRemoval(wl.Usage())
	defer revertSimulation()

	_, almostLCA := getAlmostLCAs(t)
	return TargetNewShare(almostLCA.DominantResourceShare())
}

// GetTargetCq returns the target ClusterQueue snapshot.
func (t *TargetClusterQueue) GetTargetCq() *schdcache.ClusterQueueSnapshot {
	return t.targetCq
}
