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
	initialTAS := utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling)
	initialTASFailFast := utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast)

	t.Run("enable child sets parent", func(t *testing.T) {
		SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
			TASFailedNodeReplacementFailFast: true,
		})
		if !utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast) {
			t.Error("expected TASFailedNodeReplacementFailFast to be enabled")
		}
		// Because TASFailedNodeReplacementFailFast depends on TopologyAwareScheduling and TASFailedNodeReplacement
		if !utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling) {
			t.Error("expected TopologyAwareScheduling to be enabled due to child dependency")
		}
		if !utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacement) {
			t.Error("expected TASFailedNodeReplacement to be enabled due to child dependency")
		}
	})

	// Verify cleanup restored state
	if utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling) != initialTAS {
		t.Errorf("cleanup failed for TopologyAwareScheduling, got %v, want %v", utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling), initialTAS)
	}
	if utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast) != initialTASFailFast {
		t.Errorf("cleanup failed for TASFailedNodeReplacementFailFast, got %v, want %v", utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast), initialTASFailFast)
	}

	t.Run("disable parent disables child", func(t *testing.T) {
		// First enable child to have a known state
		SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
			TASFailedNodeReplacementFailFast: true,
		})

		t.Run("disable parent", func(t *testing.T) {
			SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				TopologyAwareScheduling: false,
			})
			if utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling) {
				t.Error("expected TopologyAwareScheduling to be disabled")
			}
			if utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast) {
				t.Error("expected TASFailedNodeReplacementFailFast to be disabled due to parent being disabled")
			}
		})

		// After inner run, should be restored to enabled state
		if !utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast) {
			t.Error("cleanup failed to restore TASFailedNodeReplacementFailFast to enabled state")
		}
	})
}

func TestSetFeatureGateDuringTest(t *testing.T) {
	initialTAS := utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling)
	initialTASFailFast := utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast)

	t.Run("enable child", func(t *testing.T) {
		SetFeatureGateDuringTest(t, TASFailedNodeReplacementFailFast, true)
		if !utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast) {
			t.Error("expected child to be enabled")
		}
		if !utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling) {
			t.Error("expected parent to be enabled")
		}
	})

	if utilfeature.DefaultFeatureGate.Enabled(TopologyAwareScheduling) != initialTAS {
		t.Error("cleanup failed")
	}
	if utilfeature.DefaultFeatureGate.Enabled(TASFailedNodeReplacementFailFast) != initialTASFailFast {
		t.Error("cleanup failed")
	}
}
