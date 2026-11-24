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

package disaggregatedset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"sigs.k8s.io/kueue/pkg/features"
)

func TestCanSupportIntegration(t *testing.T) {
	cases := map[string]struct {
		featureGateEnabled bool
		wantSupported      bool
		wantErr            error
	}{
		"feature gate disabled": {
			featureGateEnabled: false,
			wantSupported:      false,
			wantErr:            errFeatureGateDisabled,
		},
		"feature gate enabled": {
			featureGateEnabled: true,
			wantSupported:      true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.DisaggregatedSetIntegration, tc.featureGateEnabled)
			gotSupported, gotErr := CanSupportIntegration()
			if gotSupported != tc.wantSupported {
				t.Errorf("Unexpected supported, want=%v, got=%v", tc.wantSupported, gotSupported)
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
