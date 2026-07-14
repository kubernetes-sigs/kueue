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

func TestDRAFeatureGraduationVersions(t *testing.T) {
	tests := []struct {
		name             string
		feature          featuregate.Feature
		expectedVersions []string
		expectedDefaults []bool
		expectedStages   []string
	}{
		{
			name:             "KueueDRAIntegrationExtendedResource graduation",
			feature:          KueueDRAIntegrationExtendedResource,
			expectedVersions: []string{"0.18", "0.19"},
			expectedDefaults: []bool{false, true},
			expectedStages:   []string{"Alpha", "Beta"},
		},
		{
			name:             "KueueDRAIntegrationPartitionableDevices graduation",
			feature:          KueueDRAIntegrationPartitionableDevices,
			expectedVersions: []string{"0.18", "0.19"},
			expectedDefaults: []bool{false, true},
			expectedStages:   []string{"Alpha", "Beta"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs, exists := defaultVersionedFeatureGates[tt.feature]
			if !exists {
				t.Fatalf("Feature %s not found in defaultVersionedFeatureGates", tt.feature)
			}

			if len(specs) != len(tt.expectedVersions) {
				t.Errorf("Expected %d version specs, got %d", len(tt.expectedVersions), len(specs))
			}

			for i, spec := range specs {
				expectedVersion := tt.expectedVersions[i]
				expectedDefault := tt.expectedDefaults[i]
				expectedStage := tt.expectedStages[i]

				if spec.Version.String() != expectedVersion {
					t.Errorf("Spec %d: expected version %s, got %s", i, expectedVersion, spec.Version.String())
				}

				if spec.Default != expectedDefault {
					t.Errorf("Spec %d (v%s): expected default=%v, got %v", i, expectedVersion, expectedDefault, spec.Default)
				}

				var stageStr string
				switch spec.PreRelease {
				case featuregate.Alpha:
					stageStr = "Alpha"
				case featuregate.Beta:
					stageStr = "Beta"
				case featuregate.GA:
					stageStr = "GA"
				case featuregate.Deprecated:
					stageStr = "Deprecated"
				}

				if stageStr != expectedStage {
					t.Errorf("Spec %d (v%s): expected stage %s, got %s", i, expectedVersion, expectedStage, stageStr)
				}
			}
		})
	}
}
