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
	"testing"
	"time"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

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
				strategies:    fsFallbackStrategies,
			},
			wantTargets: 0,
		},
		"gate=on": {
			fixture: fsFallbackFixture{
				clusterQueues: reproCQs,
				cohorts:       reproCohorts,
				admitted:      fsFallbackAdmitted(now, fsFallbackReproWls),
				incoming:      fsFallbackIncoming("4"),
				strategies:    fsFallbackStrategies,
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
				strategies: fsFallbackStrategies,
			},
			fallback:    true,
			wantTargets: 0,
		},
	}
	for name, bench := range benchmarks {
		b.Run(name, func(b *testing.B) {
			features.SetFeatureGateDuringTest(b, features.FairSharingWithinClusterQueueFallback, bench.fallback)
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
