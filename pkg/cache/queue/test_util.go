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
	"maps"
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// testInadmissibleWorkloadRequeuer buffers requeue events
// to be processed synchronously, for use in unit tests.
type testInadmissibleWorkloadRequeuer struct {
	manager *Manager
	cqs     sets.Set[kueue.ClusterQueueReference]
	cohorts sets.Set[kueue.CohortReference]
	// cqDelays and cohortDelays hold the earliest positive delay requested since
	// the last ProcessRequeues.
	cqDelays     map[kueue.ClusterQueueReference]time.Duration
	cohortDelays map[kueue.CohortReference]time.Duration
}

func (r *testInadmissibleWorkloadRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	r.cqs.Insert(cqName)
}

func (r *testInadmissibleWorkloadRequeuer) notifyClusterQueueAfter(cqName kueue.ClusterQueueReference, delay time.Duration) {
	r.cqs.Insert(cqName)
	if d, found := r.cqDelays[cqName]; delay > 0 && (!found || delay < d) {
		r.cqDelays[cqName] = delay
	}
}

func (r *testInadmissibleWorkloadRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	r.cohorts.Insert(cohortName)
}

func (r *testInadmissibleWorkloadRequeuer) notifyCohortAfter(cohortName kueue.CohortReference, delay time.Duration) {
	r.cohorts.Insert(cohortName)
	if d, found := r.cohortDelays[cohortName]; delay > 0 && (!found || delay < d) {
		r.cohortDelays[cohortName] = delay
	}
}

func (r *testInadmissibleWorkloadRequeuer) setManager(manager *Manager) {
	r.manager = manager
}

// ClusterQueueDelays returns the earliest positive delay requested per
// ClusterQueue since the last ProcessRequeues.
func (r *testInadmissibleWorkloadRequeuer) ClusterQueueDelays() map[kueue.ClusterQueueReference]time.Duration {
	return maps.Clone(r.cqDelays)
}

// CohortDelays returns the earliest positive delay requested per Cohort since
// the last ProcessRequeues.
func (r *testInadmissibleWorkloadRequeuer) CohortDelays() map[kueue.CohortReference]time.Duration {
	return maps.Clone(r.cohortDelays)
}

// ProcessRequeues requeues all the inadmissible workloads belonging to
// Cohorts/Queues which were notified, regardless of any requested delay.
// Returns the total number of workloads moved.
func (w *testInadmissibleWorkloadRequeuer) ProcessRequeues(ctx context.Context) int {
	total := 0
	for cqName := range w.cqs {
		total += requeueWorkloadsCQ(ctx, w.manager, cqName)
	}
	for cohortName := range w.cohorts {
		total += requeueWorkloadsCohort(ctx, w.manager, cohortName)
	}
	w.cqs.Clear()
	w.cohorts.Clear()
	clear(w.cqDelays)
	clear(w.cohortDelays)
	return total
}

// NewManagerForUnitTests creates a new Manager for testing purposes.
// This test manager, though exported, is not included in Kueue binary.
// Note that this function is not found when running:
// make build && go tool nm ./bin/manager | grep "NewManager"
func NewManagerForUnitTests(client client.Client, checker StatusChecker, options ...Option) *Manager {
	manager, _ := NewManagerForUnitTestsWithRequeuer(client, checker, options...)
	return manager
}

// NewManagerForUnitTestsWithRequeuer creates a new Manager for testing purposes, pre-configured with a testInadmissibleWorkloadRequeuer.
func NewManagerForUnitTestsWithRequeuer(client client.Client, checker StatusChecker, options ...Option) (*Manager, *testInadmissibleWorkloadRequeuer) {
	requeuer := &testInadmissibleWorkloadRequeuer{
		cqs:          sets.New[kueue.ClusterQueueReference](),
		cohorts:      sets.New[kueue.CohortReference](),
		cqDelays:     make(map[kueue.ClusterQueueReference]time.Duration),
		cohortDelays: make(map[kueue.CohortReference]time.Duration),
	}

	manager := NewManager(client, checker, requeuer, options...)
	return manager, requeuer
}
