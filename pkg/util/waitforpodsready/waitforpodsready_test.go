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

package waitforpodsready

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestPodsScheduledTrackingEnabled(t *testing.T) {
	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		cfg          *configapi.WaitForPodsReady
		want         bool
	}{
		"nil config": {
			featureGates: map[featuregate.Feature]bool{
				features.DisableWaitForPodsReady:            false,
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
		},
		"feature gate disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.DisableWaitForPodsReady:            false,
				features.WaitForPodsReadyUnscheduledTimeout: false,
			},
			cfg: &configapi.WaitForPodsReady{
				Timeout:            metav1.Duration{Duration: 5 * time.Minute},
				UnscheduledTimeout: &metav1.Duration{Duration: time.Minute},
			},
		},
		"unscheduledTimeout unset": {
			featureGates: map[featuregate.Feature]bool{
				features.DisableWaitForPodsReady:            false,
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			cfg: &configapi.WaitForPodsReady{Timeout: metav1.Duration{Duration: 5 * time.Minute}},
		},
		"unscheduledTimeout zero": {
			featureGates: map[featuregate.Feature]bool{
				features.DisableWaitForPodsReady:            false,
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			cfg: &configapi.WaitForPodsReady{
				Timeout:            metav1.Duration{Duration: 5 * time.Minute},
				UnscheduledTimeout: &metav1.Duration{},
			},
		},
		"unscheduledTimeout positive": {
			featureGates: map[featuregate.Feature]bool{
				features.DisableWaitForPodsReady:            false,
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			cfg: &configapi.WaitForPodsReady{
				Timeout:            metav1.Duration{Duration: 5 * time.Minute},
				UnscheduledTimeout: &metav1.Duration{Duration: time.Minute},
			},
			want: true,
		},
		"legacy readiness disabled without scheduling tracking": {
			featureGates: map[featuregate.Feature]bool{
				features.DisableWaitForPodsReady:            true,
				features.WaitForPodsReadyUnscheduledTimeout: false,
			},
			cfg: &configapi.WaitForPodsReady{
				Timeout: metav1.Duration{Duration: 5 * time.Minute},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			if got := PodsScheduledTrackingEnabled(tc.cfg); got != tc.want {
				t.Errorf("PodsScheduledTrackingEnabled() = %t, want %t", got, tc.want)
			}
		})
	}
}
