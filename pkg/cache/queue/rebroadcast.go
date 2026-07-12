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
	"time"

	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
)

// rebroadcastMinGrace ensures the timer fires after the arming scheduling
// cycle completes its requeue, avoiding a lost wakeup against an empty
// inadmissible set.
const rebroadcastMinGrace = time.Second

// rebroadcastKey identifies a root Cohort or standalone ClusterQueue for
// timer deduplication.
type rebroadcastKey struct {
	cohort       kueue.CohortReference
	clusterQueue kueue.ClusterQueueReference
}

// rebroadcastKeyLocked resolves the rebroadcast target for a ClusterQueue.
// The Manager's lock must be held.
func (m *Manager) rebroadcastKeyLocked(cqName kueue.ClusterQueueReference) (rebroadcastKey, bool) {
	cq := m.hm.ClusterQueue(cqName)
	switch {
	case cq == nil:
		return rebroadcastKey{}, false
	case !cq.HasParent():
		return rebroadcastKey{clusterQueue: cq.name}, true
	case hierarchy.HasCycle(cq.Parent()):
		return rebroadcastKey{}, false
	default:
		return rebroadcastKey{cohort: cq.Parent().getRootUnsafe().GetName()}, true
	}
}

// RebroadcastAtTime schedules a retry of inadmissible workloads shortly
// after time t. Deduplicates by Cohort tree to avoid timer accumulation.
func (m *Manager) RebroadcastAtTime(t time.Time, cqNames ...kueue.ClusterQueueReference) {
	m.Lock()
	defer m.Unlock()
	delay := max(t.Sub(m.clock.Now()), 0) + rebroadcastMinGrace
	fireAt := m.clock.Now().Add(delay)
	var keys []rebroadcastKey
	toNotify := sets.New[kueue.ClusterQueueReference]()
	for _, cqName := range cqNames {
		key, ok := m.rebroadcastKeyLocked(cqName)
		if !ok {
			continue
		}
		if armed, ok := m.pendingRebroadcasts[key]; ok && !armed.After(fireAt) {
			continue
		}
		m.pendingRebroadcasts[key] = fireAt
		keys = append(keys, key)
		toNotify.Insert(cqName)
	}
	if len(toNotify) == 0 {
		return
	}
	m.clock.AfterFunc(delay, func() {
		m.Lock()
		for _, key := range keys {
			delete(m.pendingRebroadcasts, key)
		}
		m.Unlock()
		NotifyRetryInadmissible(m, toNotify)
		// Wake the scheduler directly as well: the pending preemptor may be
		// heap-resident (e.g. in a StrictFIFO ClusterQueue) rather than in
		// the inadmissible set, in which case NotifyRetryInadmissible moves
		// nothing and would not broadcast.
		m.Broadcast()
	})
}
