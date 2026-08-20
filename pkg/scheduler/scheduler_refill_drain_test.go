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
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

// refillCensus counts what refill did during a drain, read off the V(3) lines
// the scheduler already emits. Counting from outside the cycle would mean
// exporting the pass's internals; the log lines are the contract the future
// refill_stops_total metric (kueue#14205) will publish anyway.
type refillCensus struct {
	// nominated is the pops that produced an entry the cycle went on to
	// process, and notNominated the pops that produced nothing.
	nominated    int
	notNominated int
	// deferred is the nominated pops the Fit-only rule sent back: a full
	// assignment computed and thrown away, budget spent, nothing admitted.
	deferred int
	stops    map[string]int
}

// newDrainObservers returns the two readers of one drain's log stream. The
// verbosity has to reach V(4) for the tournament's share values, which the
// fairness reader needs; benchmarks discard their logger instead, so the extra
// work of serializing them stays out of every measured drain.
func newDrainObservers(t *testing.T, sc *drainScenario) (*refillCensus, *budgetFairness, logr.Logger) {
	t.Helper()
	c := &refillCensus{stops: map[string]int{}}
	f := newBudgetFairness(sc)
	return c, f, funcr.NewJSON(func(obj string) {
		entry := make(map[string]any)
		if err := json.Unmarshal([]byte(obj), &entry); err != nil {
			t.Errorf("Failed to parse log entry %q: %v", obj, err)
			return
		}
		c.observe(entry)
		f.observe(entry)
	}, funcr.Options{Verbosity: 4})
}

func (c *refillCensus) observe(entry map[string]any) {
	switch entry["msg"] {
	case "Refilled the ClusterQueue's next workload into the running cycle":
		c.nominated++
	case "Refill stopped after an admission":
		reason := fmt.Sprint(entry["reason"])
		c.stops[reason]++
		if reason == string(refillStopSuccessorNotNominated) {
			c.notNominated++
		}
	case "Refilled workload cannot act on its assignment; deferring to the next cycle":
		c.deferred++
	}
}

func (c *refillCensus) pops() int     { return c.nominated + c.notNominated }
func (c *refillCensus) admitted() int { return c.nominated - c.deferred }

func (c *refillCensus) String() string {
	stops := make([]string, 0, len(c.stops))
	for _, reason := range slices.Sorted(maps.Keys(c.stops)) {
		stops = append(stops, fmt.Sprintf("%s=%d", reason, c.stops[reason]))
	}
	return fmt.Sprintf("%d pops (%d admitted, %d deferred, %d not nominated); stops: %v",
		c.pops(), c.admitted(), c.deferred, c.notNominated, stops)
}

// budgetFairness measures what exhausting the budget costs in fairness terms,
// which cycles and wall clock cannot show. It reads the tournament's own log
// lines: the winner of each pop, and the share every candidate held at that
// pop.
//
// When the budget stops a chain, the ClusterQueue it stopped has a successor
// waiting -- that is what separates BudgetExhausted from QueueEmpty -- and that
// successor is never nominated, so it cannot appear in any later tournament of
// the cycle. If the cycle then admits from a ClusterQueue holding a strictly
// larger share, a cycle that had taken a fresh snapshot instead would have
// served the stopped queue first, and that difference is the residue.
//
// Comparing the share a stopped queue held at that moment against a later
// winner's is sound because a stopped queue admits nothing more this cycle: its
// borrowed amount, and therefore its share, cannot move again before the next
// snapshot.
type budgetFairness struct {
	// wlToCQ resolves the workload keys the share values are logged under.
	wlToCQ map[string]kueue.ClusterQueueReference
	// shares is what each candidate held at the most recent pop.
	shares map[kueue.ClusterQueueReference]float64
	// stopped are the ClusterQueues the budget cut in the current cycle, with
	// the share each held when it happened. Reset at every cycle boundary.
	stopped map[kueue.ClusterQueueReference]float64
	// pendingWinner is the ClusterQueue of the pop whose share values have not
	// arrived yet: the winner is logged before them.
	pendingWinner kueue.ClusterQueueReference

	exhausted int
	// overtaken counts admissions that went to a ClusterQueue holding a
	// strictly larger share than one the budget had already stopped.
	overtaken int
	worstGap  float64
}

