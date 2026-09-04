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

package core

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

func TestWaitForPodsReadyUnscheduledTimeoutZeroDisabled(t *testing.T) {
	t.Parallel()
	cfg := waitForPodsReady(&configapi.WaitForPodsReady{
		Timeout: metav1.Duration{Duration: 5 * time.Minute},
		UnscheduledTimeout: &metav1.Duration{
			Duration: 0,
		},
	})
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.unscheduledTimeout != nil {
		t.Fatalf("unscheduledTimeout = %v, want nil for 0s", *cfg.unscheduledTimeout)
	}
}

func TestWaitForPodsReadyUnscheduledTimeoutPositiveEnabled(t *testing.T) {
	t.Parallel()
	cfg := waitForPodsReady(&configapi.WaitForPodsReady{
		Timeout: metav1.Duration{Duration: 5 * time.Minute},
		UnscheduledTimeout: &metav1.Duration{
			Duration: 2 * time.Minute,
		},
	})
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.unscheduledTimeout == nil || *cfg.unscheduledTimeout != 2*time.Minute {
		t.Fatalf("unscheduledTimeout = %v, want 2m", cfg.unscheduledTimeout)
	}
}
