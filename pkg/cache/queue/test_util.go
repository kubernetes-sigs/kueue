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
}

func (r *testInadmissibleWorkloadRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	r.cqs.Insert(cqName)
}

func (r *testInadmissibleWorkloadRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	r.cohorts.Insert(cohortName)
}

func (r *testInadmissibleWorkloadRequeuer) setManager(manager *Manager) {
	r.manager = manager
}

// ProcessRequeues requeues all the inadmissible workloads
// belonging to Cohorts/Queues which were notified.
func (w *testInadmissibleWorkloadRequeuer) ProcessRequeues(ctx context.Context) {
	for cqName := range w.cqs {
		requeueWorkloadsCQ(ctx, w.manager, cqName)
	}
	for cohortName := range w.cohorts {
		requeueWorkloadsCohort(ctx, w.manager, cohortName)
	}
	w.cqs.Clear()
	w.cohorts.Clear()
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
		cqs:     sets.New[kueue.ClusterQueueReference](),
		cohorts: sets.New[kueue.CohortReference](),
	}

	manager := NewManager(client, checker, requeuer, options...)
	return manager, requeuer
}
