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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// strategyLogMessage is emitted once per candidate ClusterQueue by both
// runFirstFsStrategy and runSecondFsStrategy.
const strategyLogMessage = "Evaluating FairSharing strategy"

// drsLogFields are the DominantResourceShare fields logged at the top level of
// both strategy log entries.
var drsLogFields = []string{"preemptorNewShare", "targetOldShare"}

// vLevel converts a logr V-level into the zap level that zapr maps it to.
func vLevel(v int) zapcore.Level {
	return zapcore.Level(-v)
}

// newObservedLogger returns a logr.Logger whose entries are captured, enabled
// up to and including the given logr V-level.
func newObservedLogger(enabledUpToV int) (logr.Logger, *observer.ObservedLogs) {
	core, observed := observer.New(vLevel(enabledUpToV))
	return zapr.NewLogger(zap.New(core)), observed
}

type fsLogFixture struct {
	preemptionCtx *preemptionCtx
	candidates    []*workload.Info
}

// fsLogClusterQueue describes a candidate ClusterQueue in the fixture: how
// many 1-CPU workloads it has admitted, and its FairSharing weight (nil for
// the API default).
type fsLogClusterQueue struct {
	name       kueue.ClusterQueueReference
	candidates int
	fairWeight *resource.Quantity
}

// newFsLogFixture builds a cohort holding the preemptor ClusterQueue "a" plus
// the given candidate ClusterQueues, and returns a preemptionCtx wired to the
// supplied logger together with the candidate workloads.
//
// Each ClusterQueue has 1 CPU of nominal quota. Every candidate ClusterQueue
// admits `candidates` workloads of 1 CPU, so it borrows and is not pruned by
// the target ordering. The preemptor's incoming workload requests 3 CPU, so
// the preemptor borrows too. That keeps runFirstFsStrategy on the strategy
// path instead of the FairSharingPreemptWithinNominal shortcut.
func newFsLogFixture(tb testing.TB, log logr.Logger, cqs []fsLogClusterQueue) fsLogFixture {
	tb.Helper()
	now := time.Now()
	ctx, setupLog := utiltesting.ContextWithLog(tb)

	flavor := utiltestingapi.MakeResourceFlavor("default").Obj()
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
	}
	var admitted []kueue.Workload
	for _, cq := range cqs {
		wrapper := utiltestingapi.MakeClusterQueue(string(cq.name)).
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1").Obj())
		if cq.fairWeight != nil {
			wrapper = wrapper.FairWeight(*cq.fairWeight)
		}
		clusterQueues = append(clusterQueues, wrapper.Obj())

		for i := 1; i <= cq.candidates; i++ {
			wl := utiltestingapi.MakeWorkload(fmt.Sprintf("%s-%d", cq.name, i), "").
				Request(corev1.ResourceCPU, "1").
				SimpleReserveQuota(cq.name, "default", now).
				Obj()
			// Set the name as UID so candidate ordering is deterministic.
			wl.UID = types.UID(wl.Name)
			admitted = append(admitted, *wl)
		}
	}

	cl := utiltesting.NewClientBuilder().
		WithLists(&kueue.WorkloadList{Items: admitted}).
		Build()
	cqCache := schdcache.New(cl)
	cqCache.AddOrUpdateResourceFlavor(setupLog, flavor)
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			tb.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	snapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		tb.Fatalf("unexpected error while building snapshot: %v", err)
	}

	incoming := utiltestingapi.MakeWorkload("a-incoming", "").
		Request(corev1.ResourceCPU, "3").Obj()
	wlInfo := workload.NewInfo(incoming)
	wlInfo.ClusterQueue = "a"
	assignment := singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
			Name: "default", Mode: flavorassigner.Preempt,
		},
	})

	preemptionCtx := &preemptionCtx{
		ctx:               ctx,
		clock:             clocktesting.NewFakeClock(now),
		log:               log,
		preemptor:         *wlInfo,
		preemptorCQ:       snapshot.ClusterQueue("a"),
		snapshot:          snapshot,
		frsNeedPreemption: flavorResourcesNeedPreemption(assignment),
		workloadUsage: workload.Usage{
			Quota: workload.ResourceUsage{
				Assigned: assignment.TotalRequestsFor(setupLog, wlInfo),
			},
		},
	}
	// fairPreemptions simulates the incoming workload's usage before running
	// the strategies, so that the DRS values include it. Mirror that here.
	preemptionCtx.preemptorCQ.SimulateUsageAddition(preemptionCtx.workloadUsage)

	candidates := make([]*workload.Info, 0, len(admitted))
	for i := range admitted {
		candidates = append(candidates, workload.NewInfo(&admitted[i]))
	}
	return fsLogFixture{preemptionCtx: preemptionCtx, candidates: candidates}
}

