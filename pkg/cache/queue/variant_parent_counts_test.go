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

package queue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	testingclock "k8s.io/utils/clock/testing"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// makeVariantInfo returns a Variant of the Parent named parentName, or a
// regular Workload when parentName is empty.
func makeVariantInfo(log logr.Logger, now time.Time, name, parentName string) *workload.Info {
	wl := utiltestingapi.MakeWorkload(name, defaultNamespace).
		Queue("lq").
		Creation(now)
	if parentName != "" {
		wl = wl.ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), parentName, "uid-"+parentName)
	}
	return workload.NewInfo(log, wl.Obj())
}

func TestVariantParentCountsExcess(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		disableConcurrentAdmission bool
		// Each entry is the Parent name of a pending workload; an empty name
		// is a regular Workload.
		active           []string
		inadmissible     []string
		inflight         []string
		wantActive       int
		wantInadmissible int
	}{
		"regular workloads only": {
			active:       []string{"", ""},
			inadmissible: []string{""},
		},
		"all Variants of a Parent active": {
			active:     []string{"a", "a", "a"},
			wantActive: 2,
		},
		"all Variants of a Parent inadmissible": {
			inadmissible:     []string{"a", "a", "a"},
			wantInadmissible: 2,
		},
		"Parent with active and inadmissible Variants counts as active": {
			active:           []string{"a"},
			inadmissible:     []string{"a", "a"},
			wantInadmissible: 2,
		},
		"Parent with an inflight Variant counts as active": {
			inadmissible:     []string{"a", "a"},
			inflight:         []string{"a"},
			wantInadmissible: 2,
		},
		"inflight and active Variants of the same Parent": {
			active:     []string{"a"},
			inflight:   []string{"a"},
			wantActive: 1,
		},
		"two Parents and a regular workload": {
			active:           []string{"a", "a", "b", ""},
			inadmissible:     []string{"b", "b"},
			wantActive:       1,
			wantInadmissible: 2,
		},
		"gate disabled": {
			disableConcurrentAdmission: true,
			active:                     []string{"a", "a"},
			inadmissible:               []string{"a"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, !tc.disableConcurrentAdmission)
			_, log := utiltesting.ContextWithLog(t)
			counts := newVariantParentCounts(nil)
			for i, parent := range tc.active {
				counts.addActive(makeVariantInfo(log, now, fmt.Sprintf("active-%d", i), parent))
			}
			for i, parent := range tc.inadmissible {
				counts.addInadmissible(makeVariantInfo(log, now, fmt.Sprintf("inadmissible-%d", i), parent))
			}
			for i, parent := range tc.inflight {
				// Mirror the Pop flow: the workload leaves the active bucket.
				info := makeVariantInfo(log, now, fmt.Sprintf("inflight-%d", i), parent)
				counts.addActive(info)
				counts.moveActiveToInflight(info)
			}

			gotActive, gotInadmissible := counts.excess()
			if gotActive != tc.wantActive || gotInadmissible != tc.wantInadmissible {
				t.Errorf("excess() = (%d, %d), want (%d, %d)", gotActive, gotInadmissible, tc.wantActive, tc.wantInadmissible)
			}
		})
	}
}

func TestPendingCountsEachParentOnce(t *testing.T) {
	tests := map[string]struct {
		disableConcurrentAdmission bool
		wantActive                 int
		wantInadmissible           int
	}{
		"gate enabled": {
			wantActive:       2,
			wantInadmissible: 0,
		},
		"gate disabled": {
			disableConcurrentAdmission: true,
			wantActive:                 3,
			wantInadmissible:           1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, !tc.disableConcurrentAdmission)
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now()
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))

			// Three Variants of one Parent and one regular Workload.
			for i := range 3 {
				cq.PushOrUpdate(makeVariantInfo(log, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent"))
			}
			cq.PushOrUpdate(makeVariantInfo(log, now.Add(3*time.Second), "regular", ""))

			// Check one Variant out and requeue another one as inadmissible.
			if inflight := cq.Pop(); inflight == nil {
				t.Fatal("expected to pop a workload")
			}
			cq.requeueIfNotPresent(log, cq.Pop(), false, RequeueReasonGeneric, "")

			active, inadmissible := cq.Pending()
			if active != tc.wantActive || inadmissible != tc.wantInadmissible {
				t.Errorf("Pending() = (%d, %d), want (%d, %d)", active, inadmissible, tc.wantActive, tc.wantInadmissible)
			}
			if got, want := cq.PendingTotal(), tc.wantActive+tc.wantInadmissible; got != want {
				t.Errorf("PendingTotal() = %d, want %d", got, want)
			}
			// The LocalQueue counts include the inflight Variant in active.
			lqActive, lqInadmissible := cq.PendingInLocalQueue(utilqueue.NewLocalQueueReference(defaultNamespace, "lq"))
			if lqActive != tc.wantActive || lqInadmissible != tc.wantInadmissible {
				t.Errorf("PendingInLocalQueue() = (%d, %d), want (%d, %d)", lqActive, lqInadmissible, tc.wantActive, tc.wantInadmissible)
			}
		})
	}
}