func newBudgetFairness(sc *drainScenario) *budgetFairness {
	lqToCQ := make(map[string]kueue.ClusterQueueReference, len(sc.localQueues))
	for i := range sc.localQueues {
		lqToCQ[sc.localQueues[i].Name] = sc.localQueues[i].Spec.ClusterQueue
	}
	f := &budgetFairness{
		wlToCQ:  make(map[string]kueue.ClusterQueueReference, len(sc.workloads)+1),
		shares:  map[kueue.ClusterQueueReference]float64{},
		stopped: map[kueue.ClusterQueueReference]float64{},
	}
	record := func(wl *kueue.Workload) {
		f.wlToCQ[string(workload.Key(wl))] = lqToCQ[string(wl.Spec.QueueName)]
	}
	for i := range sc.workloads {
		record(&sc.workloads[i])
	}
	if sc.arrival != nil {
		record(sc.arrival)
	}
	return f
}

func (f *budgetFairness) observe(entry map[string]any) {
	switch entry["msg"] {
	case "Scheduling cycle starts":
		f.stopped = map[kueue.ClusterQueueReference]float64{}
	case "Determined tournament winner":
		f.pendingWinner = logRefName(entry["clusterQueue"])
	case "DominantResourceShare values used during tournament":
		f.readShares(entry["drsValues"])
		f.judge()
	case "Refill stopped after an admission":
		if fmt.Sprint(entry["reason"]) != string(refillStopBudget) {
			return
		}
		f.exhausted++
		cq := logRefName(entry["clusterQueue"])
		if share, ok := f.shares[cq]; ok {
			f.stopped[cq] = share
		}
	}
}

// judge charges the pop just logged against the queues the budget has already
// stopped in this cycle.
func (f *budgetFairness) judge() {
	winner := f.pendingWinner
	f.pendingWinner = ""
	won, ok := f.shares[winner]
	if !ok {
		return
	}
	for _, stopped := range f.stopped {
		// A hair of slack: shares are floats, and equal shares are not a
		// fairness question.
		if gap := won - stopped; gap > 1e-9 {
			f.overtaken++
			f.worstGap = max(f.worstGap, gap)
			return
		}
	}
}

func (f *budgetFairness) readShares(logged any) {
	values, ok := logged.([]any)
	if !ok {
		return
	}
	f.shares = make(map[kueue.ClusterQueueReference]float64, len(values))
	for _, v := range values {
		fields, ok := v.(map[string]any)
		if !ok {
			continue
		}
		share, err := strconv.ParseFloat(fmt.Sprint(fields["drs"]), 64)
		if err != nil {
			continue
		}
		if cq, ok := f.wlToCQ[fmt.Sprint(fields["workload"])]; ok {
			f.shares[cq] = share
		}
	}
}

// String reports the residue. Shares are per mille of what the cohort can lend,
// so a gap of 27 is 2.7% of lendable capacity. The count is an upper bound: it
// does not check that the stopped queue's successor would have been admitted,
// only that a queue holding a smaller share was passed over while it had one
// waiting.
func (f *budgetFairness) String() string {
	return fmt.Sprintf("budget exhausted %d times; %d later admissions went to a larger share (worst gap %.1f per mille of lendable)",
		f.exhausted, f.overtaken, f.worstGap)
}

// logRefName reads the name out of a klog.KRef the JSON sink rendered as an
// object.
func logRefName(logged any) kueue.ClusterQueueReference {
	fields, ok := logged.(map[string]any)
	if !ok {
		return ""
	}
	return kueue.ClusterQueueReference(fmt.Sprint(fields["name"]))
}

// watchRefillSpread records, per ClusterQueue, how many workloads left its heap
// beyond the one head it is served per cycle -- that is, how much of the global
// budget each queue took. Deferred pops return to the heap within the cycle, so
// they are not counted, and a mid-cycle arrival makes its queue's count an
// undercount; both err on the low side.
func (h *drainHarness) watchRefillSpread(deepest *int, perCQ map[kueue.ClusterQueueReference]int) {
	prev := h.queues.Dump()
	h.afterCycle = func() {
		cur := h.queues.Dump()
		for cq, refs := range prev {
			drained := len(refs) - len(cur[cq])
			*deepest = max(*deepest, drained)
			if drained > 1 {
				perCQ[cq] += drained - 1
			}
		}
		prev = cur
	}
}

