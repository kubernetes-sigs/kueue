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
	"encoding/json"
	"time"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func Enabled(cfg *configapi.WaitForPodsReady) bool {
	if features.Enabled(features.DisableWaitForPodsReady) {
		return false
	}
	return cfg != nil
}

func PodsScheduledTrackingEnabled(cfg *configapi.WaitForPodsReady) bool {
	return cfg != nil && features.Enabled(features.WaitForPodsReadyUnscheduledTimeout) &&
		cfg.UnscheduledTimeout != nil && cfg.UnscheduledTimeout.Duration > 0
}

// AnnotationConfig holds the parsed content of the WaitForPodsReadyAnnotation,
// with integer seconds already converted to time.Duration.
type AnnotationConfig struct {
	Timeout         time.Duration
	RecoveryTimeout *time.Duration
}

// ParseAnnotation parses the JSON value of the WaitForPodsReadyAnnotation into
// an AnnotationConfig. Returns nil, nil when the annotation value is empty.
func ParseAnnotation(value string) (*AnnotationConfig, error) {
	if !WorkloadLevelWaitForPodsReadyEnabled() {
		return nil, nil
	}
	if value == "" {
		return nil, nil
	}
	var raw struct {
		TimeoutSeconds         int64  `json:"timeoutSeconds"`
		RecoveryTimeoutSeconds *int64 `json:"recoveryTimeoutSeconds,omitempty"`
	}
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}
	cfg := &AnnotationConfig{
		Timeout: time.Duration(raw.TimeoutSeconds) * time.Second,
	}
	// Mirror the cluster-wide recoveryTimeout semantics: only set when strictly
	// positive.
	if raw.RecoveryTimeoutSeconds != nil {
		rt := time.Duration(*raw.RecoveryTimeoutSeconds) * time.Second
		cfg.RecoveryTimeout = &rt
	}
	return cfg, nil
}

func WorkloadLevelWaitForPodsReadyEnabled() bool {
	return features.Enabled(features.WorkloadLevelWaitForPodsReady) &&
		!features.Enabled(features.DisableWaitForPodsReady)
}
