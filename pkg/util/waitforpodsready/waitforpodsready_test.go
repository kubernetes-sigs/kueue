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
	"math"
	"reflect"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestSecondsToDuration(t *testing.T) {
	maxSafeSeconds := math.MaxInt64 / int64(time.Second) // 9223372036
	minSafeSeconds := math.MinInt64 / int64(time.Second) // -9223372036

	testCases := map[string]struct {
		seconds    int64
		wantDur    time.Duration
		wantErrMsg string
	}{
		"zero": {
			seconds: 0,
			wantDur: 0,
		},
		"positive": {
			seconds: 100,
			wantDur: 100 * time.Second,
		},
		"negative": {
			seconds: -100,
			wantDur: -100 * time.Second,
		},
		"max safe positive": {
			seconds: maxSafeSeconds,
			wantDur: time.Duration(maxSafeSeconds) * time.Second,
		},
		"min safe negative": {
			seconds: minSafeSeconds,
			wantDur: time.Duration(minSafeSeconds) * time.Second,
		},
		"overflow positive": {
			seconds:    maxSafeSeconds + 1,
			wantErrMsg: "overflows when converted to duration",
		},
		"overflow negative": {
			seconds:    minSafeSeconds - 1,
			wantErrMsg: "overflows when converted to duration",
		},
		"MaxInt64 overflows": {
			seconds:    math.MaxInt64,
			wantErrMsg: "overflows when converted to duration",
		},
		"MinInt64 overflows": {
			seconds:    math.MinInt64,
			wantErrMsg: "overflows when converted to duration",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotDur, err := secondsToDuration(tc.seconds)
			gotErrMsg := ""
			if err != nil {
				gotErrMsg = err.Error()
			}
			if (tc.wantErrMsg == "") != (err == nil) {
				t.Fatalf("secondsToDuration(%d) error = %v, wantErr = %q", tc.seconds, err, tc.wantErrMsg)
			}
			if tc.wantErrMsg != "" && gotErrMsg != tc.wantErrMsg {
				t.Errorf("secondsToDuration(%d) error = %q, want %q", tc.seconds, gotErrMsg, tc.wantErrMsg)
			}
			if err == nil && gotDur != tc.wantDur {
				t.Errorf("secondsToDuration(%d) = %v, want %v", tc.seconds, gotDur, tc.wantDur)
			}
		})
	}
}

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

func TestParseAnnotation(t *testing.T) {
	testCases := map[string]struct {
		annotation string
		want       *WorkloadLevelConfig
		wantErrMsg string
	}{
		"empty annotation": {
			annotation: "",
			want:       nil,
		},
		"valid annotation": {
			annotation: `{"timeoutSeconds":100}`,
			want: &WorkloadLevelConfig{
				Timeout: 100 * time.Second,
			},
		},
		"valid annotation with recoveryTimeoutSeconds": {
			annotation: `{"timeoutSeconds":100,"recoveryTimeoutSeconds":50}`,
			want: &WorkloadLevelConfig{
				Timeout:         100 * time.Second,
				RecoveryTimeout: ptr.To(50 * time.Second),
			},
		},
		"no timeoutSeconds": {
			annotation: `{"recoveryTimeoutSeconds":100}`,
			want: &WorkloadLevelConfig{
				RecoveryTimeout: ptr.To(100 * time.Second),
			},
		},
		// ParseAnnotation does not validate values; negative timeouts are
		// rejected by the webhook (ValidateWaitForPodsReadyAnnotation).
		"negative timeoutSeconds parses without error": {
			annotation: `{"timeoutSeconds":-1}`,
			want: &WorkloadLevelConfig{
				Timeout: -1 * time.Second,
			},
		},
		"unexpected end of JSON input": {
			annotation: `{"timeoutSeconds":`,
			wantErrMsg: "unexpected end of JSON input",
		},
		"wrong type for timeoutSeconds": {
			annotation: `{"timeoutSeconds":"foo"}`,
			wantErrMsg: "json: cannot unmarshal string into Go struct field .timeoutSeconds of type int64",
		},
		"invalid character in json": {
			annotation: `{timeoutSeconds:100,recoveryTimeoutSeconds:50}`,
			wantErrMsg: "invalid character 't' looking for beginning of object key string",
		},
		"timeoutSeconds overflow": {
			annotation: `{"timeoutSeconds":9223372037}`,
			wantErrMsg: "timeoutSeconds: value 9223372037 overflows when converted to duration",
		},
		"recoveryTimeoutSeconds overflow": {
			annotation: `{"timeoutSeconds":100,"recoveryTimeoutSeconds":-9223372037}`,
			wantErrMsg: "recoveryTimeoutSeconds: value -9223372037 overflows when converted to duration",
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.WorkloadLevelWaitForPodsReady: true,
			})
			got, err := ParseAnnotation(tc.annotation)
			gotErrMsg := ""
			if err != nil {
				gotErrMsg = err.Error()
			}
			if gotErrMsg != tc.wantErrMsg {
				t.Errorf("ParseAnnotation() error = %q, want %q", gotErrMsg, tc.wantErrMsg)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ParseAnnotation() = %v, want %v", got, tc.want)
			}
		})
	}
}
