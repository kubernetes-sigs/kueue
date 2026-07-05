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

package common

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestWithinProtectionWindow(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	minAdmitDuration := 10 * time.Minute
	rule := &config.PreemptionProtectionPolicy{
		MinAdmitDuration: &metav1.Duration{Duration: minAdmitDuration},
	}

	admittedCondAt := func(status metav1.ConditionStatus, at time.Time) metav1.Condition {
		return metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             status,
			LastTransitionTime: metav1.NewTime(at),
			Reason:             "ByTest",
		}
	}
	evictedCondAt := func(at time.Time) metav1.Condition {
		return metav1.Condition{
			Type:               kueue.WorkloadEvicted,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(at),
			Reason:             "ByTest",
		}
	}

	testCases := map[string]struct {
		candidate   *kueue.Workload
		rule        *config.PreemptionProtectionPolicy
		gateEnabled bool
		want        bool
	}{
		"nil rule": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionTrue, now.Add(-time.Minute))).
				Obj(),
			rule:        nil,
			gateEnabled: true,
			want:        false,
		},
		"rule with nil minAdmitDuration": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionTrue, now.Add(-time.Minute))).
				Obj(),
			rule:        &config.PreemptionProtectionPolicy{},
			gateEnabled: true,
			want:        false,
		},
		"feature gate disabled": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionTrue, now.Add(-time.Minute))).
				Obj(),
			rule:        rule,
			gateEnabled: false,
			want:        false,
		},
		"no Admitted condition": {
			candidate:   utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).Obj(),
			rule:        rule,
			gateEnabled: true,
			want:        false,
		},
		"Admitted condition is False": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionFalse, now.Add(-time.Minute))).
				Obj(),
			rule:        rule,
			gateEnabled: true,
			want:        false,
		},
		"evicted candidate is never protected": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(
					admittedCondAt(metav1.ConditionTrue, now.Add(-time.Minute)),
					evictedCondAt(now),
				).
				Obj(),
			rule:        rule,
			gateEnabled: true,
			want:        false,
		},
		"runtime strictly less than minAdmitDuration is protected": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionTrue, now.Add(-minAdmitDuration+time.Second))).
				Obj(),
			rule:        rule,
			gateEnabled: true,
			want:        true,
		},
		"runtime exactly equal to minAdmitDuration is eligible": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionTrue, now.Add(-minAdmitDuration))).
				Obj(),
			rule:        rule,
			gateEnabled: true,
			want:        false,
		},
		"runtime greater than minAdmitDuration is eligible": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(admittedCondAt(metav1.ConditionTrue, now.Add(-minAdmitDuration-time.Hour))).
				Obj(),
			rule:        rule,
			gateEnabled: true,
			want:        false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.PreemptionProtection, tc.gateEnabled)
			if got := WithinProtectionWindow(tc.candidate, tc.rule, now); got != tc.want {
				t.Errorf("WithinProtectionWindow() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProtectionExpiry(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	admittedAt := now.Add(-3 * time.Minute)
	minAdmitDuration := 10 * time.Minute
	rule := &config.PreemptionProtectionPolicy{
		MinAdmitDuration: &metav1.Duration{Duration: minAdmitDuration},
	}

	testCases := map[string]struct {
		candidate *kueue.Workload
		rule      *config.PreemptionProtectionPolicy
		want      time.Time
	}{
		"admitted candidate expires at admitted time plus duration": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).
				Conditions(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(admittedAt),
					Reason:             "ByTest",
				}).
				Obj(),
			rule: rule,
			want: admittedAt.Add(minAdmitDuration),
		},
		"nil rule yields zero time": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).Obj(),
			rule:      nil,
			want:      time.Time{},
		},
		"no Admitted condition yields zero time": {
			candidate: utiltestingapi.MakeWorkload("wl", metav1.NamespaceDefault).Obj(),
			rule:      rule,
			want:      time.Time{},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if got := ProtectionExpiry(tc.candidate, tc.rule); !got.Equal(tc.want) {
				t.Errorf("ProtectionExpiry() = %v, want %v", got, tc.want)
			}
		})
	}
}
