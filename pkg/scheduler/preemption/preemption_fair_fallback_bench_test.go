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

package preemption

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/log"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// fsFallbackFixture is the scheduling state of one fallback scenario, ready for a preemption search.
type fsFallbackFixture struct {
	clusterQueues []*kueue.ClusterQueue
	cohorts       []*kueue.Cohort
	admitted      []kueue.Workload
	incoming      *kueue.Workload
	strategies    []config.PreemptionStrategy
}

// fsFallbackEnv is a preemption search over a fallback fixture, repeatable as the search restores the snapshot.
type fsFallbackEnv struct {
	preemptor   *Preemptor
	snapshot    *schdcache.Snapshot
	preemptorWl *workload.Info
	assignment  flavorassigner.Assignment
}

func newFsFallbackEnv(ctx context.Context, tb testing.TB, now time.Time, fixture fsFallbackFixture) *fsFallbackEnv {
	tb.Helper()
	log := log.FromContext(ctx)
	admitted := make([]kueue.Workload, len(fixture.admitted))
	copy(admitted, fixture.admitted)
	for i := range admitted {
		// Set the name as the UID so that the candidates ordering is predictable.
		admitted[i].UID = types.UID(admitted[i].Name)
	}
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: admitted}).Build()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for _, cq := range fixture.clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			tb.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	for _, cohort := range fixture.cohorts {
		if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
			tb.Fatalf("Couldn't add Cohort to cache: %v", err)
		}
	}
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		tb.Fatalf("unexpected error while building snapshot: %v", err)
	}
	preemptorWl := workload.NewInfo(log, fixture.incoming)
	preemptorWl.ClusterQueue = fsFallbackPreemptorCQ
	return &fsFallbackEnv{
		preemptor: New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, &config.FairSharing{
			PreemptionStrategies: fixture.strategies,
		}, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil),
		snapshot:    snapshot,
		preemptorWl: preemptorWl,
		assignment: singlePodSetAssignment(flavorassigner.ResourceAssignment{
			corev1.ResourceCPU: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
		}),
	}
}

// search runs the preemption search and returns the targets it found.
func (e *fsFallbackEnv) search(ctx context.Context) []*Target {
	strategies := e.preemptor.GetPreemptionStrategyIterator(ctx, *e.preemptorWl, e.snapshot, e.assignment)
	return e.preemptor.GetTargetsWithStrategy(ctx, strategies)
}

// BenchmarkFairPreemptionWithinClusterQueueFallback measures the fallback's cost on a search whose fair sharing targets are rejected.
func BenchmarkFairPreemptionWithinClusterQueueFallback(b *testing.B) {
	now := time.Now()
	reproCQs, reproCohorts := fsFallbackTree("24", "0", false, kueue.PreemptionPolicyLowerPriority)
	boundCQs, boundCohorts := fsFallbackTree("15", "0", false, kueue.PreemptionPolicyLowerPriority)

	benchmarks := map[string]struct {
		fixture     fsFallbackFixture
		fallback    bool
		wantTargets int
	}{
		"gate=off": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
				strategies:    fsRejectedPlanStrategies,
			},
			wantTargets: 0,
		},
		"gate=on": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
				strategies:    fsRejectedPlanStrategies,
			},
			fallback:    true,
			wantTargets: 1,
		},
		"gate=on bound fails": {
			fixture: fsFallbackFixture{
				clusterQueues: boundCQs,
				cohorts:       boundCohorts,
				admitted: fsFallbackAdmitted(now, []fsFallbackWl{
					{name: "p_low", cq: "p", cpu: "1", prio: 1},
					{name: "p_mid", cq: "p", cpu: "2", prio: 2},
					{name: "s1", cq: "s", cpu: "3", prio: 0},
					{name: "x1", cq: "x", cpu: "4", prio: 0},
					{name: "x2", cq: "x", cpu: "5", prio: 1},
				}),
				incoming:   fsFallbackIncoming("4"),
				strategies: fsRejectedPlanStrategies,
			},
			fallback:    true,
			wantTargets: 0,
		},
	}
	for name, bench := range benchmarks {
		b.Run(name, func(b *testing.B) {
			features.SetFeatureGateDuringTest(b, features.FairSharingPreemptWithinClusterQueueFallback, bench.fallback)
			ctx := b.Context()
			// Setup runs outside the timer, and the search restores the snapshot, so the environment is reusable.
			env := newFsFallbackEnv(ctx, b, now, bench.fixture)
			if targets := env.search(ctx); len(targets) != bench.wantTargets {
				b.Fatalf("Setup search found %d targets, want %d", len(targets), bench.wantTargets)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				env.search(ctx)
			}
		})
	}
}