func TestPendingBreakdownCountsEachParentOnce(t *testing.T) {
	teamLabel := utiltestingapi.MakeCustomLabel("team").
		SourceLabelKey("team").
		SourceKind(config.SourceKindWorkload).
		TrackedValues("a", "b").
		Obj()
	lqRef := utilqueue.NewLocalQueueReference(defaultNamespace, "lq")
	// pushVariant pushes a Variant of parentName, or a regular Workload when parentName is empty, with the team label set to team.
	pushVariant := func(log logr.Logger, cq *ClusterQueue, now time.Time, name, parentName, team string) {
		info := makeVariantInfo(log, now, name, parentName)
		info.Obj.Labels = map[string]string{"team": team}
		cq.PushOrUpdate(info)
	}
	// requeueInadmissible pops the next workload and requeues it as inadmissible.
	requeueInadmissible := func(log logr.Logger, cq *ClusterQueue) {
		wInfo := cq.Pop()
		if wInfo == nil {
			t.Fatal("expected to pop a workload")
		}
		cq.requeueIfNotPresent(log, wInfo, false, RequeueReasonGeneric, "")
	}

	tests := map[string]struct {
		disableConcurrentAdmission bool
		ops                        func(logr.Logger, *ClusterQueue, time.Time)
		wantActive                 map[string]int
		wantInadmissible           map[string]int
		// wantPendingActive and wantPendingInadmissible are the totals that Pending() reports.
		wantPendingActive       int
		wantPendingInadmissible int
	}{
		"active, inflight and inadmissible Variants of one Parent": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 3 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				pushVariant(log, cq, now.Add(3*time.Second), "regular", "", "b")
				// Check one Variant out and requeue another one as inadmissible.
				if inflight := cq.Pop(); inflight == nil {
					t.Fatal("expected to pop a workload")
				}
				cq.requeueIfNotPresent(log, cq.Pop(), false, RequeueReasonGeneric, "")
			},
			wantActive:        map[string]int{"a": 1, "b": 1},
			wantPendingActive: 2,
		},
		"Parent with an inflight Variant counts as active": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 2 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				if inflight := cq.Pop(); inflight == nil {
					t.Fatal("expected to pop a workload")
				}
				cq.requeueIfNotPresent(log, cq.Pop(), false, RequeueReasonGeneric, "")
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"all Variants of one Parent inadmissible": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 3 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				for range 3 {
					cq.requeueIfNotPresent(log, cq.Pop(), false, RequeueReasonGeneric, "")
				}
			},
			wantInadmissible:        map[string]int{"a": 1},
			wantPendingInadmissible: 1,
		},
		"Variants of two Parents with different labels": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 2 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-a-%d", i), "parent-a", "a")
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-b-%d", i), "parent-b", "b")
				}
			},
			wantActive:        map[string]int{"a": 1, "b": 1},
			wantPendingActive: 2,
		},
		"Variants of one Parent with different labels": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				pushVariant(log, cq, now, "variant-0", "parent", "a")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "a")
				pushVariant(log, cq, now.Add(2*time.Second), "variant-2", "parent", "b")
				// Check both Variants with the label value a out.
				for range 2 {
					if inflight := cq.Pop(); inflight == nil {
						t.Fatal("expected to pop a workload")
					}
				}
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"Parent with an active Variant does not count its inadmissible label": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				pushVariant(log, cq, now, "variant-0", "parent", "b")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "a")
				requeueInadmissible(log, cq)
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"Parent with an inflight Variant does not count its inadmissible label": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				pushVariant(log, cq, now, "variant-0", "parent", "a")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "b")
				if inflight := cq.Pop(); inflight == nil {
					t.Fatal("expected to pop a workload")
				}
				requeueInadmissible(log, cq)
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"inadmissible Variants of one Parent with different labels": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				pushVariant(log, cq, now, "variant-0", "parent", "a")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "b")
				for range 2 {
					requeueInadmissible(log, cq)
				}
			},
			wantInadmissible:        map[string]int{"a": 1},
			wantPendingInadmissible: 1,
		},
		"label update on an active Variant": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				pushVariant(log, cq, now, "variant-0", "parent", "a")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "a")
				pushVariant(log, cq, now.Add(2*time.Second), "variant-2", "parent", "b")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "b")
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"label update on an inadmissible Variant": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				pushVariant(log, cq, now, "variant-0", "parent", "a")
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "a")
				pushVariant(log, cq, now.Add(2*time.Second), "variant-2", "parent", "b")
				for range 3 {
					requeueInadmissible(log, cq)
				}
				pushVariant(log, cq, now.Add(time.Second), "variant-1", "parent", "b")
			},
			wantInadmissible:        map[string]int{"a": 1},
			wantPendingInadmissible: 1,
		},
		"deleting Variants down to the last one": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 3 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				requeueInadmissible(log, cq)
				cq.Delete(log, workload.NewReference(defaultNamespace, "variant-0"))
				cq.Delete(log, workload.NewReference(defaultNamespace, "variant-1"))
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"deleting the last Variant": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 3 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				requeueInadmissible(log, cq)
				for i := range 3 {
					cq.Delete(log, workload.NewReference(defaultNamespace, fmt.Sprintf("variant-%d", i)))
				}
			},
		},
		"removing inflight Variants": {
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 3 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				// Check one Variant out and release it, as after its admission.
				inflight := cq.Pop()
				if inflight == nil {
					t.Fatal("expected to pop a workload")
				}
				cq.forgetInflight(workload.Key(inflight.Obj))
			},
			wantActive:        map[string]int{"a": 1},
			wantPendingActive: 1,
		},
		"gate disabled": {
			disableConcurrentAdmission: true,
			ops: func(log logr.Logger, cq *ClusterQueue, now time.Time) {
				for i := range 3 {
					pushVariant(log, cq, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent", "a")
				}
				pushVariant(log, cq, now.Add(3*time.Second), "regular", "", "b")
				if inflight := cq.Pop(); inflight == nil {
					t.Fatal("expected to pop a workload")
				}
				cq.requeueIfNotPresent(log, cq.Pop(), false, RequeueReasonGeneric, "")
			},
			wantActive:              map[string]int{"a": 2, "b": 1},
			wantInadmissible:        map[string]int{"a": 1},
			wantPendingActive:       3,
			wantPendingInadmissible: 1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, !tc.disableConcurrentAdmission)
			t.Cleanup(func() { metrics.InitMetricVectors(nil) })
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now()
			cl := metrics.NewCustomLabels([]config.ControllerMetricsCustomLabel{teamLabel})
			cq := newClusterQueueImpl(ctx, cl, defaultOrdering, testingclock.NewFakeClock(now))
			tc.ops(log, cq, now)

			wantActive, wantInadmissible := tc.wantActive, tc.wantInadmissible
			if wantActive == nil {
				wantActive = map[string]int{}
			}
			if wantInadmissible == nil {
				wantInadmissible = map[string]int{}
			}
			active, inadmissible := cq.PendingBreakdown(nil)
			if diff := cmp.Diff(wantActive, nonZeroCounts(active)); diff != "" {
				t.Errorf("PendingBreakdown() active (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(wantInadmissible, nonZeroCounts(inadmissible)); diff != "" {
				t.Errorf("PendingBreakdown() inadmissible (-want,+got):\n%s", diff)
			}
			pendingActive, pendingInadmissible := cq.Pending()
			if pendingActive != tc.wantPendingActive || pendingInadmissible != tc.wantPendingInadmissible {
				t.Errorf("Pending() = (%d, %d), want (%d, %d)", pendingActive, pendingInadmissible, tc.wantPendingActive, tc.wantPendingInadmissible)
			}
			// The per-label counts add up to the totals.
			if active.Total() != pendingActive || inadmissible.Total() != pendingInadmissible {
				t.Errorf("PendingBreakdown() totals = (%d, %d), Pending() = (%d, %d)", active.Total(), inadmissible.Total(), pendingActive, pendingInadmissible)
			}
			// Nothing is left behind once no workload is pending.
			if pendingActive+pendingInadmissible == 0 {
				vp, lqc := cq.workloads.variantParents, cq.workloads.localQueueCounts
				if n := len(vp.activeLabels) + len(vp.inadmissibleLabels) + len(vp.inflightLabels) + len(lqc.activeVariantLabels) + len(lqc.inadmissibleVariantLabels); n != 0 {
					t.Errorf("Found %d leftover per-Parent label trackers", n)
				}
			}
			lqActive, lqInadmissible := cq.PendingBreakdownInLocalQueue(lqRef, nil)
			if diff := cmp.Diff(wantActive, nonZeroCounts(lqActive)); diff != "" {
				t.Errorf("PendingBreakdownInLocalQueue() active (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(wantInadmissible, nonZeroCounts(lqInadmissible)); diff != "" {
				t.Errorf("PendingBreakdownInLocalQueue() inadmissible (-want,+got):\n%s", diff)
			}
		})
	}
}

// nonZeroCounts returns the counts of tracker keyed by the label values joined with commas, without the label values whose count is zero.
func nonZeroCounts(tracker *metrics.LabelValsTracker) map[string]int {
	result := make(map[string]int)
	for vals, count := range tracker.Iter() {
		if count != 0 {
			result[strings.Join(vals.OrderedList(), ",")] = count
		}
	}
	return result
}