func alwaysFails(fairsharing.PreemptorNewShare, fairsharing.TargetOldShare, fairsharing.TargetNewShare) bool {
	return false
}

// targetClusterQueueName reads the targetClusterQueue field back out of a
// decoded log entry. klog.ObjectRef marshals to {"name": ..., "namespace": ...}.
func targetClusterQueueName(t *testing.T, decoded map[string]any) string {
	t.Helper()
	raw, ok := decoded["targetClusterQueue"]
	if !ok {
		t.Fatalf("log entry is missing the targetClusterQueue field: %v", decoded)
	}
	ref, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("targetClusterQueue has unexpected type %T", raw)
	}
	name, ok := ref["name"].(string)
	if !ok {
		t.Fatalf("targetClusterQueue is missing a string name: %v", ref)
	}
	return name
}

// strategyEvaluations reads the strategyEvaluations array field back out of a
// decoded log entry.
func strategyEvaluations(t *testing.T, decoded map[string]any) []map[string]any {
	t.Helper()
	// When a nested DRS is a raw +Inf float, json.Marshal of the array fails
	// and zapcore replaces the whole field with strategyEvaluationsError.
	if err, ok := decoded["strategyEvaluationsError"]; ok {
		t.Fatalf("strategyEvaluations failed to serialize: %v", err)
	}
	raw, ok := decoded["strategyEvaluations"]
	if !ok {
		t.Fatalf("log entry is missing the strategyEvaluations array field: %v", decoded)
	}
	rawEvaluations, ok := raw.([]any)
	if !ok {
		t.Fatalf("strategyEvaluations has unexpected type %T", raw)
	}
	evaluations := make([]map[string]any, 0, len(rawEvaluations))
	for _, rawEvaluation := range rawEvaluations {
		evaluation, ok := rawEvaluation.(map[string]any)
		if !ok {
			t.Fatalf("evaluation has unexpected type %T", rawEvaluation)
		}
		evaluations = append(evaluations, evaluation)
	}
	return evaluations
}

