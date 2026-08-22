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

package reclaimbackoff

import (
	"testing"
	"time"

	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
)

var (
	testCQ  = kueue.ClusterQueueReference("cq-a")
	testFR  = resources.FlavorResource{Flavor: "default", Resource: "memory"}
	otherFR = resources.FlavorResource{Flavor: "default", Resource: "cpu"}
)

// backoff base·2^(n-1) with a tiny jitter (0.0001), so assert the returned
// cooldown lands in [expected, expected*1.001).
func assertCooldown(t *testing.T, got, want time.Duration) {
	t.Helper()
	if got < want || got >= want+want/1000+time.Millisecond {
		t.Errorf("cooldown = %v, want ~%v", got, want)
	}
}

func TestRecordReclaimGrowsBackoff(t *testing.T) {
	base := 10 * time.Second
	tests := map[string]struct {
		reclaims int
		want     time.Duration
	}{
		"first reclaim uses base": {reclaims: 1, want: 10 * time.Second},
		"second reclaim doubles":  {reclaims: 2, want: 20 * time.Second},
		"third reclaim doubles":   {reclaims: 3, want: 40 * time.Second},
		"fourth reclaim doubles":  {reclaims: 4, want: 80 * time.Second},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fakeClock := testingclock.NewFakeClock(time.Now())
			tr := New(base, time.Hour, 10*time.Minute, fakeClock)

			var got time.Duration
			for range tc.reclaims {
				got = tr.RecordReclaim(testCQ, testFR)
			}
			assertCooldown(t, got, tc.want)
		})
	}
}

func TestRecordReclaimCapsAtMax(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(time.Minute, 90*time.Second, time.Hour, fakeClock)

	// 1st: 60s, 2nd would be 120s but is capped at 90s (plus jitter).
	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), time.Minute)
	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), 90*time.Second)
}

func TestResetAfterQuietPeriod(t *testing.T) {
	base := 10 * time.Second
	reset := 5 * time.Minute
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(base, time.Hour, reset, fakeClock)

	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), 10*time.Second) // count=1
	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), 20*time.Second) // count=2

	// Stay within the reset window: counter keeps growing.
	fakeClock.Step(reset)
	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), 40*time.Second) // count=3, still within reset

	// Exceed the reset window: counter restarts from base.
	fakeClock.Step(reset + time.Second)
	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), 10*time.Second) // count reset to 1
}

func TestIsBackingOffExpiry(t *testing.T) {
	base := 10 * time.Second
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(base, time.Hour, time.Hour, fakeClock)

	if tr.IsBackingOff(testCQ, testFR) {
		t.Fatal("expected no backoff before any reclaim")
	}

	tr.RecordReclaim(testCQ, testFR) // ~10s cooldown
	if !tr.IsBackingOff(testCQ, testFR) {
		t.Fatal("expected backoff active right after reclaim")
	}

	// Advance just under the cooldown: still backing off.
	fakeClock.Step(9 * time.Second)
	if !tr.IsBackingOff(testCQ, testFR) {
		t.Fatal("expected backoff still active before cooldown elapsed")
	}

	// Advance past the cooldown: expired.
	fakeClock.Step(2 * time.Second)
	if tr.IsBackingOff(testCQ, testFR) {
		t.Fatal("expected backoff expired after cooldown elapsed")
	}
}

func TestIsolationBetweenKeys(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(10*time.Second, time.Hour, time.Hour, fakeClock)

	tr.RecordReclaim(testCQ, testFR)
	if !tr.IsBackingOff(testCQ, testFR) {
		t.Fatal("expected the reclaimed (cq, fr) to be backing off")
	}
	if tr.IsBackingOff(testCQ, otherFR) {
		t.Error("a different resource on the same CQ must not be affected")
	}
	if tr.IsBackingOff("cq-b", testFR) {
		t.Error("the same resource on a different CQ must not be affected")
	}
}

func TestRecordReclaimPrunesExpiredEntries(t *testing.T) {
	base := 10 * time.Second
	reset := time.Minute
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(base, time.Hour, reset, fakeClock)

	tr.RecordReclaim(testCQ, testFR)
	tr.RecordReclaim(testCQ, otherFR)
	if got := len(tr.state); got != 2 {
		t.Fatalf("expected 2 entries, got %d", got)
	}

	// Within the reset window nothing is pruned, even after the cooldown expires.
	fakeClock.Step(30 * time.Second)
	tr.RecordReclaim("cq-b", testFR)
	if got := len(tr.state); got != 3 {
		t.Fatalf("expected no pruning within the reset window, got %d entries, want 3", got)
	}

	// Past cooldown and reset window, the dead entries are pruned by the next
	// reclaim anywhere; the entry still backing off (long cooldown) survives.
	fakeClock.Step(2 * reset)
	tr.RecordReclaim("cq-c", testFR)
	if got := len(tr.state); got != 1 {
		t.Fatalf("expected dead entries to be pruned, got %d entries, want 1", got)
	}
	if !tr.IsBackingOff("cq-c", testFR) {
		t.Error("the newly recorded entry must survive pruning")
	}
}

