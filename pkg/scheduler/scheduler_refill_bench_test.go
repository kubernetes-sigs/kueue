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
	"math"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	"sigs.k8s.io/kueue/pkg/workload"
)

// The drain benchmark measures the cross-cycle effect of refill: from a
// populated queue state, run scheduling cycles until every workload is
// admitted, and report how long that took. A single-cycle benchmark cannot see
// refill move admissions between cycles.
//
// ns/op is the drain's wall clock and is the headline: every arm of a scenario
// admits the same workloads, and the harness stops the timer for its own
// bookkeeping. cycles/op counts the snapshots a drain built, not time -- a
// cycle that admits anything returns wait.KeepGoing, so cycles run back to
// back. admits/cycle is cycles/op rescaled by a per-scenario constant; it names
// the mechanism and adds no independent evidence.
//
// Refill's cost is reported beside its benefit. A cycle works off one snapshot,
// so a longer cycle acts on a staler one: the per-cycle quantiles bound that
// staleness across every cycle of every measured drain, and each arm logs the
// trace of its worst drain. Two fixtures inject a workload after the drain has
// started and report how long the scheduler takes to reach it, and one reports
// the pops refill makes and then throws away.
//
// Two limits on what these numbers can settle:
//   - max-cycle-ms is an extreme-value statistic over a sample count that
//     itself differs by arm (cycles/op x iterations), so it does not converge
//     and cannot be compared between arms. p50 and p95 are the comparable
//     numbers; max is context.
//   - The arms of a scenario run back to back in one process, and the arm that
//     runs second measures 4-5% faster here. Differences smaller than that --
//     gate=off against gate=on/budget=0, for instance -- need one arm per
//     `go test` process before they mean anything.

// drainScenario is a self-contained queue state to drain, together with the
// events that happen while it drains and what the fixture is built to show.
type drainScenario struct {
	name string
	// flavors the ClusterQueues draw from. Empty means the single "default"
	// flavor; a fixture with more makes every nomination walk the flavor
	// fungibility scan, which is where refill's per-pop cost lives.
	flavors       []kueue.ResourceFlavor
	clusterQueues []kueue.ClusterQueue
	localQueues   []kueue.LocalQueue
	workloads     []kueue.Workload

	// occupants hold quota when the drain starts. They are accounted only in
	// the scheduler cache, never queued, so the queue layer never sees them.
	occupants []kueue.Workload
	// releasePerCycle occupants are released before every cycle, modelling
	// capacity freed by finishing jobs. Releasing mid-cycle instead would be
	// invisible to the cycle's frozen snapshot.
	releasePerCycle int

	// arrival is queued once arriveAfterAdmissions admissions have happened.
	// Its ClusterQueue decides which of the two late-arrival cases the fixture
	// covers: one that admits again later in the same cycle, or one that does
	// not and therefore cannot be reached before the next cycle's heads.
	arrival               *kueue.Workload
	arrivalCQ             kueue.ClusterQueueReference
	arriveAfterAdmissions int
	// refillReachesArrival is the expectation the two late-arrival fixtures
	// differ in: whether refill reaches the newcomer inside the cycle it
	// arrives in.
	refillReachesArrival bool

	// refillSavesCycles states whether refill is expected to shorten this
	// drain. A fixture where it does not is a result, not a broken fixture, so
	// the guard test asserts whichever this says.
	refillSavesCycles bool
	// wantChainDepth is the deepest single-cycle drain of one ClusterQueue the
	// fixture must produce: 3 is a head plus a chain of two refills, 2 is a
	// head plus one refill.
	wantChainDepth int
	// wantFullStops states that refill must find the cohort full and stop
	// before popping, the pop the capacity check exists to save.
	wantFullStops bool
	// wantBorrowers is how many ClusterQueues finish the drain using more than
	// their nominal quota. A ClusterQueue that does borrowed while draining,
	// which is the condition for a non-zero dominant resource share, so this
	// says how much of the fair-sharing tournament the fixture exercises: 0
	// means every share stayed 0 and the ordering fell through to creation
	// time.
	wantBorrowers int
}

func (sc *drainScenario) admissions() int {
	if sc.arrival != nil {
		return len(sc.workloads) + 1
	}
	return len(sc.workloads)
}

