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

package queue

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/workload"
)

// inadmissibleWorkloads is a thin wrapper around a map to encapsulate
// operations on inadmissible workloads and prevent direct map access.
type inadmissibleWorkloads map[workload.Reference]*workload.Info

// get retrieves a workload from the inadmissible workloads map.
// Returns the workload if it exists, otherwise returns nil.
func (iw inadmissibleWorkloads) get(key workload.Reference) *workload.Info {
	return iw[key]
}

// delete removes a workload from the inadmissible workloads map.
func (iw inadmissibleWorkloads) delete(key workload.Reference) {
	delete(iw, key)
}

// insert adds a workload to the inadmissible workloads map.
func (iw inadmissibleWorkloads) insert(key workload.Reference, wInfo *workload.Info) {
	iw[key] = wInfo
}

// len returns the number of inadmissible workloads.
func (iw inadmissibleWorkloads) len() int {
	return len(iw)
}

// empty returns true if there are no inadmissible workloads.
func (iw inadmissibleWorkloads) empty() bool {
	return len(iw) == 0
}

// hasKey returns true if the workload exists in the inadmissible workloads map.
func (iw inadmissibleWorkloads) hasKey(key workload.Reference) bool {
	_, ok := iw[key]
	return ok
}

// replaceAll replaces all inadmissible workloads with the provided map.
func (iw *inadmissibleWorkloads) replaceAll(newMap inadmissibleWorkloads) {
	*iw = newMap
}

// requeueWorkloadsCQ moves all workloads in the same
// cohort with this ClusterQueue from inadmissibleWorkloads to heap. If the
// cohort of this ClusterQueue is empty, it just moves all workloads in this
// ClusterQueue. If at least one workload is moved, returns true, otherwise
// returns false.
// The events listed below could make workloads in the same cohort admissible.
// Then requeueWorkloadsCQ need to be invoked.
// 1. delete events for any admitted workload in the cohort.
// 2. add events of any cluster queue in the cohort.
// 3. update events of any cluster queue in the cohort.
// 4. update of cohort.
//
// WARNING: must hold a read-lock on the manager when calling,
// or otherwise risk encountering an infinite loop if a Cohort
// cycle is introduced.
func requeueWorkloadsCQ(ctx context.Context, m *Manager, cq *ClusterQueue) bool {
	if cq.HasParent() {
		return requeueWorkloadsCohort(ctx, m, cq.Parent())
	}
	return queueInadmissibleWorkloads(ctx, cq, m.client)
}

// moveWorkloadsCohorts checks for a cycle, the moves all inadmissible
// workloads in the Cohort tree. If a cycle exists, or no workloads were
// moved, it returns false.
//
// WARNING: must hold a read-lock on the manager when calling,
// or otherwise risk encountering an infinite loop if a Cohort
// cycle is introduced.
func requeueWorkloadsCohort(ctx context.Context, m *Manager, cohort *cohort) bool {
	log := ctrl.LoggerFrom(ctx)

	if hierarchy.HasCycle(cohort) {
		log.V(2).Info("Attempted to move workloads from Cohort which has cycle", "cohort", cohort.GetName())
		return false
	}
	root := cohort.getRootUnsafe()
	log.V(2).Info("Attempting to move workloads", "cohort", cohort.Name, "root", root.Name)
	return requeueWorkloadsCohortSubtree(ctx, m, root)
}

func requeueWorkloadsCohortSubtree(ctx context.Context, m *Manager, cohort *cohort) bool {
	queued := false
	for _, clusterQueue := range cohort.ChildCQs() {
		queued = queueInadmissibleWorkloads(ctx, clusterQueue, m.client) || queued
	}
	for _, childCohort := range cohort.ChildCohorts() {
		queued = requeueWorkloadsCohortSubtree(ctx, m, childCohort) || queued
	}
	return queued
}

// queueInadmissibleWorkloads moves all workloads from inadmissibleWorkloads to heap.
// If at least one workload is moved, returns true, otherwise returns false.
func queueInadmissibleWorkloads(ctx context.Context, c *ClusterQueue, client client.Client) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	log := ctrl.LoggerFrom(ctx)
	c.queueInadmissibleCycle = c.popCycle
	if c.inadmissibleWorkloads.empty() {
		return false
	}
	log.V(2).Info("Resetting the head of the ClusterQueue", "clusterQueue", c.name)
	newInadmissibleWorkloads := make(inadmissibleWorkloads)
	moved := false
	for key, wInfo := range c.inadmissibleWorkloads {
		ns := corev1.Namespace{}
		err := client.Get(ctx, types.NamespacedName{Name: wInfo.Obj.Namespace}, &ns)
		if err != nil || !c.namespaceSelector.Matches(labels.Set(ns.Labels)) || !c.backoffWaitingTimeExpired(wInfo) {
			newInadmissibleWorkloads.insert(key, wInfo)
		} else {
			moved = c.heap.PushIfNotPresent(wInfo) || moved
		}
	}

	c.inadmissibleWorkloads.replaceAll(newInadmissibleWorkloads)
	log.V(5).Info("Moved all workloads from inadmissibleWorkloads back to heap", "clusterQueue", c.name)
	return moved
}

// QueueInadmissibleWorkloads moves all inadmissibleWorkloads in
// corresponding ClusterQueues to heap. If at least one workload queued,
// we will broadcast the event.
func QueueInadmissibleWorkloads(ctx context.Context, m *Manager, cqNames sets.Set[kueue.ClusterQueueReference]) {
	m.Lock()
	defer m.Unlock()
	if len(cqNames) == 0 {
		return
	}

	// Track processed cohort roots to avoid requeuing the same hierarchy
	// multiple times when multiple CQs in cqNames share a root.
	processedRoots := sets.New[kueue.CohortReference]()
	var queued bool
	for name := range cqNames {
		cq := m.hm.ClusterQueue(name)
		if cq == nil {
			continue
		}
		if cq.HasParent() && !hierarchy.HasCycle(cq.Parent()) {
			rootName := cq.Parent().getRootUnsafe().GetName()
			if processedRoots.Has(rootName) {
				continue
			}
			processedRoots.Insert(rootName)
		}
		if requeueWorkloadsCQ(ctx, m, cq) {
			queued = true
		}
	}

	if queued {
		m.Broadcast()
	}
}
