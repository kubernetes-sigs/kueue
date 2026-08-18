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

package common

import (
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestCandidatesOrderingPartialPreemption checks the partial-preemption ordering criterion:
// among equal-priority candidates, a partial-preemptible one is preferred (sorts first), but
// priority still dominates, and the whole criterion is a no-op when the gate is off.
func TestCandidatesOrderingPartialPreemption(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	// partialWl: opt-in elastic job admitted above minCount (executor count=5, minCount=1).
	partialWl := func(name string, prio int32, reserved time.Time) *workload.Info {
		wl := utiltesting.MakeWorkload(name, "ns").
			UID(types.UID(name)).
			Annotation(constants.PartialPreemptionAnnotation, "true").
			Priority(prio).
			PodSets(*utiltesting.MakePodSet("executor", 5).
				Request(corev1.ResourceCPU, "1").
				SetMinimumCount(1).
				Obj()).
			ReserveQuotaAt(
				utiltesting.MakeAdmission("cq").
					PodSets(utiltesting.MakePodSetAssignment("executor").
						Assignment(corev1.ResourceCPU, "default", "5000m").
						Count(5).
						Obj()).
					Obj(),
				reserved,
			).
			Obj()
		return workload.NewInfo(wl)
	}
	// plainWl: ordinary (non-partial) admitted workload.
	plainWl := func(name string, prio int32, reserved time.Time) *workload.Info {
		wl := utiltesting.MakeWorkload(name, "ns").
			UID(types.UID(name)).
			Priority(prio).
			PodSets(*utiltesting.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			ReserveQuotaAt(
				utiltesting.MakeAdmission("cq").
					PodSets(utiltesting.MakePodSetAssignment("main").
						Assignment(corev1.ResourceCPU, "default", "1000m").
						Count(1).
						Obj()).
					Obj(),
				reserved,
			).
			Obj()
		return workload.NewInfo(wl)
	}

	log := logr.Discard()
	// preemptor CQ differs from both candidates' CQ ("cq"), so the CQ-locality criterion is a no-op.
	const preemptorCQ = kueue.ClusterQueueReference("preemptor-cq")

	t.Run("gate on: partial-preemptible preferred among equal priority", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
		// Same priority; the plain one is admitted more recently (would win the recency tiebreak),
		// but the partial criterion sits before recency, so the partial one sorts first (< 0).
		a := partialWl("a-partial", 0, now)
		b := plainWl("b-plain", 0, now.Add(time.Minute))
		if got := CandidatesOrdering(log, false, a, b, preemptorCQ, now); got >= 0 {
			t.Errorf("CandidatesOrdering(partial, plain) = %d, want < 0 (partial first)", got)
		}
		if got := CandidatesOrdering(log, false, b, a, preemptorCQ, now); got <= 0 {
			t.Errorf("CandidatesOrdering(plain, partial) = %d, want > 0 (partial first)", got)
		}
	})

	t.Run("gate on: priority still dominates partial", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.PartialPreemption, true)
		// Lower-priority plain candidate must be preempted before a higher-priority partial one.
		lowPlain := plainWl("low-plain", 0, now)
		highPartial := partialWl("high-partial", 10, now)
		if got := CandidatesOrdering(log, false, lowPlain, highPartial, preemptorCQ, now); got >= 0 {
			t.Errorf("CandidatesOrdering(lowPlain, highPartial) = %d, want < 0 (lower priority first)", got)
		}
	})

	t.Run("gate off: partial criterion is a no-op", func(t *testing.T) {
		features.SetFeatureGateDuringTest(t, features.PartialPreemption, false)
		// Same inputs as the first case; with the gate off the partial criterion contributes 0,
		// so the recency criterion decides: the more recently admitted plain one sorts first (> 0).
		a := partialWl("a-partial", 0, now)
		b := plainWl("b-plain", 0, now.Add(time.Minute))
		if got := CandidatesOrdering(log, false, a, b, preemptorCQ, now); got <= 0 {
			t.Errorf("CandidatesOrdering(partial, plain) with gate off = %d, want > 0 (recency decides, not partial)", got)
		}
	})
}
