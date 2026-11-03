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

package policy

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestCandidatesOrderingPartialPreemption locks the criterion-5 behavior of
// CandidatesOrdering: among otherwise-equal candidates, a partial-preemptible
// (elastic, opted-in) workload must sort before a non-elastic one, because
// shedding replicas toward minCount is a lower-cost preemption than evicting a
// whole workload. It also verifies the criterion ordering relative to priority.
func TestCandidatesOrderingPartialPreemption(t *testing.T) {
	// The gate must be enabled before NewInfo runs, since NewInfo derives
	// Info.IsPartialPreemptionJob from the gate + annotation at construction time.
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)

	_, log := utiltesting.ContextWithLog(t)
	now := time.Now()

	const cq kueue.ClusterQueueReference = "cq"

	makeInfo := func(name string, priority int32, partial bool) *workload.Info {
		wl := utiltestingapi.MakeWorkload(name, metav1.NamespaceDefault).
			Queue("default").
			Priority(priority).
			PodSets(*utiltestingapi.MakePodSet("main", 5).
				SetMinimumCount(2).
				Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		if partial {
			wl.Annotations = map[string]string{
				constants.ElasticJobAnnotation:        "true",
				constants.PartialPreemptionAnnotation: "true",
			}
		}
		info := workload.NewInfo(log, wl)
		info.ClusterQueue = cq
		return info
	}

	testCases := map[string]struct {
		a, b *workload.Info
		// want is the sign of CandidatesOrdering(a, b): -1 means a sorts first,
		// +1 means b sorts first.
		want int
	}{
		"partial-preemptible sorts before non-elastic (equal priority)": {
			a:    makeInfo("partial", 100, true),
			b:    makeInfo("whole", 100, false),
			want: -1,
		},
		"non-elastic sorts after partial-preemptible (equal priority)": {
			a:    makeInfo("whole", 100, false),
			b:    makeInfo("partial", 100, true),
			want: 1,
		},
		"lower priority wins before the partial-preemption criterion": {
			// Priority (criterion 4) is evaluated before partial-preemptibility
			// (criterion 5): the lower-priority non-elastic workload still sorts
			// first even though the other is partial-preemptible.
			a:    makeInfo("whole-low-pri", 50, false),
			b:    makeInfo("partial-high-pri", 100, true),
			want: -1,
		},
		"both partial-preemptible falls through to lower priority first": {
			a:    makeInfo("partial-low-pri", 50, true),
			b:    makeInfo("partial-high-pri", 100, true),
			want: -1,
		},
		"both non-elastic falls through to lower priority first": {
			a:    makeInfo("whole-low-pri", 50, false),
			b:    makeInfo("whole-high-pri", 100, false),
			want: -1,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := CandidatesOrdering(log, false, tc.a, tc.b, cq, now)
			if sign(got) != tc.want {
				t.Errorf("CandidatesOrdering() = %d (sign %d), want sign %d", got, sign(got), tc.want)
			}
		})
	}
}

func sign(x int) int {
	switch {
	case x < 0:
		return -1
	case x > 0:
		return 1
	default:
		return 0
	}
}