// decodeLogEntry renders an observed log entry through zap's JSON encoder,
// which is what the production logging stack does, and decodes the result.
// This is the form that reaches a log index, so it is the form in which the
// field types must be stable.
func decodeLogEntry(t *testing.T, entry observer.LoggedEntry) map[string]any {
	t.Helper()
	enc := zapcore.NewJSONEncoder(zapcore.EncoderConfig{
		MessageKey: "msg",
		LineEnding: zapcore.DefaultLineEnding,
	})
	buf, err := enc.EncodeEntry(entry.Entry, entry.Context)
	if err != nil {
		t.Fatalf("failed to encode log entry: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("log entry is not valid JSON (%v): %s", err, buf.String())
	}
	return decoded
}

// assertJSONString asserts that a decoded log field is a JSON string. The DRS
// fields must be strings for every value, so that their type in a log index
// does not depend on whether the value happened to be finite.
func assertJSONString(t *testing.T, fields map[string]any, key string) {
	t.Helper()
	value, ok := fields[key]
	if !ok {
		t.Fatalf("log entry is missing the %s field: %v", key, fields)
	}
	if _, ok := value.(string); !ok {
		t.Errorf("expected %s to be serialized as a JSON string, got %T (%v)", key, value, value)
	}
}

// TestRunFirstFsStrategyLogsOncePerCandidateClusterQueue asserts that the
// first FairSharing strategy emits one log entry per candidate ClusterQueue,
// carrying an array of per-candidate evaluations, rather than one entry per
// evaluated candidate workload.
func TestRunFirstFsStrategyLogsOncePerCandidateClusterQueue(t *testing.T) {
	log, observed := newObservedLogger(4)
	fixture := newFsLogFixture(t, log, []fsLogClusterQueue{
		{name: "b", candidates: 3},
		{name: "c", candidates: 2},
	})

	fits, targets, retryCandidates := runFirstFsStrategy(fixture.preemptionCtx, fixture.candidates, alwaysFails)
	if fits {
		t.Fatalf("expected the always-failing strategy to not fit")
	}
	if len(targets) != 0 {
		t.Fatalf("expected no targets, got %d", len(targets))
	}
	// All 5 candidates were evaluated, and all were rejected.
	if len(retryCandidates) != 5 {
		t.Fatalf("expected 5 retry candidates (i.e. 5 evaluations), got %d", len(retryCandidates))
	}

	entries := observed.FilterMessage(strategyLogMessage).All()
	// The defect being fixed: one entry per evaluated candidate workload (5)
	// instead of one entry per candidate ClusterQueue (2).
	if len(entries) != 2 {
		t.Fatalf("expected 1 log entry per candidate ClusterQueue (2 total), got %d", len(entries))
	}

	wantEvaluationsPerCQ := map[string]int{"b": 3, "c": 2}
	gotEvaluationsPerCQ := make(map[string]int, len(entries))
	for _, entry := range entries {
		decoded := decodeLogEntry(t, entry)
		targetCQ := targetClusterQueueName(t, decoded)

		// The constant-per-ClusterQueue fields are still present, once each.
		for _, key := range drsLogFields {
			if _, ok := decoded[key]; !ok {
				t.Errorf("log entry for %q is missing the %s field", targetCQ, key)
			}
		}
		// The per-candidate fields moved into the array.
		for _, key := range []string{"targetWorkload", "targetNewShare", "strategyPassed"} {
			if _, ok := decoded[key]; ok {
				t.Errorf("log entry for %q unexpectedly has per-candidate field %s at the top level", targetCQ, key)
			}
		}

		evaluations := strategyEvaluations(t, decoded)
		for _, evaluation := range evaluations {
			if name, _ := evaluation["targetWorkload"].(string); name == "" {
				t.Errorf("evaluation for %q is missing targetWorkload: %v", targetCQ, evaluation)
			}
			if _, ok := evaluation["targetNewShare"]; !ok {
				t.Errorf("evaluation for %q is missing targetNewShare: %v", targetCQ, evaluation)
			}
			if passed, _ := evaluation["strategyPassed"].(bool); passed {
				t.Errorf("evaluation for %q unexpectedly passed: %v", targetCQ, evaluation)
			}
		}
		gotEvaluationsPerCQ[targetCQ] = len(evaluations)
	}
	if diff := cmp.Diff(wantEvaluationsPerCQ, gotEvaluationsPerCQ); diff != "" {
		t.Errorf("Unexpected evaluations per ClusterQueue (-want,+got):\n%s", diff)
	}
}

// TestRunFirstFsStrategyLogRetainsStrategyPassed asserts that a passing
// evaluation is collapsed into the same entry as the failing ones that
// preceded it, and that its strategyPassed value is retained. The rare
// passing evaluations are the diagnostically interesting ones.
func TestRunFirstFsStrategyLogRetainsStrategyPassed(t *testing.T) {
	log, observed := newObservedLogger(4)
	fixture := newFsLogFixture(t, log, []fsLogClusterQueue{{name: "b", candidates: 3}})

	evaluated := 0
	failTwiceThenPass := func(fairsharing.PreemptorNewShare, fairsharing.TargetOldShare, fairsharing.TargetNewShare) bool {
		evaluated++
		return evaluated == 3
	}
	runFirstFsStrategy(fixture.preemptionCtx, fixture.candidates, failTwiceThenPass)

	if evaluated != 3 {
		t.Fatalf("expected 3 evaluations, got %d", evaluated)
	}
	entries := observed.FilterMessage(strategyLogMessage).All()
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 log entry for the 3 evaluations, got %d", len(entries))
	}
	evaluations := strategyEvaluations(t, decodeLogEntry(t, entries[0]))
	gotPassed := make([]bool, 0, len(evaluations))
	for _, evaluation := range evaluations {
		passed, ok := evaluation["strategyPassed"].(bool)
		if !ok {
			t.Fatalf("evaluation is missing a boolean strategyPassed: %v", evaluation)
		}
		gotPassed = append(gotPassed, passed)
	}
	if diff := cmp.Diff([]bool{false, false, true}, gotPassed); diff != "" {
		t.Errorf("Unexpected strategyPassed values (-want,+got):\n%s", diff)
	}
}

