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
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TargetClusterQueue is a ClusterQueue which yields candidate
// workloads for preemption.
type TargetClusterQueue struct {
	ordering *TargetClusterQueueOrdering
	targetCq *cache.ClusterQueueSnapshot
}

// InClusterQueuePreemption indicates that the TargetClusterQueue is
// the preemptor ClusterQueue; i.e. the preemptor ClusterQueue is
// considering its own workloads for priority based preemption.
func (t *TargetClusterQueue) InClusterQueuePreemption() bool {
	return t.targetCq == t.ordering.preemptorCq
}

func (t *TargetClusterQueue) PopWorkload() *workload.Info {
	head := t.ordering.clusterQueueToTarget[t.targetCq.GetName()][0]
	t.ordering.clusterQueueToTarget[t.targetCq.GetName()] = t.ordering.clusterQueueToTarget[t.targetCq.GetName()][1:]
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
	preemptorAlmostLca, targetAlmostLca := getAlmostLcas(t)
	return PreemptorNewShare(preemptorAlmostLca.DominantResourceShare()), TargetOldShare(targetAlmostLca.DominantResourceShare())
}

// ComputeTargetNewShare returns DominantResourceShare of the
// TargetClusterQueue's AlmostLeastCommonAncestor, after removing
// provided workload.
//
// This simulation is required, as we need to update usage while
// respecting lending and borrowing limits, which the cache/snapshot
// implements.
func (t *TargetClusterQueue) ComputeTargetNewShare(wl *workload.Info) TargetNewShare {
	_, almostLca := getAlmostLcas(t)
	usage := wl.Usage()
	revertSimulation := t.targetCq.SimulateUsageRemoval(usage)
	drs := almostLca.DominantResourceShare()
	revertSimulation()
	return TargetNewShare(drs)
}
