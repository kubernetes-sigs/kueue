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

package features

import (
	"fmt"
	"strings"
	"testing"

	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/component-base/featuregate"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
)

func TestFeatureGate(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, PartialAdmission, false)

	if utilfeature.DefaultFeatureGate.Enabled(PartialAdmission) {
		t.Error("feature gate should be disabled")
	}
}

func TestTASResidualPodAccountingAlphaDefault(t *testing.T) {
	specs := defaultVersionedFeatureGates[TASResidualPodAccounting]
	if len(specs) != 1 {
		t.Fatalf("feature has %d version entries, want one Alpha entry", len(specs))
	}
	spec := specs[0]
	if spec.Default || spec.PreRelease != featuregate.Alpha || spec.Version.Major() != 0 || spec.Version.Minor() != 20 {
		t.Errorf("feature spec = %+v, want default-disabled Alpha for 0.20", spec)
	}
	if Enabled(TASResidualPodAccounting) {
		t.Error("Alpha residual Pod accounting must be disabled by default")
	}
}

func TestSetFeatureGatesDuringTest(t *testing.T) {
	type gateState struct {
		enabled       bool
		explicitlySet bool
	}
	snapshot := func() map[featuregate.Feature]gateState {
		state := make(map[featuregate.Feature]gateState)
		for fg := range utilfeature.DefaultMutableFeatureGate.GetAll() {
			state[fg] = gateState{
				enabled:       Enabled(fg),
				explicitlySet: utilfeature.DefaultMutableFeatureGate.ExplicitlySet(fg),
			}
		}
		return state
	}

	cases := map[string]struct {
		initial         map[featuregate.Feature]bool
		input           map[featuregate.Feature]bool
		directInput     map[string]bool
		wantState       map[featuregate.Feature]bool
		nestedInput     map[featuregate.Feature]bool
		wantNestedState map[featuregate.Feature]bool
	}{
		"nil overrides preserve registry state": {},
		"empty overrides preserve registry state": {
			input: map[featuregate.Feature]bool{},
		},
		"nil overrides guard direct registry changes": {
			directInput: map[string]bool{string(PartialAdmission): false},
			wantState:   map[featuregate.Feature]bool{PartialAdmission: false},
		},
		"empty overrides guard direct registry changes": {
			input:       map[featuregate.Feature]bool{},
			directInput: map[string]bool{string(PartialAdmission): false},
			wantState:   map[featuregate.Feature]bool{PartialAdmission: false},
		},
		"restore an explicitly overridden value": {
			initial:   map[featuregate.Feature]bool{PartialAdmission: false},
			input:     map[featuregate.Feature]bool{PartialAdmission: true},
			wantState: map[featuregate.Feature]bool{PartialAdmission: true},
		},
		"nested overrides restore dependency state": {
			input: map[featuregate.Feature]bool{TASFailedNodeReplacementFailFast: true},
			wantState: map[featuregate.Feature]bool{
				TopologyAwareScheduling:          true,
				TASFailedNodeReplacement:         true,
				TASFailedNodeReplacementFailFast: true,
			},
			nestedInput: map[featuregate.Feature]bool{TopologyAwareScheduling: false},
			wantNestedState: map[featuregate.Feature]bool{
				TopologyAwareScheduling:          false,
				TASFailedNodeReplacement:         false,
				TASFailedNodeReplacementFailFast: false,
			},
		},
		"enable child sets parent": {
			input: map[featuregate.Feature]bool{
				TASFailedNodeReplacementFailFast: true,
			},
			wantState: map[featuregate.Feature]bool{
				TASFailedNodeReplacementFailFast: true,
				TopologyAwareScheduling:          true,
				TASFailedNodeReplacement:         true,
			},
		},
		"enable residual accounting sets TAS": {
			input:     map[featuregate.Feature]bool{TASResidualPodAccounting: true},
			wantState: map[featuregate.Feature]bool{TASResidualPodAccounting: true, TopologyAwareScheduling: true},
		},
		"enable multiple node replacement sets parents": {
			input: map[featuregate.Feature]bool{
				TASReplaceMultipleFailedNodes: true,
			},
			wantState: map[featuregate.Feature]bool{
				TASReplaceMultipleFailedNodes: true,
				TopologyAwareScheduling:       true,
				TASFailedNodeReplacement:      true,
			},
		},
		"disable parent disables child": {
			input: map[featuregate.Feature]bool{
				TopologyAwareScheduling: false,
			},
			wantState: map[featuregate.Feature]bool{
				TopologyAwareScheduling:          false,
				TASFailedNodeReplacementFailFast: false,
				TASResidualPodAccounting:         false,
			},
		},
		"explicit map values take precedence over implicit dependency resolution": {
			input: map[featuregate.Feature]bool{
				TopologyAwareScheduling:          true,
				TASFailedNodeReplacementFailFast: false,
			},
			wantState: map[featuregate.Feature]bool{
				TopologyAwareScheduling:          true,
				TASFailedNodeReplacementFailFast: false,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if len(tc.initial) > 0 {
				featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, tc.initial)
			}
			before := snapshot()
			t.Run("override", func(t *testing.T) {
				SetFeatureGatesDuringTest(t, tc.input)
				if tc.directInput != nil {
					if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(tc.directInput); err != nil {
						t.Fatalf("failed to change feature gates directly: %v", err)
					}
				}
				for fg, want := range tc.wantState {
					if got := Enabled(fg); got != want {
						t.Errorf("unexpected state for feature gate %s: got %v, want %v", fg, got, want)
					}
				}
				if tc.nestedInput != nil {
					beforeNested := snapshot()
					t.Run("nested", func(t *testing.T) {
						SetFeatureGatesDuringTest(t, tc.nestedInput)
						for fg, want := range tc.wantNestedState {
							if got := Enabled(fg); got != want {
								t.Errorf("unexpected nested state for feature gate %s: got %v, want %v", fg, got, want)
							}
						}
					})
					if diff := cmp.Diff(beforeNested, snapshot(), cmp.AllowUnexported(gateState{})); diff != "" {
						t.Errorf("nested overrides leaked feature gate state (-before,+after):\n%s", diff)
					}
				}
			})
			if diff := cmp.Diff(before, snapshot(), cmp.AllowUnexported(gateState{})); diff != "" {
				t.Errorf("overrides leaked feature gate state (-before,+after):\n%s", diff)
			}
		})
	}
}

