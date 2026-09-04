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
	"fmt"
	"testing"

	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/kueue/pkg/scheduler/simulation"
)

// byteCounter is a zapcore.WriteSyncer that counts bytes and discards them.
type byteCounter struct{ n int64 }

func (c *byteCounter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
func (c *byteCounter) Sync() error                 { return nil }

// BenchmarkRunFirstFsStrategy evaluates every workload of one borrowing
// ClusterQueue with an always-failing strategy and reports the log volume it
// emits (logB/op) through zap's JSON encoder. v2 disables the log (baseline),
// v4 enables it.
func BenchmarkRunFirstFsStrategy(b *testing.B) {
	for _, candidates := range []int{10, 100} {
		for _, v := range []int{2, 4} {
			b.Run(fmt.Sprintf("candidatesPerCQ=%d/v%d", candidates, v), func(b *testing.B) {
				sink := &byteCounter{}
				log := zapr.NewLogger(zap.New(zapcore.NewCore(
					zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), sink, vLevel(v))))
				fixture := newFsLogFixture(b, log, []fsLogClusterQueue{{name: "b", candidates: candidates}})

				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					if err := simulation.Simulate(fixture.preemptionCtx.ctx, fixture.snapshot, func(simCtx *simulation.SimulationContext) error {
						_, _, _, inErr := runFirstFsStrategy(simCtx, fixture.preemptionCtx, fixture.candidates, alwaysFails)
						return inErr
					}); err != nil {
						b.Errorf("Unexpected error: %v", err)
					}
				}
				b.ReportMetric(float64(sink.n)/float64(b.N), "logB/op")
			})
		}
	}
}