// TestRefillDrainScenarios pins the benchmark fixtures' invariants so they
// cannot silently stop measuring what they are named for: every scenario drains
// fully in both arms, refill engages, each fixture moves the drain length in
// the direction it declares -- including the wide fixture, which declares that
// refill cannot help it -- and the late-arrival fixtures keep separating the
// case refill reaches from the case it does not.
func TestRefillDrainScenarios(t *testing.T) {
	for _, sc := range refillDrainScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			borrowers := borrowingClusterQueues(sc)
			if len(borrowers) != sc.wantBorrowers {
				t.Errorf("%d ClusterQueues must borrow to drain %v, want %d", len(borrowers), borrowers, sc.wantBorrowers)
			}
			runs := map[bool]*drainRun{}
			census := map[bool]*refillCensus{}
			deepest := 0
			perCQ := map[kueue.ClusterQueueReference]int{}
			for _, gateOn := range []bool{false, true} {
				features.SetFeatureGateDuringTest(t, features.FairSharingRefill, gateOn)
				c, fairness, log := newDrainObservers(t, sc)
				h, run := setupDrain(t, sc, defaultRefillBudget, log)
				if gateOn {
					h.watchRefillSpread(&deepest, perCQ)
				}
				drainToEmpty(t, h, sc, run, 2*sc.admissions()+8)
				runs[gateOn] = run
				census[gateOn] = c
				if gateOn && fairness.exhausted > 0 {
					t.Logf("fairness at the budget boundary: %v", fairness)
				}
			}
			t.Logf("cycles to drain %d workloads: gate off %d, gate on %d", sc.admissions(), runs[false].cycles, runs[true].cycles)
			t.Logf("refill: %v", census[true])
			t.Logf("refills taken per ClusterQueue: %v (deepest single-cycle drain %d)", perCQ, deepest)

			assertDrainLength(t, sc, runs)
			if census[false].pops() != 0 {
				t.Errorf("the gate-off arm refilled %d times", census[false].pops())
			}
			if census[true].pops() == 0 {
				t.Errorf("refill never popped, so the fixture measures nothing")
			}
			if deepest < sc.wantChainDepth {
				t.Errorf("deepest single-cycle drain of one ClusterQueue was %d, want %d", deepest, sc.wantChainDepth)
			}
			if sc.wantWastedPops && census[true].deferred == 0 {
				t.Errorf("no refilled workload was deferred, so the fixture does not price the Fit-only rule")
			}
			if sc.arrival == nil {
				return
			}
			t.Logf("late arrival waited: gate off %d cycles / %d admissions / %v, gate on %d cycles / %d admissions / %v",
				runs[false].waitCycles, runs[false].waitAdmits, runs[false].waitWall,
				runs[true].waitCycles, runs[true].waitAdmits, runs[true].waitWall)
			for _, gateOn := range []bool{false, true} {
				if runs[gateOn].waitWall <= 0 {
					t.Errorf("the arrival's wall-clock wait was not measured with the gate %v", gateOn)
				}
			}
			assertArrivalWait(t, sc, runs)
		})
	}
}

// TestRefillBudgetAcrossCohorts reports how a global budget lands when two
// cohorts compete for it. The tournament arbitrates inside a cohort; between
// cohorts nothing does, and getCq chooses whose tournament runs next by ranging
// a map, so which cohort spends the budget is not a guarantee the scheduler
// makes. The point of the test is therefore the spread over repeated drains,
// not a share: it asserts only that neither cohort is shut out, and prints what
// each drain did so a policy discussion has numbers rather than intuitions.
func TestRefillBudgetAcrossCohorts(t *testing.T) {
	var sc *drainScenario
	for _, candidate := range refillDrainScenarios() {
		if candidate.name == "two-cohorts" {
			sc = candidate
		}
	}
	if sc == nil {
		t.Fatal("the two-cohorts fixture is gone, so nothing measures the budget across cohorts")
	}
	cohortOf := make(map[kueue.ClusterQueueReference]string, len(sc.clusterQueues))
	cohorts := sets.New[string]()
	for i := range sc.clusterQueues {
		cohort := string(sc.clusterQueues[i].Spec.CohortName)
		cohortOf[kueue.ClusterQueueReference(sc.clusterQueues[i].Name)] = cohort
		cohorts.Insert(cohort)
	}
	if len(cohorts) < 2 {
		t.Fatalf("the fixture has %d cohort(s), so it cannot measure a split", len(cohorts))
	}

	features.SetFeatureGateDuringTest(t, features.FairSharingRefill, true)
	const drains = 8
	shares := map[string][]int{}
	for run := range drains {
		deepest := 0
		perCQ := map[kueue.ClusterQueueReference]int{}
		_, _, log := newDrainObservers(t, sc)
		h, drain := setupDrain(t, sc, defaultRefillBudget, log)
		h.watchRefillSpread(&deepest, perCQ)
		drainToEmpty(t, h, sc, drain, 2*sc.admissions()+8)
		perCohort := map[string]int{}
		for cq, taken := range perCQ {
			perCohort[cohortOf[cq]] += taken
		}
		parts := make([]string, 0, len(cohorts))
		for _, cohort := range slices.Sorted(maps.Keys(perCohort)) {
			parts = append(parts, fmt.Sprintf("%s=%d", cohort, perCohort[cohort]))
			shares[cohort] = append(shares[cohort], perCohort[cohort])
		}
		t.Logf("drain %d: %d cycles, refills per cohort %v", run+1, drain.cycles, parts)
		for cohort := range cohorts {
			if perCohort[cohort] == 0 && cohort != "" {
				t.Errorf("cohort %s took none of the budget in drain %d", cohort, run+1)
			}
		}
	}
	for _, cohort := range slices.Sorted(maps.Keys(shares)) {
		taken := shares[cohort]
		t.Logf("cohort %s over %d drains: min %d, max %d", cohort, drains, slices.Min(taken), slices.Max(taken))
	}
}