func TestSetFeatureGateDuringTest(t *testing.T) {
	cases := map[string]struct {
		feature   featuregate.Feature
		value     bool
		wantState map[featuregate.Feature]bool
	}{
		"enable child": {
			feature: TASFailedNodeReplacementFailFast,
			value:   true,
			wantState: map[featuregate.Feature]bool{
				TASFailedNodeReplacementFailFast: true,
				TopologyAwareScheduling:          true,
			},
		},
		"enable child SchedulerLibraryIntegration sets TopologyAwareScheduling": {
			feature: SchedulerLibraryIntegration,
			value:   true,
			wantState: map[featuregate.Feature]bool{
				SchedulerLibraryIntegration: true,
				TopologyAwareScheduling:     true,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			SetFeatureGateDuringTest(t, tc.feature, tc.value)

			for fg, want := range tc.wantState {
				if got := utilfeature.DefaultFeatureGate.Enabled(fg); got != want {
					t.Errorf("unexpected state for feature gate %s: got %v, want %v", fg, got, want)
				}
			}
		})
	}
}

func TestFeatureGateDependencies(t *testing.T) {
	testCases := map[string]struct {
		set                     map[string]bool
		wantEnabled             map[featuregate.Feature]bool
		wantMissingDependencies map[featuregate.Feature][]featuregate.Feature
	}{
		// AdmissionFairSharingAnchorAtQuotaReservation only does anything while AdmissionFairSharing
		// is enabled, so the registry has to reject the combination rather than silently
		// ignoring the anchor. Now that the anchor defaults to enabled, the dependency
		// also rejects a configuration that names only the parent, so disabling
		// AdmissionFairSharing means disabling both.
		"AdmissionFairSharingAnchorAtQuotaReservation: enabled by default": {
			wantEnabled: map[featuregate.Feature]bool{
				AdmissionFairSharingAnchorAtQuotaReservation: true,
			},
		},
		"AdmissionFairSharingAnchorAtQuotaReservation: anchor enabled while AdmissionFairSharing is disabled": {
			set: map[string]bool{
				string(AdmissionFairSharing):                         false,
				string(AdmissionFairSharingAnchorAtQuotaReservation): true,
			},
			wantMissingDependencies: map[featuregate.Feature][]featuregate.Feature{
				AdmissionFairSharingAnchorAtQuotaReservation: {AdmissionFairSharing},
			},
		},
		"AdmissionFairSharingAnchorAtQuotaReservation: AdmissionFairSharing disabled on its own": {
			set: map[string]bool{
				string(AdmissionFairSharing): false,
			},
			wantMissingDependencies: map[featuregate.Feature][]featuregate.Feature{
				AdmissionFairSharingAnchorAtQuotaReservation: {AdmissionFairSharing},
			},
		},
		"AdmissionFairSharingAnchorAtQuotaReservation: both disabled together": {
			set: map[string]bool{
				string(AdmissionFairSharing):                         false,
				string(AdmissionFairSharingAnchorAtQuotaReservation): false,
			},
			wantEnabled: map[featuregate.Feature]bool{
				AdmissionFairSharing:                         false,
				AdmissionFairSharingAnchorAtQuotaReservation: false,
			},
		},

		"TASReplaceMultipleFailedNodes: disabled by default": {
			wantEnabled: map[featuregate.Feature]bool{
				TASReplaceMultipleFailedNodes: false,
			},
		},
		"TASReplaceMultipleFailedNodes: enabled with its dependencies": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes): true,
			},
			wantEnabled: map[featuregate.Feature]bool{
				TASReplaceMultipleFailedNodes: true,
			},
		},
		"TASReplaceMultipleFailedNodes: does not require fail-fast eviction": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes):    true,
				string(TASFailedNodeReplacementFailFast): false,
			},
			wantEnabled: map[featuregate.Feature]bool{
				TASReplaceMultipleFailedNodes: true,
			},
		},
		"TASReplaceMultipleFailedNodes: requires topology aware scheduling": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes): true,
				string(TopologyAwareScheduling):       false,
			},
			wantMissingDependencies: map[featuregate.Feature][]featuregate.Feature{
				TASReplaceMultipleFailedNodes: {TopologyAwareScheduling},
			},
		},
		"TASReplaceMultipleFailedNodes: requires failed node replacement": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes): true,
				string(TASFailedNodeReplacement):      false,
			},
			wantMissingDependencies: map[featuregate.Feature][]featuregate.Feature{
				TASReplaceMultipleFailedNodes: {TASFailedNodeReplacement},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// A copy, because SetFromMap records the raw values before it validates them
			// and does not roll them back when the validation fails. DeepCopy carries the
			// registered dependencies over; DeepCopyAndReset would drop them.
			gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
			err := gate.SetFromMap(tc.set)
			if len(tc.wantMissingDependencies) > 0 {
				if err == nil {
					t.Fatal("expected the missing feature gate dependency to be rejected")
				}
				for feature, missing := range tc.wantMissingDependencies {
					wantErr := fmt.Sprintf("%s is enabled, but depends on features that are disabled: %v", feature, missing)
					if !strings.Contains(err.Error(), wantErr) {
						t.Errorf("expected dependency error %q, got: %v", wantErr, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected feature gate error: %v", err)
			}
			for feature, want := range tc.wantEnabled {
				if got := gate.Enabled(feature); got != want {
					t.Errorf("unexpected feature gate state for %s: got %v, want %v", feature, got, want)
				}
			}
		})
	}
}