// TestFsStrategyLogDisabled asserts that nothing is emitted, and no array is
// built, when V(4) is not enabled.
func TestFsStrategyLogDisabled(t *testing.T) {
	t.Run("no log entry is emitted", func(t *testing.T) {
		// Enabled up to V(3) only, so V(4) is disabled.
		log, observed := newObservedLogger(3)
		fixture := newFsLogFixture(t, log, []fsLogClusterQueue{
			{name: "b", candidates: 3},
			{name: "c", candidates: 2},
		})

		runFirstFsStrategy(fixture.preemptionCtx, fixture.candidates, alwaysFails)

		if got := observed.Len(); got != 0 {
			t.Errorf("expected no log entries when V(4) is disabled, got %d: %v", got, observed.All())
		}
	})

	t.Run("no array is built", func(t *testing.T) {
		log, _ := newObservedLogger(3)
		fixture := newFsLogFixture(t, log, []fsLogClusterQueue{{name: "b", candidates: 3}})
		candCQ := firstCandidateClusterQueue(t, fixture)
		preemptorNewShare, targetOldShare := candCQ.ComputeShares()

		strategyLog := newFsStrategyLog(log, candCQ, preemptorNewShare, targetOldShare)
		if strategyLog.enabled {
			t.Fatalf("expected the strategy log to be disabled at V(4)")
		}
		for _, candWl := range fixture.candidates {
			strategyLog.record(candWl, fairsharing.TargetNewShare{}, false)
		}
		if len(strategyLog.entries) != 0 {
			t.Errorf("expected no entries to be accumulated when V(4) is disabled, got %d", len(strategyLog.entries))
		}
	})
}

