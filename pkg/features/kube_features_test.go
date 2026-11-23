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
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
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

func TestSetFeatureGatesDuringTest(t *testing.T) {
	cases := map[string]struct {
		input     map[featuregate.Feature]bool
		wantState map[featuregate.Feature]bool
	}{
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
		"enable TAS replacement deferral sets TAS parents": {
			input: map[featuregate.Feature]bool{
				TASNodeReplacementDeferFailFastWhenUsefulInflightEvictions: true,
			},
			wantState: map[featuregate.Feature]bool{
				TASNodeReplacementDeferFailFastWhenUsefulInflightEvictions: true,
				TASFailedNodeReplacement:                                   true,
				TopologyAwareScheduling:                                    true,
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
			SetFeatureGatesDuringTest(t, tc.input)

			for fg, want := range tc.wantState {
				if got := utilfeature.DefaultFeatureGate.Enabled(fg); got != want {
					t.Errorf("unexpected state for feature gate %s: got %v, want %v", fg, got, want)
				}
			}
		})
	}
}

// AdmissionFairSharingAnchorAtQuotaReservation only does anything while AdmissionFairSharing
// is enabled, so the registry has to reject the combination rather than silently
// ignoring the anchor. Now that the anchor defaults to enabled, the dependency
// also rejects a configuration that names only the parent, so disabling
// AdmissionFairSharing means disabling both.
func TestAnchorAtQuotaReservationRequiresAdmissionFairSharing(t *testing.T) {
	cases := map[string]struct {
		set        map[string]bool
		wantReject bool
	}{
		"the anchor enabled while AdmissionFairSharing is disabled": {
			set: map[string]bool{
				string(AdmissionFairSharing):                         false,
				string(AdmissionFairSharingAnchorAtQuotaReservation): true,
			},
			wantReject: true,
		},
		"AdmissionFairSharing disabled on its own, leaving the anchor at its default": {
			set:        map[string]bool{string(AdmissionFairSharing): false},
			wantReject: true,
		},
		"both disabled together": {
			set: map[string]bool{
				string(AdmissionFairSharing):                         false,
				string(AdmissionFairSharingAnchorAtQuotaReservation): false,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// A copy, because SetFromMap records the raw values before it validates them
			// and does not roll them back when the validation fails. DeepCopy carries the
			// registered dependencies over; DeepCopyAndReset would drop them.
			gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
			err := gate.SetFromMap(tc.set)
			if !tc.wantReject {
				if err != nil {
					t.Fatalf("disabling both gates together should be accepted: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("the configuration leaves the anchor enabled without AdmissionFairSharing and should be rejected")
			}
			if !strings.Contains(err.Error(), string(AdmissionFairSharingAnchorAtQuotaReservation)) ||
				!strings.Contains(err.Error(), string(AdmissionFairSharing)) {
				t.Errorf("the rejection does not name both gates, so it may not be the dependency check: %v", err)
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

func TestTASReplaceMultipleFailedNodesDependencies(t *testing.T) {
	cases := map[string]struct {
		set                   map[string]bool
		wantEnabled           bool
		wantMissingDependency featuregate.Feature
	}{
		"disabled by default": {},
		"enabled with its dependencies": {
			set:         map[string]bool{string(TASReplaceMultipleFailedNodes): true},
			wantEnabled: true,
		},
		"does not require fail-fast eviction": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes):    true,
				string(TASFailedNodeReplacementFailFast): false,
			},
			wantEnabled: true,
		},
		"requires topology aware scheduling": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes): true,
				string(TopologyAwareScheduling):       false,
			},
			wantMissingDependency: TopologyAwareScheduling,
		},
		"requires failed node replacement": {
			set: map[string]bool{
				string(TASReplaceMultipleFailedNodes): true,
				string(TASFailedNodeReplacement):      false,
			},
			wantMissingDependency: TASFailedNodeReplacement,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
			err := gate.SetFromMap(tc.set)
			if tc.wantMissingDependency != "" {
				if err == nil {
					t.Fatal("expected the missing feature gate dependency to be rejected")
				}
				wantErr := fmt.Sprintf("%s is enabled, but depends on features that are disabled: [%s]", TASReplaceMultipleFailedNodes, tc.wantMissingDependency)
				if !strings.Contains(err.Error(), wantErr) {
					t.Errorf("expected dependency error %q, got: %v", wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected feature gate error: %v", err)
			}
			if got := gate.Enabled(TASReplaceMultipleFailedNodes); got != tc.wantEnabled {
				t.Errorf("unexpected feature gate state: got %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestSchedulerLibraryDeepIntegrationDependencies(t *testing.T) {
	cases := map[string]struct {
		set                   map[string]bool
		wantEnabled           bool
		wantMissingDependency featuregate.Feature
	}{
		"disabled by default": {},
		"enabled with its dependencies": {
			set: map[string]bool{
				string(SchedulerLibraryDeepIntegration): true,
				string(SchedulerLibraryIntegration):     true,
			},
			wantEnabled: true,
		},
		"requires SchedulerLibraryIntegration": {
			set: map[string]bool{
				string(SchedulerLibraryDeepIntegration): true,
				string(SchedulerLibraryIntegration):     false,
			},
			wantMissingDependency: SchedulerLibraryIntegration,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
			err := gate.SetFromMap(tc.set)
			if tc.wantMissingDependency != "" {
				if err == nil {
					t.Fatal("expected the missing feature gate dependency to be rejected")
				}
				wantErr := fmt.Sprintf("%s is enabled, but depends on features that are disabled: [%s]", SchedulerLibraryDeepIntegration, tc.wantMissingDependency)
				if !strings.Contains(err.Error(), wantErr) {
					t.Errorf("expected dependency error %q, got: %v", wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected feature gate error: %v", err)
			}
			if got := gate.Enabled(SchedulerLibraryDeepIntegration); got != tc.wantEnabled {
				t.Errorf("unexpected feature gate state: got %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestDQOMultiKueueCapacityProviderDependencies(t *testing.T) {
	cases := map[string]struct {
		set                   map[string]bool
		wantEnabled           bool
		wantMissingDependency featuregate.Feature
	}{
		"disabled by default": {},
		"enabled with its dependencies": {
			set: map[string]bool{
				string(DQOMultiKueueCapacityProvider): true,
				string(DynamicQuotaOrchestration):     true,
				string(MultiKueue):                    true,
			},
			wantEnabled: true,
		},
		"requires DynamicQuotaOrchestration": {
			set: map[string]bool{
				string(DQOMultiKueueCapacityProvider): true,
				string(DynamicQuotaOrchestration):     false,
				string(MultiKueue):                    true,
			},
			wantMissingDependency: DynamicQuotaOrchestration,
		},
		"requires MultiKueue": {
			set: map[string]bool{
				string(DQOMultiKueueCapacityProvider): true,
				string(DynamicQuotaOrchestration):     true,
				string(MultiKueue):                    false,
			},
			wantMissingDependency: MultiKueue,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
			err := gate.SetFromMap(tc.set)
			if tc.wantMissingDependency != "" {
				if err == nil {
					t.Fatal("expected the missing feature gate dependency to be rejected")
				}
				wantErr := fmt.Sprintf("%s is enabled, but depends on features that are disabled: [%s]", DQOMultiKueueCapacityProvider, tc.wantMissingDependency)
				if !strings.Contains(err.Error(), wantErr) {
					t.Errorf("expected dependency error %q, got: %v", wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected feature gate error: %v", err)
			}
			if got := gate.Enabled(DQOMultiKueueCapacityProvider); got != tc.wantEnabled {
				t.Errorf("unexpected feature gate state: got %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestKueueFeatureGates(t *testing.T) {
	gates := KueueFeatureGates()

	// The registry is process-wide and holds gates Kueue does not own: the meta gates
	// featuregate itself registers, plus every gate the apiserver libraries linked in
	// by the visibility server register. Reporting exactly the declared set - no more,
	// no less - is the contract callers rely on.
	wantNames := slices.Sorted(maps.Keys(defaultVersionedFeatureGates))
	gotNames := slices.Sorted(maps.Keys(gates))
	if diff := cmp.Diff(wantNames, gotNames); diff != "" {
		t.Errorf("Unexpected feature gates (-want,+got):\n%s", diff)
	}

	// The spec is the one resolved for the running version, not the first one declared:
	// PartialAdmission was introduced as Alpha and graduated to Beta.
	if got := gates[PartialAdmission].PreRelease; got != featuregate.Beta {
		t.Errorf("PreRelease of %q: got %q, want %q", PartialAdmission, got, featuregate.Beta)
	}
}

func TestConfigurablePreemptionLimitsDependencies(t *testing.T) {
	cases := map[string]struct {
		set                   map[string]bool
		wantEnabled           bool
		wantMissingDependency featuregate.Feature
	}{
		"disabled by default": {},
		"enabled with its dependencies": {
			set: map[string]bool{
				string(ConfigurablePreemptionLimits): true,
				string(ConfigurablePreemptions):      true,
			},
			wantEnabled: true,
		},
		"requires ConfigurablePreemptions": {
			set: map[string]bool{
				string(ConfigurablePreemptionLimits): true,
				string(ConfigurablePreemptions):      false,
			},
			wantMissingDependency: ConfigurablePreemptions,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
			err := gate.SetFromMap(tc.set)
			if tc.wantMissingDependency != "" {
				if err == nil {
					t.Fatal("expected the missing feature gate dependency to be rejected")
				}
				wantErr := fmt.Sprintf("%s is enabled, but depends on features that are disabled: [%s]", ConfigurablePreemptionLimits, tc.wantMissingDependency)
				if !strings.Contains(err.Error(), wantErr) {
					t.Errorf("expected dependency error %q, got: %v", wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected feature gate error: %v", err)
			}
			if got := gate.Enabled(ConfigurablePreemptionLimits); got != tc.wantEnabled {
				t.Errorf("unexpected feature gate state: got %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}
