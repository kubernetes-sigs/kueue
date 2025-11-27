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

package v1beta1

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestConfigurationQueueConvertTo(t *testing.T) {
	testCases := map[string]struct {
		input    *Configuration
		expected *v1beta2.Configuration
	}{
		"minimal Configuration": {
			input:    &Configuration{},
			expected: &v1beta2.Configuration{},
		},
		"with FairSharing": {
			input: &Configuration{
				FairSharing: &FairSharing{
					Enable:               true,
					PreemptionStrategies: []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare},
				},
			},
			expected: &v1beta2.Configuration{
				FairSharing: &v1beta2.FairSharing{
					PreemptionStrategies: []v1beta2.PreemptionStrategy{v1beta2.LessThanOrEqualToFinalShare, v1beta2.LessThanInitialShare},
				},
			},
		},
		"with FairSharing disabled": {
			input: &Configuration{
				FairSharing: &FairSharing{
					Enable:               false,
					PreemptionStrategies: []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare},
				},
			},
			expected: &v1beta2.Configuration{
				FairSharing: nil,
			},
		},
		"with FairSharing enabled, but preemption strategies are empty": {
			input: &Configuration{
				FairSharing: &FairSharing{
					Enable: true,
				},
			},
			expected: &v1beta2.Configuration{
				FairSharing: &v1beta2.FairSharing{
					PreemptionStrategies: []v1beta2.PreemptionStrategy{
						v1beta2.LessThanOrEqualToFinalShare,
						v1beta2.LessThanInitialShare,
					},
				},
			},
		},
		"with WaitForPodsReady": {
			input: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: true,
				},
			},
			expected: &v1beta2.Configuration{
				WaitForPodsReady: &v1beta2.WaitForPodsReady{
					Timeout:        metav1.Duration{Duration: defaultPodsReadyTimeout},
					BlockAdmission: ptr.To(true),
				},
			},
		},
		"with WaitForPodsReady disabled": {
			input: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable: false,
				},
			},
			expected: &v1beta2.Configuration{
				WaitForPodsReady: nil,
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &v1beta2.Configuration{}
			if err := tc.input.ConvertTo(result); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConfigurationQueueConvertFrom(t *testing.T) {
	testCases := map[string]struct {
		input    *v1beta2.Configuration
		expected *Configuration
	}{
		"minimal Configuration": {
			input:    &v1beta2.Configuration{},
			expected: &Configuration{},
		},
		"with FairSharing": {
			input: &v1beta2.Configuration{
				FairSharing: &v1beta2.FairSharing{
					PreemptionStrategies: []v1beta2.PreemptionStrategy{v1beta2.LessThanOrEqualToFinalShare, v1beta2.LessThanInitialShare},
				},
			},
			expected: &Configuration{
				FairSharing: &FairSharing{
					Enable:               true,
					PreemptionStrategies: []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare},
				},
			},
		},
		"with WaitForPodsReady": {
			input: &v1beta2.Configuration{
				WaitForPodsReady: &v1beta2.WaitForPodsReady{
					Timeout: metav1.Duration{Duration: defaultPodsReadyTimeout},
				},
			},
			expected: &Configuration{
				WaitForPodsReady: &WaitForPodsReady{
					Enable:         true,
					BlockAdmission: ptr.To(false),
					Timeout:        &metav1.Duration{Duration: defaultPodsReadyTimeout},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result := &Configuration{}
			if err := result.ConvertFrom(tc.input); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("unexpected conversion result (-want +got):\n%s", diff)
			}
		})
	}
}

func TestConfigurationQueueConversion_RoundTrip(t *testing.T) {
	testCases := map[string]struct {
		v1beta1Obj *Configuration
	}{
		"minimal Configuration": {
			v1beta1Obj: &Configuration{},
		},
		"with FairSharing": {
			v1beta1Obj: &Configuration{
				FairSharing: &FairSharing{
					Enable:               true,
					PreemptionStrategies: []PreemptionStrategy{LessThanOrEqualToFinalShare, LessThanInitialShare},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// Convert v1beta1 -> v1beta2
			v1beta2Obj := &v1beta2.Configuration{}
			if err := tc.v1beta1Obj.ConvertTo(v1beta2Obj); err != nil {
				t.Fatalf("ConvertTo failed: %v", err)
			}

			// Convert v1beta2 -> v1beta1 (round-trip)
			roundTripped := &Configuration{}
			if err := roundTripped.ConvertFrom(v1beta2Obj); err != nil {
				t.Fatalf("ConvertFrom failed: %v", err)
			}

			// Verify round-trip
			if diff := cmp.Diff(tc.v1beta1Obj, roundTripped); diff != "" {
				t.Errorf("round-trip conversion produced diff (-original +roundtripped):\n%s", diff)
			}
		})
	}
}