// refillDrainScenarios covers the shapes requested on the kueue#13496 and
// kueue#13729 threads plus the shapes where refill is expected to lose. Every
// workload requests 1 CPU, and every scenario ends with all its queued
// workloads admitted and nothing left inadmissible.
func refillDrainScenarios() []*drainScenario {
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	seq := 0
	// Unique, strictly increasing creation timestamps, spaced by SECONDS. Equal
	// timestamps are broken by UID (cluster_queue.go baseCompareFunc), and
	// MakeWorkload leaves the UID empty, so ties fall through to heap insertion
	// order -- which is seeded by ranging a map and would ruin determinism.
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
	occupant := func(name, cqName, flavor string) kueue.Workload {
		now := nextCreation()
		return *utiltestingapi.MakeWorkload(name, "default").
			Creation(now).
			PodSets(*utiltestingapi.MakePodSet("one", 1).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cqName)).PodSets(
				utiltestingapi.MakePodSetAssignment("one").
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(flavor), "1").Count(1).Obj(),
			).Obj(), now).
			AdmittedAt(true, now).
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

	backlog := &drainScenario{name: "backlog", refillSavesCycles: true, wantChainDepth: 3, wantBorrowers: 1}
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

	// Identical queues with equal backlogs, all within nominal quota. Nothing
	// borrows, so every DRS stays 0 and the tournament falls through to
	// creation time: this pair measures refill under plain FIFO ordering.
	//
	// Every queue empties its own head once per cycle whatever refill does, so
	// refill can only shorten the drain by finishing a queue early with the
	// budget it shares between them all. The benefit is therefore a function of
	// budget per ClusterQueue -- one unit per queue per cycle at 8 queues, a
	// quarter of one at 32 -- and the wide fixture reports what that buys.
	identicalQueues := func(name string, queues int, saves bool, depth int) *drainScenario {
		sc := &drainScenario{name: name, refillSavesCycles: saves, wantChainDepth: depth}
		for q := range queues {
			cqName := fmt.Sprintf("%s-%d", name, q)
			sc.clusterQueues = append(sc.clusterQueues, cq(cqName, "drain", "8", "0"))
			sc.localQueues = append(sc.localQueues, lq(cqName))
			for i := range 8 {
				sc.workloads = append(sc.workloads, pending(fmt.Sprintf("%s-%d-%d", name, q, i), cqName+"-lq"))
			}
		}
		return sc
	}
	balanced := identicalQueues("balanced", 8, true, 3)
	balancedWide := identicalQueues("balanced-wide", 32, false, 3)

	// Both late-arrival fixtures share one shape: two queues with a short
	// backlog and one that starts empty. The queue the newcomer lands in gets
	// one extra unit of nominal quota, so the wait it measures is scheduling
	// delay and not a shortage of quota, and capacity still equals demand.
	lateArrival := func(name string, arrivalCQ kueue.ClusterQueueReference, refillReaches bool) *drainScenario {
		sc := &drainScenario{
			name:                  name,
			arrivalCQ:             arrivalCQ,
			arriveAfterAdmissions: 2,
			refillReachesArrival:  refillReaches,
			refillSavesCycles:     true,
			wantChainDepth:        3,
		}
		for _, q := range []struct {
			name    string
			nominal int
			pending int
		}{
			{"arrive-refilled", 3, 3},
			{"arrive-busy", 3, 3},
			{"arrive-idle", 0, 0},
		} {
			nominal := q.nominal
			if kueue.ClusterQueueReference(q.name) == arrivalCQ {
				nominal++
			}
			sc.clusterQueues = append(sc.clusterQueues, cq(q.name, "arrive", strconv.Itoa(nominal), "0"))
			sc.localQueues = append(sc.localQueues, lq(q.name))
			for i := range q.pending {
				sc.workloads = append(sc.workloads, pending(fmt.Sprintf("%s-%d", q.name, i), q.name+"-lq"))
			}
		}
		newcomer := pending("newcomer", string(arrivalCQ)+"-lq")
		sc.arrival = &newcomer
		return sc
	}
	// The newcomer lands in a ClusterQueue that keeps admitting for the rest
	// of the cycle, so a refill pop can reach it without waiting for the next
	// cycle's heads.
	arrivalRefilled := lateArrival("arrival-refilled-cq", "arrive-refilled", true)
	// The newcomer lands in a ClusterQueue that admits nothing this cycle.
	// Refill only pops from a queue that just admitted, so nothing reaches it
	// before the next cycle -- it waits out the rest of the cycle it arrived
	// in, which refill has made longer.
	arrivalIdle := lateArrival("arrival-idle-cq", "arrive-idle", false)

	// One worker queue with a backlog next to residents that start full, so it
	// admits only what they release. releasePerCycle sets how much capacity a
	// cycle finds waiting, and therefore how much budget can be spent before a
	// pop runs out of quota: freed-capacity leaves room for 4, contended for 1,
	// so every contended cycle finds the cohort full and stops before popping.
	// Capacity -- the worker's nominal 1 plus the residents' 12 -- equals its 13
	// queued workloads only once the last occupant is gone.
	residentCohort := func(name string, releasePerCycle int, saves bool, depth int, full bool) *drainScenario {
		sc := &drainScenario{
			name:              name,
			releasePerCycle:   releasePerCycle,
			refillSavesCycles: saves,
			wantChainDepth:    depth,
			wantFullStops:     full,
			wantBorrowers:     1,
		}
		worker := name + "-worker"
		sc.clusterQueues = append(sc.clusterQueues, cq(worker, name, "1", "12"))
		sc.localQueues = append(sc.localQueues, lq(worker))
		for i := range 13 {
			sc.workloads = append(sc.workloads, pending(fmt.Sprintf("%s-work-%02d", name, i), worker+"-lq"))
		}
		for q := range 3 {
			resident := fmt.Sprintf("%s-res-%d", name, q)
			sc.clusterQueues = append(sc.clusterQueues, cq(resident, name, "4", "0"))
			sc.localQueues = append(sc.localQueues, lq(resident))
			for i := range 4 {
				sc.occupants = append(sc.occupants, occupant(fmt.Sprintf("%s-occ-%d-%d", name, q, i), resident, "default"))
			}
		}
		return sc
	}
	freed := residentCohort("freed-capacity", 4, true, 3, false)
	contended := residentCohort("contended", 1, true, 2, true)

	// ClusterQueues that all have to borrow to drain, which is what makes a
	// dominant resource share non-zero: nominal quota is 1, 2, 3, 4, 1, ... so
	// no two neighbours hold the same share, and an idle ClusterQueue holds
	// exactly the quota they collectively borrow, so capacity still equals
	// demand. Every other fixture here has at most one borrowing ClusterQueue,
	// which leaves every share at 0 and the tournament ordering by creation
	// time; these are the only ones where the re-ranking refill performs on
	// every pop has to choose between two shares, which is the situation refill
	// exists for.
	contestedGroup := func(sc *drainScenario, cohort string, queues, depth int) {
		lent := 0
		for q := range queues {
			// Below depth so the queue cannot drain within its own quota.
			nominal := min(q%4+1, depth-1)
			lent += depth - nominal
			cqName := fmt.Sprintf("%s-%d", cohort, q)
			sc.clusterQueues = append(sc.clusterQueues,
				cq(cqName, cohort, strconv.Itoa(nominal), strconv.Itoa(depth)))
			sc.localQueues = append(sc.localQueues, lq(cqName))
			for i := range depth {
				sc.workloads = append(sc.workloads,
					pending(fmt.Sprintf("%s-%d-%02d", cohort, q, i), cqName+"-lq"))
			}
		}
		idle := cohort + "-idle"
		sc.clusterQueues = append(sc.clusterQueues, cq(idle, cohort, strconv.Itoa(lent), "0"))
		sc.localQueues = append(sc.localQueues, lq(idle))
	}
	contestedQueues := func(name string, queues, depth int, saves bool, chainDepth int) *drainScenario {
		sc := &drainScenario{
			name:              name,
			refillSavesCycles: saves,
			wantChainDepth:    chainDepth,
			wantBorrowers:     queues,
		}
		contestedGroup(sc, name, queues, depth)
		return sc
	}
	// Two borrowers and a budget larger than the number of ClusterQueues that
	// admit: both chains extend as far as their backlog allows, so this one
	// prices contested shares without the budget itself being contested.
	contestedShare := contestedQueues("contested-share", 2, 8, true, 3)
	// Twelve borrowers against a budget of 8: every cycle has more
	// ClusterQueues admitting than there are refill pops to go round, so the
	// budget has to be allocated between shares rather than merely spent. This
	// is the fixture the budget-exhaustion question needs, since a cycle here
	// always ends on BudgetExhausted with backlog still visible.
	contestedWide := contestedQueues("contested-wide", 12, 6, true, 2)
	// The same contested shape in two independent cohorts, sharing one global
	// budget. The tournament arbitrates inside a cohort; nothing arbitrates
	// between them, and getCq picks whose tournament runs next by ranging a
	// map, so the split of the budget across cohorts is not a guarantee the
	// scheduler makes. This fixture exists to report what that costs, which is
	// why the guard test runs it repeatedly and prints the spread rather than
	// pinning a share.
	twoCohorts := &drainScenario{
		name:              "two-cohorts",
		refillSavesCycles: true,
		wantChainDepth:    2,
		wantBorrowers:     8,
	}
	contestedGroup(twoCohorts, "two-cohorts-a", 4, 6)
	contestedGroup(twoCohorts, "two-cohorts-b", 4, 6)

	// The balanced shape drawn from eight flavors, only the last of which has
	// quota, so every nomination -- head or refill -- walks the whole flavor
	// fungibility scan instead of matching on the first try. Refill multiplies
	// the number of nominations a cycle performs, so this fixture is what says
	// whether that multiplier lands on something cheap or something expensive.
	const flavorCount = 8
	manyFlavors := identicalQueues("many-flavors", 8, true, 3)
	manyFlavors.clusterQueues = nil
	for f := range flavorCount {
		manyFlavors.flavors = append(manyFlavors.flavors,
			*utiltestingapi.MakeResourceFlavor(fmt.Sprintf("flavor-%d", f)).Obj())
	}
	for q := range 8 {
		cqName := fmt.Sprintf("many-flavors-%d", q)
		builder := utiltestingapi.MakeClusterQueue(cqName).Cohort("drain")
		quotas := make([]kueue.FlavorQuotas, 0, flavorCount)
		for f := range flavorCount {
			nominal := "0"
			if f == flavorCount-1 {
				nominal = "8"
			}
			quotas = append(quotas, *utiltestingapi.MakeFlavorQuotas(fmt.Sprintf("flavor-%d", f)).
				Resource(corev1.ResourceCPU, nominal, "0").Obj())
		}
		manyFlavors.clusterQueues = append(manyFlavors.clusterQueues, *builder.ResourceGroup(quotas...).Obj())
	}

	return []*drainScenario{
		backlog, balanced, balancedWide,
		arrivalRefilled, arrivalIdle,
		freed, contended, contestedShare, contestedWide, twoCohorts, manyFlavors,
	}
}