func TestRecordReclaimKeepsActiveBackoffPastResetWindow(t *testing.T) {
	// With max > reset, an entry can still be backing off when the reset window
	// passes; pruning must not clear such an active backoff.
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(10*time.Second, time.Hour, 15*time.Second, fakeClock)

	tr.RecordReclaim(testCQ, testFR)                                    // ~10s cooldown
	assertCooldown(t, tr.RecordReclaim(testCQ, testFR), 20*time.Second) // backoffUntil ~20s from now

	// Past the 15s reset window but still inside the ~20s cooldown.
	fakeClock.Step(18 * time.Second)
	tr.RecordReclaim("cq-b", testFR)
	if !tr.IsBackingOff(testCQ, testFR) {
		t.Error("an entry still in its cooldown must not be pruned, even past the reset window")
	}
}

func TestRecordReclaimStaysCappedOverLongSequence(t *testing.T) {
	max := time.Minute
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(10*time.Second, max, time.Hour, fakeClock)

	// 10s, 20s, 40s, then capped at 60s for the remaining reclaims.
	for _, want := range []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second, time.Minute, time.Minute, time.Minute, time.Minute, time.Minute} {
		assertCooldown(t, tr.RecordReclaim(testCQ, testFR), want)
	}
}

func TestDeleteClusterQueue(t *testing.T) {
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(10*time.Second, time.Hour, time.Hour, fakeClock)

	tr.RecordReclaim(testCQ, testFR)
	tr.RecordReclaim(testCQ, otherFR)
	tr.RecordReclaim("cq-b", testFR)

	// Deleting the ClusterQueue purges its entries even while their cooldowns
	// are still active; other ClusterQueues are unaffected.
	tr.DeleteClusterQueue(testCQ)

	if got := len(tr.state); got != 1 {
		t.Fatalf("expected only cq-b's entry to survive, got %d entries", got)
	}
	if tr.IsBackingOff(testCQ, testFR) {
		t.Error("entries of the deleted ClusterQueue must be purged before their cooldown expires")
	}
	if !tr.IsBackingOff("cq-b", testFR) {
		t.Error("another ClusterQueue's entries must not be affected")
	}

	// Deleting an unknown ClusterQueue is a no-op.
	tr.DeleteClusterQueue("cq-unknown")
	if !tr.IsBackingOff("cq-b", testFR) {
		t.Error("deleting an unknown ClusterQueue must be a no-op")
	}
}

func TestReadPathPrunesExpiredEntries(t *testing.T) {
	base := 10 * time.Second
	reset := time.Minute
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(base, time.Hour, reset, fakeClock)

	tr.RecordReclaim(testCQ, testFR)
	tr.RecordReclaim(testCQ, otherFR)

	// Past cooldown and reset window, with no further reclaim recorded: the
	// read path drops the dead entries on its own.
	fakeClock.Step(2 * reset)
	if tr.IsBackingOff(testCQ, testFR) {
		t.Fatal("expected backoff expired")
	}
	if got := len(tr.state); got != 1 {
		t.Fatalf("expected IsBackingOff to prune the queried entry, got %d entries, want 1", got)
	}
	if got := tr.MinRemaining(testCQ); got != 0 {
		t.Fatalf("expected no remaining cooldown, got %v", got)
	}
	if got := len(tr.state); got != 0 {
		t.Fatalf("expected MinRemaining to prune the remaining entry, got %d entries, want 0", got)
	}
}

func TestMinRemainingReturnsEarliestDeadline(t *testing.T) {
	base := 10 * time.Second
	fakeClock := testingclock.NewFakeClock(time.Now())
	tr := New(base, time.Hour, time.Hour, fakeClock)

	tr.RecordReclaim(testCQ, testFR)  // ~10s cooldown
	tr.RecordReclaim(testCQ, otherFR) // ~10s cooldown
	tr.RecordReclaim(testCQ, otherFR) // ~20s cooldown
	tr.RecordReclaim("cq-b", testFR)  // another CQ: ignored

	got := tr.MinRemaining(testCQ)
	if got < base || got >= base+time.Second {
		t.Errorf("MinRemaining = %v, want ~%v (the earliest deadline)", got, base)
	}

	// Past the first cooldown, the second one remains.
	fakeClock.Step(11 * time.Second)
	got = tr.MinRemaining(testCQ)
	if got < 8*time.Second || got >= 10*time.Second {
		t.Errorf("MinRemaining = %v, want ~9s (remaining on the longer cooldown)", got)
	}

	// Past all cooldowns: zero.
	fakeClock.Step(10 * time.Second)
	if got := tr.MinRemaining(testCQ); got != 0 {
		t.Errorf("MinRemaining = %v, want 0 after all cooldowns elapsed", got)
	}
}