// TestRunFirstFsStrategyLogSerializesDRS asserts that every
// DominantResourceShare logged by runFirstFsStrategy is serialized as a JSON
// string, whether it is finite or +Inf.
//
// A DRS is +Inf when the FairSharing weight is 0. zapcore writes a raw +Inf
// float64 as the JSON string "+Inf" but writes finite float64s as JSON
// numbers, so logging DRS as a raw float makes the field's type depend on the
// value. Inside the strategyEvaluations array it is worse: json.Marshal
// rejects +Inf outright, and zapcore drops the whole array in favour of an
// error field. PreciseWeightedShareSerialized fixes both. This applies the fix
// from #13154 to preemption.go.
func TestRunFirstFsStrategyLogSerializesDRS(t *testing.T) {
	zeroWeight := resource.MustParse("0")
	cases := map[string]struct {
		fairWeight         *resource.Quantity
		wantTargetOldShare string
		wantTargetNewShare string
	}{
		// Guards against the type instability: with the default weight every
		// DRS here is finite, and a raw float64 would be a JSON number.
		"finite DRS": {
			fairWeight:         nil,
			wantTargetOldShare: "1000",
			wantTargetNewShare: "500",
		},
		"infinite DRS": {
			fairWeight:         &zeroWeight,
			wantTargetOldShare: "+Inf",
			wantTargetNewShare: "+Inf",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log, observed := newObservedLogger(4)
			// 3 candidates so the target still borrows after one is removed,
			// which keeps targetNewShare non-zero.
			fixture := newFsLogFixture(t, log, []fsLogClusterQueue{
				{name: "b", candidates: 3, fairWeight: tc.fairWeight},
			})

			runFirstFsStrategy(fixture.preemptionCtx, fixture.candidates, alwaysFails)

			entries := observed.FilterMessage(strategyLogMessage).All()
			if len(entries) != 1 {
				t.Fatalf("expected exactly 1 log entry, got %d", len(entries))
			}
			decoded := decodeLogEntry(t, entries[0])

			for _, key := range drsLogFields {
				assertJSONString(t, decoded, key)
			}
			if got := decoded["targetOldShare"]; got != tc.wantTargetOldShare {
				t.Errorf("expected targetOldShare to be %q, got %v", tc.wantTargetOldShare, got)
			}

			evaluations := strategyEvaluations(t, decoded)
			if len(evaluations) != 3 {
				t.Fatalf("expected 3 evaluations, got %d", len(evaluations))
			}
			for _, evaluation := range evaluations {
				assertJSONString(t, evaluation, "targetNewShare")
				if got := evaluation["targetNewShare"]; got != tc.wantTargetNewShare {
					t.Errorf("expected targetNewShare to be %q, got %v", tc.wantTargetNewShare, got)
				}
			}
		})
	}
}

// TestRunSecondFsStrategyLog asserts that runSecondFsStrategy serializes its
// DominantResourceShare values as JSON strings, for both finite and +Inf DRS.
func TestRunSecondFsStrategyLog(t *testing.T) {
	zeroWeight := resource.MustParse("0")
	cases := map[string]struct {
		fairWeight         *resource.Quantity
		wantTargetOldShare string
	}{
		"finite DRS":   {fairWeight: nil, wantTargetOldShare: "1000"},
		"infinite DRS": {fairWeight: &zeroWeight, wantTargetOldShare: "+Inf"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log, observed := newObservedLogger(4)
			fixture := newFsLogFixture(t, log, []fsLogClusterQueue{
				{name: "b", candidates: 3, fairWeight: tc.fairWeight},
			})

			runSecondFsStrategy(fixture.candidates, fixture.preemptionCtx, nil)

			entries := observed.FilterMessage(strategyLogMessage).All()
			if len(entries) == 0 {
				t.Fatalf("expected at least 1 log entry from runSecondFsStrategy, got 0")
			}

			decoded := decodeLogEntry(t, entries[0])
			for _, key := range drsLogFields {
				assertJSONString(t, decoded, key)
			}
			if got := decoded["targetOldShare"]; got != tc.wantTargetOldShare {
				t.Errorf("expected targetOldShare to be %q, got %v", tc.wantTargetOldShare, got)
			}
		})
	}
}

// firstCandidateClusterQueue returns the first TargetClusterQueue that the
// target ordering yields for the fixture's candidates.
func firstCandidateClusterQueue(t *testing.T, fixture fsLogFixture) *fairsharing.TargetClusterQueue {
	t.Helper()
	ctx := fixture.preemptionCtx
	ordering := fairsharing.MakeClusterQueueOrdering(ctx.preemptorCQ, fixture.candidates, ctx.log, ctx.clock)
	for candCQ := range ordering.Iter() {
		return candCQ
	}
	t.Fatalf("the target ordering yielded no candidate ClusterQueue")
	return nil
}