// drainHarness holds one drain's scheduler and the state it acts on.
type drainHarness struct {
	ctx       context.Context
	log       logr.Logger
	client    client.Client
	cache     *schdcache.Cache
	queues    *qcache.Manager
	scheduler *Scheduler
	wg        *sync.WaitGroup

	// held is the occupants still holding quota, in release order.
	held []workload.Reference
	// afterCycle, when set, observes the queue state at every cycle boundary.
	afterCycle func()
}

// cpuMeter accumulates process CPU time over the spans the benchmark timer is
// running, mirroring every StopTimer/StartTimer pair. Wall clock counts time
// the host spent on other processes; CPU time does not, so on a machine with
// bursty background load it is the more readable of the two. It can exceed the
// wall clock, since a cycle's admissions run on several goroutines.
type cpuMeter struct {
	running bool
	from    time.Duration
	total   time.Duration
}

func processCPU() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

func (m *cpuMeter) start() {
	if m != nil && !m.running {
		m.from, m.running = processCPU(), true
	}
}

func (m *cpuMeter) stop() {
	if m != nil && m.running {
		m.total, m.running = m.total+processCPU()-m.from, false
	}
}

// drainRun accumulates what one drain observed.
type drainRun struct {
	// bench is nil outside benchmarks; it keeps probes out of the timed region.
	bench *testing.B
	// cpu is nil outside benchmarks. It tracks the same spans as bench's timer.
	cpu *cpuMeter

	cycles     int
	cycleTimes []time.Duration
	admissions int

	arrived    bool
	pickedUp   bool
	waitAdmits int
	waitCycles int
	// arrivedAt and excludedAtArrival anchor waitWall, the wall clock the
	// arrival spent queued. It is observed at the probe that first saw the
	// scheduler take the newcomer, so it is an upper bound whose granularity
	// is one admission.
	arrivedAt         time.Time
	excludedAtArrival time.Duration
	waitWall          time.Duration

	// excluded is how long the harness has spent on work of its own that the
	// cycle timer must not charge to the scheduler: the arrival injection and
	// the wait probes run inside a cycle, and the convergence checks and
	// capacity releases run between cycles but inside the same pause/resume
	// pair.
	excluded     time.Duration
	excludedFrom time.Time
}

