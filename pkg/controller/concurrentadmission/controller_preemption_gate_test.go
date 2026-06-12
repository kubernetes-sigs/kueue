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

package concurrentadmission

import (
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// blockedOnPreemptionCondition returns a WorkloadBlockedOnPreemptionGates=True
// condition with the given signal time. The scheduler stamps this condition when
// a Variant needs preemption but its gate is still closed.
func blockedOnPreemptionCondition(t time.Time) metav1.Condition {
	return metav1.Condition{
		Type:               kueue.WorkloadBlockedOnPreemptionGates,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.PreemptionGated,
		Message:            "Workload requires preemption, but it's gated",
		LastTransitionTime: metav1.NewTime(t),
	}
}

func admittedCondition() metav1.Condition {
	return metav1.Condition{
		Type:    kueue.WorkloadAdmitted,
		Status:  metav1.ConditionTrue,
		Reason:  "ByTest",
		Message: "admitted",
	}
}

func caGate() kueue.PreemptionGate {
	return kueue.PreemptionGate{Name: constants.ConcurrentAdmissionPreemptionGate}
}

func caGateState(position kueue.PreemptionGatePosition, t time.Time) kueue.PreemptionGateState {
	return kueue.PreemptionGateState{
		Name:               constants.ConcurrentAdmissionPreemptionGate,
		Position:           position,
		LastTransitionTime: metav1.NewTime(t),
	}
}

func TestVariantToOpenPreemptionGate(t *testing.T) {
	now := metav1.Now().Time

	testCases := map[string]struct {
		// variants must be passed pre-sorted by flavor preference (index 0 =
		// most preferred), mirroring sortVariantsByFlavorOrder in Reconcile.
		variants      []kueue.Workload
		wantCandidate string // empty means no candidate (nil)
		wantRequeue   time.Duration
	}{
		"no variants": {
			variants:    nil,
			wantRequeue: 0,
		},
		"single blocked variant with a closed gate is selected": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, now)).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
			},
			wantCandidate: "spot",
			wantRequeue:   preemptionTimeout,
		},
		"blocked variant that is inactive is skipped": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					Active(false).
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
			},
			wantRequeue: 0,
		},
		"blocked variant that is admitted is skipped": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Condition(admittedCondition()).
					Obj(),
			},
			wantRequeue: 0,
		},
		"blocked variant that is finished is skipped": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Finished().
					Obj(),
			},
			wantRequeue: 0,
		},
		"variant with a closed gate but no blocked condition is not a candidate": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, now)).
					Obj(),
			},
			wantRequeue: 0,
		},
		"among multiple blocked variants the highest-preference one is selected": {
			variants: []kueue.Workload{
				// spot is first => highest preference, even though its signal
				// time is newer than on-demand's. Selection is by order, not time.
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
				*utiltestingapi.MakeWorkload("on-demand", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now.Add(-time.Minute))).
					Obj(),
			},
			wantCandidate: "spot",
			wantRequeue:   preemptionTimeout,
		},
		"an open gate opened recently blocks the next ungating until timeout": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, now.Add(-2*time.Minute))).
					Obj(),
				*utiltestingapi.MakeWorkload("on-demand", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
			},
			wantCandidate: "", // gated by timeout
			wantRequeue:   preemptionTimeout - 2*time.Minute,
		},
		"a stale open gate lets the next candidate be ungated": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, now.Add(-6*time.Minute))).
					Obj(),
				*utiltestingapi.MakeWorkload("on-demand", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
			},
			wantCandidate: "on-demand",
			wantRequeue:   preemptionTimeout,
		},
		"timeout uses the latest open-gate time among several": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, now.Add(-4*time.Minute))).
					Obj(),
				*utiltestingapi.MakeWorkload("on-demand", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, now.Add(-time.Minute))).
					Obj(),
				*utiltestingapi.MakeWorkload("reservation", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
			},
			wantCandidate: "", // latest open gate is 1m ago => 4m left
			wantRequeue:   preemptionTimeout - time.Minute,
		},
		"open gate but no blocked candidate yields no requeue": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, now.Add(-time.Minute))).
					Obj(),
			},
			wantRequeue: 0,
		},
		"open gate exactly at the timeout boundary allows ungating": {
			variants: []kueue.Workload{
				*utiltestingapi.MakeWorkload("spot", "default").
					PreemptionGates(caGate()).
					PreemptionGateStates(caGateState(kueue.PreemptionGatePositionOpen, now.Add(-preemptionTimeout))).
					Obj(),
				*utiltestingapi.MakeWorkload("on-demand", "default").
					PreemptionGates(caGate()).
					Condition(blockedOnPreemptionCondition(now)).
					Obj(),
			},
			wantCandidate: "on-demand",
			wantRequeue:   preemptionTimeout,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			r := &variantReconciler{
				clock: testingclock.NewFakeClock(now),
			}
			gotWl, gotRequeue := r.variantToOpenPreemptionGate(t.Context(), tc.variants)

			var gotCandidate string
			if gotWl != nil {
				gotCandidate = gotWl.Name
			}
			if gotCandidate != tc.wantCandidate {
				t.Errorf("variantToOpenPreemptionGate() candidate = %q, want %q", gotCandidate, tc.wantCandidate)
			}
			if gotRequeue != tc.wantRequeue {
				t.Errorf("variantToOpenPreemptionGate() requeue = %v, want %v", gotRequeue, tc.wantRequeue)
			}
		})
	}
}

