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
		"disable parent disables child": {
			input: map[featuregate.Feature]bool{
				TopologyAwareScheduling: false,
			},
			wantState: map[featuregate.Feature]bool{
				TopologyAwareScheduling:          false,
				TASFailedNodeReplacementFailFast: false,
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
// ignoring the anchor.
func TestAnchorAtQuotaReservationRequiresAdmissionFairSharing(t *testing.T) {
	// A copy, because SetFromMap records the raw values before it validates them
	// and does not roll them back when the validation fails. DeepCopy carries the
	// registered dependencies over; DeepCopyAndReset would drop them.
	gate := utilfeature.DefaultMutableFeatureGate.DeepCopy()
	err := gate.SetFromMap(map[string]bool{
		string(AdmissionFairSharing):                         false,
		string(AdmissionFairSharingAnchorAtQuotaReservation): true,
	})
	if err == nil {
		t.Fatal("enabling AdmissionFairSharingAnchorAtQuotaReservation with AdmissionFairSharing disabled should be rejected")
	}
	if !strings.Contains(err.Error(), string(AdmissionFairSharingAnchorAtQuotaReservation)) ||
		!strings.Contains(err.Error(), string(AdmissionFairSharing)) {
		t.Errorf("the rejection does not name both gates, so it may not be the dependency check: %v", err)
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