func (r *drainRun) pause() {
	r.excludedFrom = time.Now()
	if r.bench != nil {
		r.bench.StopTimer()
	}
	r.cpu.stop()
}

// resume restarts both timers before closing the excluded interval, so that
// StartTimer's own stop-the-world ReadMemStats is charged to the harness and
// not to the cycle it returns to. pause is ordered the same way.
func (r *drainRun) resume() {
	if r.bench != nil {
		r.bench.StartTimer()
	}
	r.cpu.start()
	r.excluded += time.Since(r.excludedFrom)
}

// stillWaiting reports whether the arrival is still on its ClusterQueue's heap,
// and latches the answer once it is not. A workload leaves the heap when the
// scheduler pops it, so the latch marks the point at which the scheduler first
// reached the newcomer, which is the wait being measured.
func (r *drainRun) stillWaiting(h *drainHarness, sc *drainScenario) bool {
	if !r.arrived || r.pickedUp {
		return false
	}
	// Read the clock before pausing, so this probe does not time itself.
	waited, excludedSoFar := time.Since(r.arrivedAt), r.excluded
	r.pause()
	defer r.resume()
	if slices.Contains(h.queues.Dump()[sc.arrivalCQ], workload.Key(sc.arrival)) {
		return true
	}
	r.pickedUp = true
	r.waitWall = waited - (excludedSoFar - r.excludedAtArrival)
	return false
}