// assertDrainLength holds each fixture to the direction it declares. A fixture
// that shows refill unable to shorten a drain is reporting a result, so it must
// not be treated as a fixture that stopped working.
func assertDrainLength(t *testing.T, sc *drainScenario, runs map[bool]*drainRun) {
	t.Helper()
	on, off := runs[true].cycles, runs[false].cycles
	if sc.refillSavesCycles {
		if on >= off {
			t.Errorf("refill did not reduce cycles to drain: gate on %d, gate off %d", on, off)
		}
		return
	}
	if on != off {
		t.Errorf("refill changed a drain it cannot help: gate on %d, gate off %d", on, off)
	}
}

// assertArrivalWait pins the two sides of the late-arrival trade-off: refill
// reaches a newcomer within the running cycle when its ClusterQueue is still
// admitting, and never reaches one whose ClusterQueue is not.
//
// The wait is counted in cycles. The companion count of admissions that go by
// is logged but not asserted: it rises with refill's throughput rather than
// with the newcomer's delay, and its baseline here is structurally zero,
// because a newcomer alone in its ClusterQueue is popped by Heads before that
// cycle admits anything.
func assertArrivalWait(t *testing.T, sc *drainScenario, runs map[bool]*drainRun) {
	t.Helper()
	if sc.refillReachesArrival {
		if runs[true].waitCycles != 0 {
			t.Errorf("refill did not pick up the arrival within the running cycle: waited %d cycles", runs[true].waitCycles)
		}
		if runs[false].waitCycles == 0 {
			t.Errorf("the baseline picked up the arrival within the running cycle, so the fixture proves nothing")
		}
		return
	}
	if runs[true].waitCycles < runs[false].waitCycles {
		t.Errorf("the arrival waited fewer cycles with refill on (%d) than off (%d), in a ClusterQueue refill never pops",
			runs[true].waitCycles, runs[false].waitCycles)
	}
}

// assertScenarioForcesBorrowing fails unless at least one ClusterQueue has
// more pending workloads (1 CPU each) than nominal quota, which guarantees
// the drain admits beyond nominal and produces a nonzero DRS at some point.
// Guards against a future quota edit silently degrading the fixture into a
// pure-FIFO benchmark.
// borrowingClusterQueues returns the ClusterQueues whose queued workloads
// exceed their own nominal quota, so that draining them has to borrow from the
// cohort. Borrowing is what makes a dominant resource share non-zero, so this
// is also how much of the fair-sharing tournament a fixture exercises: with
// fewer than two borrowers no share competes with another and the ordering
// falls through to creation time, which is plain FIFO.
//
// Every workload requests one CPU, and nominal quota is summed across the
// ClusterQueue's flavors: many-flavors puts its whole quota on the last one.
func borrowingClusterQueues(sc *drainScenario) []kueue.ClusterQueueReference {
	lqToCQ := make(map[string]kueue.ClusterQueueReference, len(sc.localQueues))
	for i := range sc.localQueues {
		lqToCQ[sc.localQueues[i].Name] = sc.localQueues[i].Spec.ClusterQueue
	}
	queuedPerCQ := make(map[kueue.ClusterQueueReference]int64)
	for i := range sc.workloads {
		queuedPerCQ[lqToCQ[string(sc.workloads[i].Spec.QueueName)]]++
	}
	if sc.arrival != nil {
		queuedPerCQ[sc.arrivalCQ]++
	}
	var borrowers []kueue.ClusterQueueReference
	for i := range sc.clusterQueues {
		cq := &sc.clusterQueues[i]
		var nominal int64
		for _, rg := range cq.Spec.ResourceGroups {
			for _, flavor := range rg.Flavors {
				for _, r := range flavor.Resources {
					if r.Name == corev1.ResourceCPU {
						nominal += r.NominalQuota.Value()
					}
				}
			}
		}
		if queuedPerCQ[kueue.ClusterQueueReference(cq.Name)] > nominal {
			borrowers = append(borrowers, kueue.ClusterQueueReference(cq.Name))
		}
	}
	return borrowers
}
