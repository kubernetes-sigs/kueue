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
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// The drain benchmark measures the cross-cycle effect of refill: starting
// from a populated queue state, run scheduling cycles until every workload is
// admitted, and report how many cycles the drain took. Single-cycle
// benchmarks structurally cannot see refill's benefit (the mechanism moves
// admissions between cycles); this one can. Every fixture's total capacity
// equals its total demand, so each drain admits every workload in both arms
// and the arms are comparable via cycles/op and ns/admit -- sec/op alone is
// not comparable because the arms do the same total admissions in a
// different number of cycles.

// drainScenario is a self-contained queue state to drain.
type drainScenario struct {
	name          string
	clusterQueues []kueue.ClusterQueue
	localQueues   []kueue.LocalQueue
	workloads     []kueue.Workload
}

// refillDrainScenarios covers the two shapes requested on the kueue#13496
// thread:
//   - backlog: one ClusterQueue with a long backlog borrowing beyond its
//     nominal quota, next to steady queues with short backlogs (the
//     "empty CQ with long backlog" scenario, the kueue#9345 shape drained to
//     completion);
//   - balanced: identical ClusterQueues with equal backlogs, all within
//     nominal quota (DRS stays 0, ordering falls through to FIFO).
//
// Every workload requests 1 CPU and every scenario's cohort capacity equals
// its total demand, so nothing ever becomes inadmissible and the drain
// terminates with all workloads admitted.
func refillDrainScenarios() []*drainScenario {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	seq := 0
	// Unique, strictly increasing creation timestamps, spaced by SECONDS:
	// metav1.Time is only reliable at second granularity once objects round
	// trip, so sub-second spacing collapses into FIFO ties that get broken by
	// map iteration order and ruin determinism.
	nextCreation := func() time.Time {
		seq++
		return base.Add(time.Duration(seq) * time.Second)
	}
	pending := func(name, lq string) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "default").
			Queue(kueue.LocalQueueName(lq)).
			Creation(nextCreation()).
			PodSets(*utiltestingapi.MakePodSet("one", 1).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
	}
	cq := func(name, cohort, nominal, borrow string) kueue.ClusterQueue {
		return *utiltestingapi.MakeClusterQueue(name).
			Cohort(kueue.CohortReference(cohort)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, nominal, borrow).Obj()).
			Obj()
	}
	lq := func(cqName string) kueue.LocalQueue {
		return *utiltestingapi.MakeLocalQueue(cqName+"-lq", "default").
			ClusterQueue(cqName).Obj()
	}

	backlog := &drainScenario{name: "backlog"}
	// One backlogged queue: nominal 4, borrows up to 28, 32 pending.
	backlog.clusterQueues = append(backlog.clusterQueues, cq("drain-backlog", "drain", "4", "28"))
	backlog.localQueues = append(backlog.localQueues, lq("drain-backlog"))
	for i := range 32 {
		backlog.workloads = append(backlog.workloads, pending(fmt.Sprintf("backlog-%02d", i), "drain-backlog-lq"))
	}
	// Seven steady queues: nominal 6 each, 2 pending each. Capacity
	// 4+7*6=46 equals demand 32+7*2=46.
	for q := range 7 {
		name := fmt.Sprintf("drain-steady-%d", q)
		backlog.clusterQueues = append(backlog.clusterQueues, cq(name, "drain", "6", "0"))
		backlog.localQueues = append(backlog.localQueues, lq(name))
		for i := range 2 {
			backlog.workloads = append(backlog.workloads, pending(fmt.Sprintf("steady-%d-%d", q, i), name+"-lq"))
		}
	}

	balanced := &drainScenario{name: "balanced"}
	// Eight identical queues: nominal 8 each, 8 pending each. Capacity
	// 8*8=64 equals demand.
	for q := range 8 {
		name := fmt.Sprintf("drain-bal-%d", q)
		balanced.clusterQueues = append(balanced.clusterQueues, cq(name, "drain", "8", "0"))
		balanced.localQueues = append(balanced.localQueues, lq(name))
		for i := range 8 {
			balanced.workloads = append(balanced.workloads, pending(fmt.Sprintf("bal-%d-%d", q, i), name+"-lq"))
		}
	}

	return []*drainScenario{backlog, balanced}
}