func TestCandidatesOrderingPartialPreemptionGateDisabled(t *testing.T) {
	// With the gate disabled, no workload is partial-preemptible, so criterion 5
	// is inert and ordering falls through to priority then reservation time.
	features.SetFeatureGateDuringTest(t, features.PartialPreemption, false)

	_, log := utiltesting.ContextWithLog(t)
	now := time.Now()
	const cq kueue.ClusterQueueReference = "cq"

	annotated := utiltestingapi.MakeWorkload("annotated", metav1.NamespaceDefault).
		Queue("default").
		Priority(100).
		Annotation(constants.PartialPreemptionAnnotation, "true").
		PodSets(*utiltestingapi.MakePodSet("main", 5).SetMinimumCount(2).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()
	plain := utiltestingapi.MakeWorkload("plain", metav1.NamespaceDefault).
		Queue("default").
		Priority(100).
		PodSets(*utiltestingapi.MakePodSet("main", 5).Request(corev1.ResourceCPU, "1").Obj()).
		Obj()

	aInfo := workload.NewInfo(log, annotated)
	aInfo.ClusterQueue = cq
	bInfo := workload.NewInfo(log, plain)
	bInfo.ClusterQueue = cq

	if aInfo.IsPartialPreemptionJob {
		t.Fatalf("IsPartialPreemptionJob should be false when the gate is disabled")
	}

	// Equal priority and no partial-preemptibility edge: ordering is decided by
	// the deterministic tie-breakers, not by the annotation. Reversing the
	// argument order must flip the sign (antisymmetry), proving the annotation
	// does not bias the result.
	ab := CandidatesOrdering(log, false, aInfo, bInfo, cq, now)
	ba := CandidatesOrdering(log, false, bInfo, aInfo, cq, now)
	if sign(ab) != -sign(ba) {
		t.Errorf("ordering not antisymmetric: ab=%d ba=%d", ab, ba)
	}
}

// TestCandidatesOrderingPendingReclaimTarget checks the HasPendingReclaimTarget ordering
// criterion: a workload with a pending reclaim target sorts before one without.
func TestCandidatesOrderingPendingReclaimTarget(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	_, log := utiltesting.ContextWithLog(t)
	// partialWl builds a partial-preemption workload admitted at count=5.
	// reclaimTarget < 0 means no ReclaimTargetCount is set.
	partialWl := func(name string, reserved time.Time, reclaimTarget int32) *workload.Info {
		psa := utiltestingapi.MakePodSetAssignment("executor").
			Assignment(corev1.ResourceCPU, "default", "5000m").
			Count(5).
			Obj()
		if reclaimTarget >= 0 {
			psa.ReclaimTargetCount = new(reclaimTarget)
		}
		wl := utiltestingapi.MakeWorkload(name, "ns").
			UID(types.UID(name)).
			Annotation(constants.PartialPreemptionAnnotation, "true").
			Priority(0).
			PodSets(*utiltestingapi.MakePodSet("executor", 5).
				Request(corev1.ResourceCPU, "1").
				SetMinimumCount(1).
				Obj()).
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("cq").
					PodSets(psa).
					Obj(),
				reserved,
			).
			Obj()
		return workload.NewInfo(log, wl)
	}

	const preemptorCQ = kueue.ClusterQueueReference("preemptor-cq")

	// wlSpec describes one candidate. reservedDelta offsets its admission time from now (a larger
	// delta means admitted more recently); reclaimTarget < 0 leaves ReclaimTargetCount unset.
	type wlSpec struct {
		reservedDelta time.Duration
		reclaimTarget int32
	}
	cases := map[string]struct {
		gateEnabled bool
		a, b        wlSpec
		// wantSign is the expected sign of CandidatesOrdering(a, b); the reverse ordering must be
		// its negation (antisymmetry).
		wantSign int
	}{
		"gate on: pending reclaim target sorts before a more recent no-target candidate": {
			// a has no reclaim target and is admitted more recently; b has count=5 > target=3
			// (HasPendingReclaimTarget=true), so b sorts first.
			gateEnabled: true,
			a:           wlSpec{reservedDelta: time.Minute, reclaimTarget: -1},
			b:           wlSpec{reservedDelta: 0, reclaimTarget: 3},
			wantSign:    1,
		},
		"gate on: satisfied reclaim target does not sort first, recency decides": {
			// a has count=5 == target=5 (not pending); both not pending, so the newer a sorts first.
			gateEnabled: true,
			a:           wlSpec{reservedDelta: time.Minute, reclaimTarget: 5},
			b:           wlSpec{reservedDelta: 0, reclaimTarget: -1},
			wantSign:    -1,
		},
		"gate on: recency decides when both have pending targets": {
			// both pending; the newer b sorts first.
			gateEnabled: true,
			a:           wlSpec{reservedDelta: 0, reclaimTarget: 3},
			b:           wlSpec{reservedDelta: time.Minute, reclaimTarget: 3},
			wantSign:    1,
		},
		"gate off: HasPendingReclaimTarget always false, recency decides": {
			// gate off → neither is pending; the newer b sorts first.
			gateEnabled: false,
			a:           wlSpec{reservedDelta: 0, reclaimTarget: 3},
			b:           wlSpec{reservedDelta: time.Minute, reclaimTarget: -1},
			wantSign:    1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PartialPreemption, tc.gateEnabled)
			a := partialWl("a", now.Add(tc.a.reservedDelta), tc.a.reclaimTarget)
			b := partialWl("b", now.Add(tc.b.reservedDelta), tc.b.reclaimTarget)
			if got := CandidatesOrdering(log, false, a, b, preemptorCQ, now); sign(got) != tc.wantSign {
				t.Errorf("CandidatesOrdering(a, b) = %d (sign %d), want sign %d", got, sign(got), tc.wantSign)
			}
			if got := CandidatesOrdering(log, false, b, a, preemptorCQ, now); sign(got) != -tc.wantSign {
				t.Errorf("CandidatesOrdering(b, a) = %d (sign %d), want sign %d (antisymmetry)", got, sign(got), -tc.wantSign)
			}
		})
	}
}
