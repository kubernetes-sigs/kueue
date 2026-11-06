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

package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestRequeueAndUpdateRetryAt(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		cohortName             kueue.CohortReference
		requeueReason          qcache.RequeueReason
		retryAt                *time.Time
		beforeRequeue          func(context.Context, client.Client, *kueue.Workload) error
		wantMoved              int
		wantClusterQueueDelays map[kueue.ClusterQueueReference]time.Duration
		wantCohortDelays       map[kueue.CohortReference]time.Duration
	}{
		"PreemptionNoCandidates with retryAt on standalone ClusterQueue arms ClusterQueue delay": {
			requeueReason:          qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:                new(now.Add(10 * time.Second)),
			wantMoved:              1,
			wantClusterQueueDelays: map[kueue.ClusterQueueReference]time.Duration{"cq": 10 * time.Second},
		},
		"PreemptionNoCandidates with retryAt on cohort ClusterQueue arms root Cohort delay": {
			cohortName:       "cohort",
			requeueReason:    qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:          new(now.Add(15 * time.Second)),
			wantMoved:        1,
			wantCohortDelays: map[kueue.CohortReference]time.Duration{"cohort": 15 * time.Second},
		},
		"PreemptionNoCandidates with non-future retryAt still notifies requeuer": {
			requeueReason: qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:       new(now.Add(-time.Second)),
			wantMoved:     1,
		},
		"PreemptionNoCandidates without retryAt arms no delay": {
			requeueReason: qcache.RequeueReasonPreemptionNoCandidates,
		},
		"NoFit with retryAt arms ClusterQueue delay": {
			requeueReason:          qcache.RequeueReasonNoFit,
			retryAt:                new(now.Add(10 * time.Second)),
			wantMoved:              1,
			wantClusterQueueDelays: map[kueue.ClusterQueueReference]time.Duration{"cq": 10 * time.Second},
		},
		"PendingPreemption with retryAt arms no delay": {
			requeueReason: qcache.RequeueReasonPendingPreemption,
			retryAt:       new(now.Add(10 * time.Second)),
		},
		"deleted workload with retryAt arms no delay": {
			requeueReason: qcache.RequeueReasonPreemptionNoCandidates,
			retryAt:       new(now.Add(10 * time.Second)),
			beforeRequeue: func(ctx context.Context, cl client.Client, wl *kueue.Workload) error {
				return cl.Delete(ctx, wl)
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			wl := utiltestingapi.MakeWorkload("wl", "ns").Queue("lq").Request(corev1.ResourceCPU, "1").Obj()
			cq := utiltestingapi.MakeClusterQueue("cq").Cohort(tc.cohortName).Obj()
			lq := utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()

			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: []kueue.Workload{*wl}}, &kueue.LocalQueueList{Items: []kueue.LocalQueue{*lq}}).
				WithObjects(utiltesting.MakeNamespace("ns"), cq).
				WithStatusSubresource(&kueue.Workload{}).
				Build()
			cqCache := schdcache.New(cl)
			if tc.cohortName != "" {
				if err := cqCache.AddOrUpdateCohort(utiltestingapi.MakeCohort(tc.cohortName).Obj()); err != nil {
					t.Fatalf("adding Cohort to cache: %v", err)
				}
			}
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to cache: %v", err)
			}
			preemptionExpectations := preemptexpectations.New()
			qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, cqCache, qcache.WithPreemptionExpectations(preemptionExpectations))
			if tc.cohortName != "" {
				qManager.AddOrUpdateCohort(ctx, utiltestingapi.MakeCohort(tc.cohortName).Obj())
			}
			if err := qManager.AddLocalQueue(ctx, lq); err != nil {
				t.Fatalf("adding LocalQueue to manager: %v", err)
			}
			if err := qManager.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to manager: %v", err)
			}
			requeuer.ProcessRequeues(ctx)

			heads := qManager.Heads(ctx)
			if len(heads) != 1 {
				t.Fatalf("Heads() returned %d workloads, want 1", len(heads))
			}

			scheduler := New(
				qManager, cqCache, cl, &utiltesting.EventRecorder{},
				WithClock(t, testingclock.NewFakeClock(now)),
				WithPreemptionExpectations(preemptionExpectations),
			)

			e := entry{
				Head:          heads[0],
				status:        nominated,
				requeueReason: tc.requeueReason,
			}
			e.recordAssignment(flavorassigner.Assignment{}, nil, tc.retryAt)
			if tc.beforeRequeue != nil {
				if err := tc.beforeRequeue(ctx, cl, wl); err != nil {
					t.Fatalf("beforeRequeue(): %v", err)
				}
			}
			scheduler.requeueAndUpdate(ctx, e)

			if diff := cmp.Diff(tc.wantClusterQueueDelays, requeuer.ClusterQueueDelays(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ClusterQueueDelays() (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCohortDelays, requeuer.CohortDelays(), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("CohortDelays() (-want,+got):\n%s", diff)
			}
			if got := requeuer.ProcessRequeues(ctx); got != tc.wantMoved {
				t.Errorf("ProcessRequeues() = %d, want %d", got, tc.wantMoved)
			}
		})
	}
}
