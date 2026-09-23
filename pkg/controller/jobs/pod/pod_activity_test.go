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

package pod

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestPodActivity(t *testing.T) {
	now := time.Date(2026, time.October, 16, 12, 0, 0, 0, time.UTC)
	running := testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodRunning)
	cases := map[string]struct {
		pod              *corev1.Pod
		fastQuotaRelease bool
		wantActive       bool
		wantRecheckAfter time.Duration
	}{
		"pending pod is inactive": {
			pod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
		},
		"finished pod is inactive": {
			pod: running.Clone().StatusPhase(corev1.PodSucceeded).Obj(),
		},
		"running pod waits for an event": {
			pod: running.Clone().Obj(), wantActive: true,
		},
		"missing grace period waits for an event": {
			pod: running.Clone().DeletionTimestamp(now.Add(-time.Minute)).Obj(), wantActive: true,
		},
		"terminating pod rechecks after the API deadline": {
			pod:        running.Clone().DeletionTimestamp(now.Add(30 * time.Second)).DeletionGracePeriod(90).Obj(),
			wantActive: true, wantRecheckAfter: 30*time.Second + time.Nanosecond,
		},
		"deadline is inclusive": {
			pod:        running.Clone().DeletionTimestamp(now).DeletionGracePeriod(90).Obj(),
			wantActive: true, wantRecheckAfter: time.Nanosecond,
		},
		"past deadline does not add the grace period again": {
			pod: running.Clone().DeletionTimestamp(now.Add(-time.Second)).DeletionGracePeriod(90).Obj(),
		},
		"fast release ignores the remaining deadline": {
			pod: running.Clone().DeletionTimestamp(now.Add(time.Minute)).DeletionGracePeriod(90).Obj(), fastQuotaRelease: true,
		},
		"fast release also handles a missing grace period": {
			pod: running.Clone().DeletionTimestamp(now.Add(time.Minute)).Obj(), fastQuotaRelease: true,
		},
		"fast release does not affect a non-deleting pod": {
			pod: running.Clone().Obj(), fastQuotaRelease: true, wantActive: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FastQuotaReleaseInPodIntegration, tc.fastQuotaRelease)
			active, after := PodActivity(tc.pod, now)
			if active != tc.wantActive || after != tc.wantRecheckAfter {
				t.Errorf("PodActivity() = (%v, %v), want (%v, %v)", active, after, tc.wantActive, tc.wantRecheckAfter)
			}
		})
	}
}
