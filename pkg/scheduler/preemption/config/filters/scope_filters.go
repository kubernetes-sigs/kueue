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

package filters

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/workload"
)

// withinClusterQueueFilter permits only candidate workloads residing in the exact same ClusterQueue as the preemptor.
type withinClusterQueueFilter struct {
	preemptorCQ kueue.ClusterQueueReference
}

// NewWithinClusterQueueFilter creates a ClusterQueueFilter permitting only the specified ClusterQueue.
func NewWithinClusterQueueFilter(preemptorCQ kueue.ClusterQueueReference) ClusterQueueFilter {
	return &withinClusterQueueFilter{preemptorCQ: preemptorCQ}
}

func (f *withinClusterQueueFilter) Matches(cq *schdcache.ClusterQueueSnapshot) bool {
	return cq.Name == f.preemptorCQ
}

// withinParentCohortFilter permits ClusterQueues sharing the immediate parent Cohort, or the preemptor's own ClusterQueue.
type withinParentCohortFilter struct {
	preemptorCQ     kueue.ClusterQueueReference
	preemptorCohort kueue.CohortReference
	hasCohort       bool
}

// NewWithinParentCohortFilter encapsulates preemptor cohort resolution and caches the match target.
func NewWithinParentCohortFilter(preemptorCQ kueue.ClusterQueueReference, snapshot *schdcache.Snapshot) ClusterQueueFilter {
	f := &withinParentCohortFilter{preemptorCQ: preemptorCQ}
	if snapshotCQ := snapshot.ClusterQueue(preemptorCQ); snapshotCQ != nil && snapshotCQ.HasParent() {
		f.preemptorCohort = snapshotCQ.Parent().GetName()
		f.hasCohort = true
	}
	return f
}

func (f *withinParentCohortFilter) Matches(cq *schdcache.ClusterQueueSnapshot) bool {
	// The preemptor's own ClusterQueue is always within the same cohort boundary.
	if cq.Name == f.preemptorCQ {
		return true
	}
	if !f.hasCohort || !cq.HasParent() {
		return false
	}
	return cq.Parent().GetName() == f.preemptorCohort
}

// withinCohortTreeFilter permits ClusterQueues in the same Cohort Tree (sharing the root Cohort ancestor),
// or the preemptor's own ClusterQueue.
type withinCohortTreeFilter struct {
	preemptorCQ         kueue.ClusterQueueReference
	preemptorRootCohort kueue.CohortReference
	hasCohort           bool
}

// NewWithinCohortTreeFilter encapsulates preemptor root cohort resolution and caches the match target.
func NewWithinCohortTreeFilter(preemptorCQ kueue.ClusterQueueReference, snapshot *schdcache.Snapshot) ClusterQueueFilter {
	f := &withinCohortTreeFilter{preemptorCQ: preemptorCQ}
	if snapshotCQ := snapshot.ClusterQueue(preemptorCQ); snapshotCQ != nil && snapshotCQ.HasParent() {
		if root := snapshotCQ.Parent().Root(); root != nil {
			f.preemptorRootCohort = root.GetName()
			f.hasCohort = true
		}
	}
	return f
}

func (f *withinCohortTreeFilter) Matches(cq *schdcache.ClusterQueueSnapshot) bool {
	// The preemptor's own ClusterQueue is always within the same cohort tree boundary.
	if cq.Name == f.preemptorCQ {
		return true
	}
	if !f.hasCohort || !cq.HasParent() {
		return false
	}
	root := cq.Parent().Root()
	return root != nil && root.GetName() == f.preemptorRootCohort
}

// withinLocalQueueFilter is a WorkloadFilter matching workloads in the exact same Namespace and LocalQueue.
type withinLocalQueueFilter struct {
	namespace string
	queueName kueue.LocalQueueName
}

// NewWithinLocalQueueFilter creates a WorkloadFilter matching the given Namespace and LocalQueue.
func NewWithinLocalQueueFilter(namespace string, queueName kueue.LocalQueueName) WorkloadFilter {
	return &withinLocalQueueFilter{
		namespace: namespace,
		queueName: queueName,
	}
}

func (f *withinLocalQueueFilter) Matches(wl *workload.Info) bool {
	return wl.Obj.Namespace == f.namespace && wl.Obj.Spec.QueueName == f.queueName
}