// setupDrain builds a fresh client, cache, queue manager and scheduler for
// one drain. The context carries a discarded logger: log volume correlates
// with the arm under test (refill logs per extra pop), so a live test logger
// would bias the measurement.
func setupDrain(tb testing.TB, sc *drainScenario) (context.Context, *qcache.Manager, *Scheduler, *sync.WaitGroup) {
	tb.Helper()
	log := logr.Discard()
	ctx := logr.NewContext(tb.Context(), log)
	cl := utiltesting.NewClientBuilder().
		WithObjects(utiltesting.MakeNamespaceWrapper("default").Obj()).
		WithLists(
			&kueue.WorkloadList{Items: sc.workloads},
			&kueue.LocalQueueList{Items: sc.localQueues},
			&kueue.ClusterQueueList{Items: sc.clusterQueues},
		).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
				return nil // discard status updates; queue and cache state drive the drain
			},
		}).
		Build()
	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache)
	cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	for i := range sc.localQueues {
		if err := qManager.AddLocalQueue(ctx, sc.localQueues[i].DeepCopy()); err != nil {
			tb.Fatalf("Failed adding LocalQueue %s: %v", sc.localQueues[i].Name, err)
		}
	}
	for i := range sc.clusterQueues {
		cqCopy := sc.clusterQueues[i].DeepCopy()
		if err := cqCache.AddClusterQueue(ctx, cqCopy); err != nil {
			tb.Fatalf("Failed adding ClusterQueue %s to cache: %v", cqCopy.Name, err)
		}
		if err := qManager.AddClusterQueue(ctx, cqCopy); err != nil {
			tb.Fatalf("Failed adding ClusterQueue %s to manager: %v", cqCopy.Name, err)
		}
	}
	scheduler := New(qManager, cqCache, cl, recorder,
		WithFairSharing(&config.FairSharing{}),
		WithPreemptionExpectations(preemptexpectations.New()))
	wg := &sync.WaitGroup{}
	scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() { wg.Add(1) },
		func() { wg.Done() },
	))
	return ctx, qManager, scheduler, wg
}

// drainToEmpty runs scheduling cycles until every ClusterQueue heap is empty
// and returns the number of cycles. The emptiness and convergence checks run
// with the benchmark timer stopped: the baseline arm runs many more cycles --
// and therefore many more checks -- than the refill arm, so leaving the
// harness's own bookkeeping in the timed region would bias the comparison.
// afterCycle, when non-nil, runs after every cycle (used by the fixture guard
// test; pass nil when benchmarking). Fails the test if the queues do not
// converge or if any workload ends up inadmissible.
func drainToEmpty(ctx context.Context, tb testing.TB, qManager *qcache.Manager, scheduler *Scheduler, wg *sync.WaitGroup, maxCycles int, afterCycle func(cycle int)) int {
	tb.Helper()
	b, isBench := tb.(*testing.B)
	cycles := 0
	for {
		if isBench {
			b.StopTimer()
		}
		drained := qManager.Dump() == nil
		if drained {
			if inadmissible := qManager.DumpInadmissible(); inadmissible != nil {
				tb.Fatalf("workloads left inadmissible after drain: %v", inadmissible)
			}
		} else if cycles >= maxCycles {
			tb.Fatalf("queues did not drain within %d cycles; left: %v, inadmissible: %v",
				maxCycles, qManager.Dump(), qManager.DumpInadmissible())
		}
		if isBench {
			b.StartTimer()
		}
		if drained {
			return cycles
		}
		scheduler.schedule(ctx)
		wg.Wait()
		cycles++
		if afterCycle != nil {
			afterCycle(cycles)
		}
	}
}