// onAdmission runs synchronously inside the cycle, before the admission's own
// goroutine starts, so it can queue the arrival between two admissions of the
// same cycle.
func (r *drainRun) onAdmission(tb testing.TB, h *drainHarness, sc *drainScenario) {
	r.admissions++
	if sc.arrival != nil && !r.arrived && r.admissions >= sc.arriveAfterAdmissions {
		r.pause()
		h.queueArrival(tb, sc)
		r.resume()
		r.arrived, r.arrivedAt, r.excludedAtArrival = true, time.Now(), r.excluded
		return
	}
	if r.stillWaiting(h, sc) {
		r.waitAdmits++
	}
}

// queueArrival hands the arrival to the queue manager, the step a Workload
// informer event performs. The object has to reach the API server too: a
// requeue re-reads it and drops a workload that is not there
// (Manager.RequeueWorkload).
func (h *drainHarness) queueArrival(tb testing.TB, sc *drainScenario) {
	tb.Helper()
	wl := sc.arrival.DeepCopy()
	if err := h.client.Create(h.ctx, wl); err != nil {
		tb.Fatalf("Failed creating the late arrival %s: %v", wl.Name, err)
	}
	if err := h.queues.AddOrUpdateWorkload(h.log, wl); err != nil {
		tb.Fatalf("Failed queueing the late arrival %s: %v", wl.Name, err)
	}
}

func (h *drainHarness) releaseCapacity(tb testing.TB, n int) {
	tb.Helper()
	for range min(n, len(h.held)) {
		key := h.held[0]
		h.held = h.held[1:]
		if err := h.cache.DeleteWorkload(h.log, key); err != nil {
			tb.Fatalf("Failed releasing the occupant %s: %v", key, err)
		}
	}
}

