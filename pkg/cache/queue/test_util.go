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
	"time"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	requeueBatchPeriodIntegrationTests = 1 * time.Millisecond
)

// testInadmissibleWorkloadRequeuer buffers requeue events
// to be processed synchronously, for use in unit tests.
type testInadmissibleWorkloadRequeuer struct {
	manager *Manager
	cqs     sets.Set[kueue.ClusterQueueReference]
	cohorts sets.Set[kueue.CohortReference]
}

func (w *testInadmissibleWorkloadRequeuer) notifyClusterQueue(cqName kueue.ClusterQueueReference) {
	w.cqs.Insert(cqName)
}

func (w *testInadmissibleWorkloadRequeuer) notifyCohort(cohortName kueue.CohortReference) {
	w.cohorts.Insert(cohortName)
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

// NewManagerForUnitTestsWithRequeuer creates a new Manager for testing purposes, pre-configured with a testInadmissibleWorkloadRequeuer.
func NewManagerForUnitTestsWithRequeuer(client client.Client, checker StatusChecker, options ...Option) (*Manager, *testInadmissibleWorkloadRequeuer) {
	watcher := &testInadmissibleWorkloadRequeuer{
		cqs:     sets.New[kueue.ClusterQueueReference](),
		cohorts: sets.New[kueue.CohortReference](),
	}
	options = append(options, func(m *Manager) {
		m.inadmissibleWorkloadRequeuer = watcher
	})
	m := NewManager(client, checker, options...)
	watcher.manager = m
	return m, watcher
}

// NewManagerForUnitTests creates a new Manager for testing purposes.
func NewManagerForUnitTests(client client.Client, checker StatusChecker, options ...Option) *Manager {
	manager, _ := NewManagerForUnitTestsWithRequeuer(client, checker, options...)
	return manager
}

// NewManagerForIntegrationTests is a factory for Integration Tests, setting the
// batch period to a much lower value (requeueBatchPeriodIntegrationTests).
func NewManagerForIntegrationTests(client client.Client, checker StatusChecker, options ...Option) *Manager {
	requeuer := &inadmissibleWorkloadRequeuer{
		eventCh:     make(chan event.TypedGenericEvent[requeueRequest], 1024),
		batchPeriod: requeueBatchPeriodIntegrationTests,
	}
	options = append(options, func(m *Manager) {
		m.inadmissibleWorkloadRequeuer = requeuer
	})
	m := NewManager(client, checker, options...)
	requeuer.qManager = m
	return m
}