func TestGenerateVariantStampsPreemptionGate(t *testing.T) {
	otherGate := kueue.PreemptionGate{Name: "other-gate"}

	testCases := map[string]struct {
		parentGates      []kueue.PreemptionGate
		wantVariantGates []kueue.PreemptionGate
	}{
		"parent without gates: variant gets only the ConcurrentAdmission gate": {
			wantVariantGates: []kueue.PreemptionGate{caGate()},
		},
		"parent with an existing gate: the ConcurrentAdmission gate is appended": {
			parentGates:      []kueue.PreemptionGate{otherGate},
			wantVariantGates: []kueue.PreemptionGate{otherGate, caGate()},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			parent := utiltestingapi.MakeWorkload("wl-12345", "default").
				Queue("lq").
				Label(constants.ConcurrentAdmissionParentLabelKey, "true").
				PreemptionGates(tc.parentGates...).
				Obj()

			variant := generateVariant(parent, "spot")

			if diff := cmp.Diff(tc.wantVariantGates, variant.Spec.PreemptionGates); diff != "" {
				t.Errorf("Unexpected variant preemption gates (-want +got):\n%s", diff)
			}

			// The gate must not leak back onto the parent's spec: generateVariant
			// copies parent.Spec by value, so the slice backing array is shared
			// until cloned.
			if diff := cmp.Diff(tc.parentGates, parent.Spec.PreemptionGates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Parent preemption gates were mutated (-want +got):\n%s", diff)
			}
		})
	}
}

func TestOpenPreemptionGate(t *testing.T) {
	now := metav1.Now().Time

	testCases := map[string]struct {
		variant *kueue.Workload
		// wantPosition is the gate position expected in status; empty means no
		// ConcurrentAdmission gate state is expected at all.
		wantPosition kueue.PreemptionGatePosition
		wantEvents   []utiltesting.EventRecord
	}{
		"closed gate is opened and an event is emitted": {
			variant: utiltestingapi.MakeWorkload("spot", "default").
				PreemptionGates(caGate()).
				PreemptionGateStates(caGateState(kueue.PreemptionGatePositionClosed, now.Add(-time.Hour))).
				Obj(),
			wantPosition: kueue.PreemptionGatePositionOpen,
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "default", Name: "spot"},
					EventType: corev1.EventTypeNormal,
					Reason:    ReasonOpenPreemptionGateVariant,
					Message:   "Opened preemption gate for variant workload \"default/spot\"",
				},
			},
		},
		"variant without the gate in its spec is a no-op": {
			variant:      utiltestingapi.MakeWorkload("spot", "default").Obj(),
			wantPosition: "",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().
				WithObjects(tc.variant).
				WithStatusSubresource(tc.variant).
				Build()

			recorder := &utiltesting.EventRecorder{}
			r := &variantReconciler{
				client:   cl,
				clock:    testingclock.NewFakeClock(now),
				recorder: recorder,
			}

			if err := r.openPreemptionGate(t.Context(), tc.variant); err != nil {
				t.Fatalf("openPreemptionGate() unexpected error: %v", err)
			}

			var got kueue.Workload
			if err := cl.Get(t.Context(), client.ObjectKeyFromObject(tc.variant), &got); err != nil {
				t.Fatal(err)
			}

			var gotPosition kueue.PreemptionGatePosition
			if i := slices.IndexFunc(got.Status.PreemptionGates, func(g kueue.PreemptionGateState) bool {
				return g.Name == constants.ConcurrentAdmissionPreemptionGate
			}); i != -1 {
				gotPosition = got.Status.PreemptionGates[i].Position
			}
			if gotPosition != tc.wantPosition {
				t.Errorf("gate position = %q, want %q", gotPosition, tc.wantPosition)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.EquateEmpty(), cmpopts.SortSlices(utiltesting.SortEvents)); diff != "" {
				t.Errorf("Unexpected events (-want +got):\n%s", diff)
			}
		})
	}
}