// setupDrain builds a fresh client, cache, queue manager and scheduler for one
// drain. Benchmarks pass a discarded logger: log volume correlates with the arm
// under test, since refill logs per extra pop, so a live logger would bias the
// measurement.
func setupDrain(tb testing.TB, sc *drainScenario, budget int, log logr.Logger) (*drainHarness, *drainRun) {
	tb.Helper()
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
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithPreemptionExpectations(preemptexpectations.New()))
	flavors := sc.flavors
	if flavors == nil {
		flavors = []kueue.ResourceFlavor{*utiltestingapi.MakeResourceFlavor("default").Obj()}
	}
	for i := range flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flavors[i].DeepCopy())
	}
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
	h := &drainHarness{
		ctx:    ctx,
		log:    log,
		client: cl,
		cache:  cqCache,
		queues: qManager,
		wg:     &sync.WaitGroup{},
	}
	for i := range sc.occupants {
		if !cqCache.AddOrUpdateWorkload(log, sc.occupants[i].DeepCopy()) {
			tb.Fatalf("Failed accounting the occupant %s in the scheduler cache", sc.occupants[i].Name)
		}
		h.held = append(h.held, workload.Key(&sc.occupants[i]))
	}
	h.scheduler = New(qManager, cqCache, cl, recorder,
		WithFairSharing(&config.FairSharing{}),
		WithRefillBudget(budget),
		WithPreemptionExpectations(preemptexpectations.New()))
	run := &drainRun{}
	if b, ok := tb.(*testing.B); ok {
		run.bench = b
	}
	h.scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
		func() {
			h.wg.Add(1)
			run.onAdmission(tb, h, sc)
		},
		func() { h.wg.Done() },
	))
	return h, run
}

// drainToEmpty runs scheduling cycles until the queue layer is empty. The
// convergence checks run with the benchmark timer stopped: the baseline arm
// runs many more cycles -- and therefore many more checks -- than the refill
// arm, so leaving the harness's own bookkeeping in the timed region would bias
// the comparison. Fails the test unless the drain ends with every workload
// admitted and none stranded, since the reported metrics divide by the
// admissions the fixture expects rather than by the ones that happened.
func drainToEmpty(tb testing.TB, h *drainHarness, sc *drainScenario, run *drainRun, maxCycles int) {
	tb.Helper()
	for {
		run.pause()
		drained := h.queues.Dump() == nil
		if drained {
			if inadmissible := h.queues.DumpInadmissible(); inadmissible != nil {
				tb.Fatalf("workloads left inadmissible after drain: %v", inadmissible)
			}
			// Admitted workloads stay inflight: the claim Pop took is
			// released by requeue, delete, or ForgetInflight, and no
			// workload controller runs here to delete them. So every
			// workload must be inflight at the end -- one that is not
			// left the queue layer without being admitted.
			if inflight := countInflight(h.queues.DumpInflight()); inflight != sc.admissions() {
				tb.Fatalf("drain ended with %d workloads inflight, want %d", inflight, sc.admissions())
			}
			if run.admissions != sc.admissions() {
				tb.Fatalf("drain admitted %d workloads, want %d", run.admissions, sc.admissions())
			}
			if sc.arrival != nil && !run.arrived {
				tb.Fatalf("the drain ended in %d admissions, before the arrival was due after %d",
					run.admissions, sc.arriveAfterAdmissions)
			}
		} else if run.cycles >= maxCycles {
			tb.Fatalf("queues did not drain within %d cycles; left: %v, inadmissible: %v",
				maxCycles, h.queues.Dump(), h.queues.DumpInadmissible())
		}
		if !drained {
			h.releaseCapacity(tb, sc.releasePerCycle)
		}
		run.resume()
		if drained {
			return
		}
		start, excludedBefore := time.Now(), run.excluded
		h.scheduler.schedule(h.ctx)
		h.wg.Wait()
		run.cycleTimes = append(run.cycleTimes, time.Since(start)-(run.excluded-excludedBefore))
		run.cycles++
		if run.stillWaiting(h, sc) {
			run.waitCycles++
		}
		if h.afterCycle != nil {
			h.afterCycle()
		}
	}
}

func countInflight(dump map[kueue.ClusterQueueReference][]workload.Reference) int {
	total := 0
	for _, refs := range dump {
		total += len(refs)
	}
	return total
}

type drainArm struct {
	name   string
	gateOn bool
	budget int
}

