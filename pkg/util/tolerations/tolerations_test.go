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

package tolerations

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
)

func TestEqual(t *testing.T) {
	withEqualOperator := corev1.Toleration{
		Key:      "t0",
		Operator: corev1.TolerationOpEqual,
		Value:    "t0v",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	withoutOperator := corev1.Toleration{
		Key:    "t0",
		Value:  "t0v",
		Effect: corev1.TaintEffectNoSchedule,
	}
	differentKey := corev1.Toleration{
		Key:      "t1",
		Operator: corev1.TolerationOpEqual,
		Value:    "t0v",
		Effect:   corev1.TaintEffectNoSchedule,
	}

	cases := map[string]struct {
		a, b   corev1.Toleration
		wantEq bool
	}{
		"empty vs Equal operator": {
			a:      withEqualOperator,
			b:      withoutOperator,
			wantEq: true,
		},
		"Equal vs empty operator": {
			a:      withoutOperator,
			b:      withEqualOperator,
			wantEq: true,
		},
		"same toleration (empty)": {
			a:      withoutOperator,
			b:      withoutOperator,
			wantEq: true,
		},
		"same toleration (not empty)": {
			a:      withEqualOperator,
			b:      withEqualOperator,
			wantEq: true,
		},
		"different tolerations": {
			a:      withEqualOperator,
			b:      differentKey,
			wantEq: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Equal(tc.a, tc.b); got != tc.wantEq {
				t.Errorf("Equal() = %v, want %v", got, tc.wantEq)
			}
		})
	}
}

// TestMerge verifies order preservation, duplicate handling, and input ownership.
func TestMerge(t *testing.T) {
	base := []corev1.Toleration{
		{
			Key:      "base",
			Operator: corev1.TolerationOpEqual,
			Value:    "value",
			Effect:   corev1.TaintEffectNoSchedule,
		},
	}
	baseWithoutOperator := []corev1.Toleration{
		{
			Key:    "same",
			Value:  "value",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	cases := map[string]struct {
		base, extra []corev1.Toleration
		want        []corev1.Toleration
	}{
		"empty": {},
		"append unique tolerations": {
			base: base,
			extra: []corev1.Toleration{
				{Key: "second", Effect: corev1.TaintEffectNoExecute},
				{Key: "third", Effect: corev1.TaintEffectNoSchedule},
			},
			want: append(append([]corev1.Toleration{}, base...),
				corev1.Toleration{Key: "second", Effect: corev1.TaintEffectNoExecute},
				corev1.Toleration{Key: "third", Effect: corev1.TaintEffectNoSchedule}),
		},
		"keep the base value for duplicates": {
			base: []corev1.Toleration{{
				Key:               "base",
				Operator:          corev1.TolerationOpEqual,
				Value:             "value",
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: new(int64(10)),
			}},
			extra: []corev1.Toleration{
				{Key: "base", Value: "value", Effect: corev1.TaintEffectNoExecute, TolerationSeconds: new(int64(20))},
			},
			want: []corev1.Toleration{{
				Key:               "base",
				Operator:          corev1.TolerationOpEqual,
				Value:             "value",
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: new(int64(10)),
			}},
		},
		"treat empty and Equal operators as duplicates": {
			base: baseWithoutOperator,
			extra: []corev1.Toleration{
				{Key: "same", Operator: corev1.TolerationOpEqual, Value: "value", Effect: corev1.TaintEffectNoSchedule},
			},
			want: baseWithoutOperator,
		},
		"ignore TolerationSeconds when checking identity": {
			base: []corev1.Toleration{{
				Key:               "seconds",
				Operator:          corev1.TolerationOpEqual,
				Value:             "value",
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: new(int64(10)),
			}},
			extra: []corev1.Toleration{{
				Key:               "seconds",
				Operator:          corev1.TolerationOpEqual,
				Value:             "value",
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: new(int64(20)),
			}},
			want: []corev1.Toleration{{
				Key:               "seconds",
				Operator:          corev1.TolerationOpEqual,
				Value:             "value",
				Effect:            corev1.TaintEffectNoExecute,
				TolerationSeconds: new(int64(10)),
			}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			baseCopy := append([]corev1.Toleration(nil), tc.base...)
			got := Merge(tc.base, tc.extra)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Merge() mismatch (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(baseCopy, tc.base); diff != "" {
				t.Errorf("Merge() modified base (-want,+got):\n%s", diff)
			}
		})
	}
}
