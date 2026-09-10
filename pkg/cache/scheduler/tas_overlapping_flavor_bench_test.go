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
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// countingSink is a logr.LogSink that counts only the "skip accounting for TAS
// usage in domain" lines emitted at or below enabledV, so a benchmark can
// measure skip-log volume without a logging backend. Other V(3) records (e.g.
// "Constructing TAS snapshot") are ignored.
type countingSink struct {
	enabledV int
	lines    int64
}

func (s *countingSink) Init(logr.RuntimeInfo)  {}
func (s *countingSink) Enabled(level int) bool { return level <= s.enabledV }
func (s *countingSink) Info(_ int, msg string, _ ...any) {
	if msg == "skip accounting for TAS usage in domain" {
		s.lines++
	}
}
func (s *countingSink) Error(error, string, ...any)    {}
func (s *countingSink) WithValues(...any) logr.LogSink { return s }
func (s *countingSink) WithName(string) logr.LogSink   { return s }

// BenchmarkTASFlavorSnapshotOverlappingUsage models N disjoint flavors sharing a
// hostname topology: each flavor's snapshot receives the merged usage of all N,
// so held*(flavors-1) domains are foreign to it. logLines/op is the V(3) skip
// lines produced; v2 disables them, v3 enables them.
func BenchmarkTASFlavorSnapshotOverlappingUsage(b *testing.B) {
	features.SetFeatureGateDuringTest(b, features.TASHandleOverlappingFlavors, true)
	levels := []string{benchBlockLabel, benchRackLabel, benchHostLabel}
	usage := resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000})

	for _, tc := range []struct{ held, flavors int }{
		{held: 100, flavors: 10},
		{held: 250, flavors: 10},
	} {
		for _, v := range []int{2, 3} {
			b.Run(fmt.Sprintf("held=%d/flavors=%d/v%d", tc.held, tc.flavors, v), func(b *testing.B) {
				nodes := buildBenchNodes(benchTopology{nodes: tc.held, nodesPerRack: 16, racksPerBlock: 16})
				tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
				for i := range nodes {
					tasCache.SyncNode(&nodes[i])
				}
				flavorCache := tasCache.NewTASFlavorCache(
					topologyInformation{Levels: levels},
					flavorInformation{TopologyName: "default"},
				)

				// Held domains come from the flavor's own leaves; the rest are foreign.
				_, log := utiltesting.ContextWithLog(b)
				held, err := flavorCache.snapshot(b.Context(), log, nil, nil)
				if err != nil {
					b.Fatalf("initial TASFlavorSnapshot creation failed: %v", err)
				}
				merged := make(map[utiltas.TopologyDomainID]resources.Requests, tc.held*tc.flavors)
				for domainID := range held.leaves {
					merged[domainID] = usage
				}
				for i := range tc.held * (tc.flavors - 1) {
					merged[utiltas.TopologyDomainID(fmt.Sprintf("other-flavor-node-%d", i))] = usage
				}

				sink := &countingSink{enabledV: v}
				log = logr.New(sink)
				b.ReportAllocs()
				iters := 0
				for b.Loop() {
					if _, err := flavorCache.snapshot(b.Context(), log, nil, merged); err != nil {
						b.Fatalf("TASFlavorSnapshot creation failed: %v", err)
					}
					iters++
				}
				b.ReportMetric(float64(sink.lines)/float64(iters), "logLines/op")
			})
		}
	}
}