// drainArms are the baseline, refill with no budget to spend, the budgets
// kueue#13729 has to choose a default from, and no bound at all -- the budget
// exists to cap cycle length, so the uncapped arm is what prices it.
func drainArms() []drainArm {
	arms := []drainArm{{name: "gate=off", budget: defaultRefillBudget}}
	for _, budget := range []int{0, 2, 4, 8} {
		arms = append(arms, drainArm{
			name:   fmt.Sprintf("gate=on/budget=%d", budget),
			gateOn: true,
			budget: budget,
		})
	}
	return append(arms, drainArm{name: "gate=on/budget=unbounded", gateOn: true, budget: math.MaxInt32})
}

// quantile returns the q-th quantile of already sorted durations.
func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)-1))
	return sorted[i]
}

func BenchmarkSchedulerFairSharingRefillDrain(b *testing.B) {
	for _, sc := range refillDrainScenarios() {
		for _, arm := range drainArms() {
			b.Run(fmt.Sprintf("scenario=%s/%s", sc.name, arm.name), func(b *testing.B) {
				b.ReportAllocs()
				features.SetFeatureGateDuringTest(b, features.FairSharingRefill, arm.gateOn)
				admissions := sc.admissions()
				// Warm the process before measuring. The first drain walks
				// every scheduling path for the first time, and the refill
				// arms are only a handful of cycles long, so that cost would
				// otherwise land in their first measured cycles.
				warmHarness, warmRun := setupDrain(b, sc, arm.budget, logr.Discard())
				warmRun.bench = nil // not measured, so it must not touch the timer
				drainToEmpty(b, warmHarness, sc, warmRun, 2*admissions+8)
				b.ResetTimer()

				var cycles, waitCycles, waitAdmits, iterations int
				var waitWall time.Duration
				var everyCycle []time.Duration
				var worstDrain []time.Duration
				cpu := &cpuMeter{}
				for b.Loop() {
					b.StopTimer()
					cpu.stop()
					h, run := setupDrain(b, sc, arm.budget, logr.Discard())
					run.cpu = cpu
					runtime.GC()
					b.StartTimer()
					cpu.start()
					drainToEmpty(b, h, sc, run, 2*admissions+8)
					b.StopTimer()
					cpu.stop()
					cycles += run.cycles
					waitCycles += run.waitCycles
					waitAdmits += run.waitAdmits
					waitWall += run.waitWall
					everyCycle = append(everyCycle, run.cycleTimes...)
					if slices.Max(run.cycleTimes) > slices.Max(append(worstDrain, 0)) {
						worstDrain = run.cycleTimes
					}
					iterations++
					b.StartTimer()
					cpu.start()
				}
				cpu.stop()
				b.ReportMetric(float64(cpu.total.Nanoseconds())/float64(iterations)/1e6, "cpu-ms")
				b.ReportMetric(float64(cycles)/float64(iterations), "cycles/op")
				b.ReportMetric(float64(admissions*iterations)/float64(cycles), "admits/cycle")
				slices.Sort(everyCycle)
				b.ReportMetric(float64(quantile(everyCycle, 0.50).Nanoseconds())/1e6, "p50-cycle-ms")
				b.ReportMetric(float64(quantile(everyCycle, 0.95).Nanoseconds())/1e6, "p95-cycle-ms")
				b.ReportMetric(float64(quantile(everyCycle, 1).Nanoseconds())/1e6, "max-cycle-ms")
				b.ReportMetric(float64(len(everyCycle)), "cycle-samples")
				if sc.arrival != nil {
					b.ReportMetric(float64(waitCycles)/float64(iterations), "wait-cycles")
					b.ReportMetric(float64(waitAdmits)/float64(iterations), "wait-admits")
					b.ReportMetric(float64(waitWall.Nanoseconds())/float64(iterations)/1e6, "wait-ms")
				}
				b.Logf("worst drain's per-cycle trace (ms): %s", formatTrace(worstDrain))
			})
		}
	}
}

// formatTrace renders one drain's cycle durations for pasting into a review.
func formatTrace(cycles []time.Duration) string {
	parts := make([]string, 0, len(cycles))
	for _, d := range cycles {
		parts = append(parts, fmt.Sprintf("%.2f", float64(d.Nanoseconds())/1e6))
	}
	return strings.Join(parts, " ")
}