// Measurement protocol: for statistically meaningful comparisons, run the
// whole benchmark in N separate `go test` invocations (one sample per arm per
// invocation) and compare with benchstat. A single invocation with -count=N
// runs each arm's N samples back-to-back, which lets thermal drift correlate
// with the arm order.
func BenchmarkSchedulerFairSharingRefillDrain(b *testing.B) {
	for _, sc := range refillDrainScenarios() {
		for _, arm := range []struct {
			name   string
			gateOn bool
		}{
			{name: "off", gateOn: false},
			{name: "on", gateOn: true},
		} {
			b.Run(fmt.Sprintf("scenario=%s/gate=%s", sc.name, arm.name), func(b *testing.B) {
				features.SetFeatureGateDuringTest(b, features.FairSharingRefill, arm.gateOn)
				totalWorkloads := len(sc.workloads)
				totalCycles := 0
				iterations := 0
				for b.Loop() {
					b.StopTimer()
					ctx, qManager, scheduler, wg := setupDrain(b, sc)
					runtime.GC()
					b.StartTimer()
					totalCycles += drainToEmpty(ctx, b, qManager, scheduler, wg, 2*totalWorkloads+8, nil)
					iterations++
				}
				b.ReportMetric(float64(totalCycles)/float64(iterations), "cycles/op")
				b.ReportMetric(float64(totalWorkloads*iterations)/float64(totalCycles), "admits/cycle")
				b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(totalWorkloads*iterations), "ns/admit")
			})
		}
	}
}

// TestRefillDrainScenarios pins the benchmark fixtures' invariants so they
// cannot silently stop exercising the feature: every scenario drains fully
// in both arms, the gate-on arm drains in strictly fewer cycles (refill
// actually engages), some ClusterQueue drains deep enough in one cycle to
// prove a refill chain of at least two, and the backlog scenario's fixture
// forces borrowing so the drain leaves the all-DRS-zero regime.
func TestRefillDrainScenarios(t *testing.T) {
	for _, sc := range refillDrainScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			if sc.name == "backlog" {
				assertScenarioForcesBorrowing(t, sc)
			}
			cycles := map[bool]int{}
			maxDrainedPerCQPerCycle := 0
			for _, gateOn := range []bool{false, true} {
				features.SetFeatureGateDuringTest(t, features.FairSharingRefill, gateOn)
				ctx, qManager, scheduler, wg := setupDrain(t, sc)
				prev := qManager.Dump()
				afterCycle := func(int) {
					cur := qManager.Dump()
					if gateOn {
						for cq, refs := range prev {
							if drained := len(refs) - len(cur[cq]); drained > maxDrainedPerCQPerCycle {
								maxDrainedPerCQPerCycle = drained
							}
						}
					}
					prev = cur
				}
				cycles[gateOn] = drainToEmpty(ctx, t, qManager, scheduler, wg, 2*len(sc.workloads)+8, afterCycle)
			}
			if cycles[true] >= cycles[false] {
				t.Errorf("refill did not reduce cycles to drain: gate on %d, gate off %d", cycles[true], cycles[false])
			}
			// Head + at least two refills from one ClusterQueue in one cycle.
			if maxDrainedPerCQPerCycle < 3 {
				t.Errorf("no refill chain of depth >= 2 observed: max per-CQ per-cycle drain %d", maxDrainedPerCQPerCycle)
			}
			t.Logf("cycles to drain %d workloads: gate off %d, gate on %d (max per-CQ per-cycle drain %d)",
				len(sc.workloads), cycles[false], cycles[true], maxDrainedPerCQPerCycle)
		})
	}
}

// assertScenarioForcesBorrowing fails unless at least one ClusterQueue has
// more pending workloads (1 CPU each) than nominal quota, which guarantees
// the drain admits beyond nominal and produces a nonzero DRS at some point.
// Guards against a future quota edit silently degrading the fixture into a
// pure-FIFO benchmark.
func assertScenarioForcesBorrowing(t *testing.T, sc *drainScenario) {
	t.Helper()
	lqToCQ := make(map[string]kueue.ClusterQueueReference, len(sc.localQueues))
	for i := range sc.localQueues {
		lqToCQ[sc.localQueues[i].Name] = sc.localQueues[i].Spec.ClusterQueue
	}
	pendingPerCQ := make(map[kueue.ClusterQueueReference]int64)
	for i := range sc.workloads {
		pendingPerCQ[lqToCQ[string(sc.workloads[i].Spec.QueueName)]]++
	}
	for i := range sc.clusterQueues {
		cq := &sc.clusterQueues[i]
		nominal := cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota
		if pendingPerCQ[kueue.ClusterQueueReference(cq.Name)] > nominal.Value() {
			return
		}
	}
	t.Fatalf("no ClusterQueue in scenario %q has more pending workloads than nominal quota; the drain would never borrow", sc.name)
}
