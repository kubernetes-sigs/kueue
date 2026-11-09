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

func TestContainsAll(t *testing.T) {
	first := corev1.Toleration{Key: "first", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	second := corev1.Toleration{Key: "second", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: new(int64(10))}

	cases := map[string]struct {
		tolerations []corev1.Toleration
		subset      []corev1.Toleration
		want        bool
	}{
		"empty": {
			tolerations: nil,
			subset:      nil,
			want:        true,
		},
		"empty subset": {
			tolerations: []corev1.Toleration{first},
			subset:      nil,
			want:        true,
		},
		"equal": {
			tolerations: []corev1.Toleration{first, second},
			subset:      []corev1.Toleration{first, second},
			want:        true,
		},
		"added toleration in a different order": {
			tolerations: []corev1.Toleration{second, first},
			subset:      []corev1.Toleration{first},
			want:        true,
		},
		"removed toleration": {
			tolerations: []corev1.Toleration{first},
			subset:      []corev1.Toleration{first, second},
			want:        false,
		},
		"changed TolerationSeconds": {
			tolerations: []corev1.Toleration{first, {Key: "second", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoExecute, TolerationSeconds: new(int64(20))}},
			subset:      []corev1.Toleration{first, second},
			want:        false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ContainsAll(tc.tolerations, tc.subset); got != tc.want {
				t.Errorf("ContainsAll() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCopyIfOnlyAdded(t *testing.T) {
	first := corev1.Toleration{Key: "first", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	second := corev1.Toleration{Key: "second", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}

	cases := map[string]struct {
		dst         []corev1.Toleration
		src         []corev1.Toleration
		wantDst     []corev1.Toleration
		wantChanged bool
	}{
		"equal": {
			dst:         []corev1.Toleration{first},
			src:         []corev1.Toleration{first},
			wantDst:     []corev1.Toleration{first},
			wantChanged: false,
		},
		"added toleration": {
			dst:         []corev1.Toleration{first},
			src:         []corev1.Toleration{second, first},
			wantDst:     []corev1.Toleration{second, first},
			wantChanged: true,
		},
		"added toleration to empty": {
			dst:         nil,
			src:         []corev1.Toleration{first},
			wantDst:     []corev1.Toleration{first},
			wantChanged: true,
		},
		"removed toleration": {
			dst:         []corev1.Toleration{first, second},
			src:         []corev1.Toleration{first},
			wantDst:     []corev1.Toleration{first, second},
			wantChanged: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotChanged := copyIfOnlyAdded(&tc.dst, tc.src)
			if gotChanged != tc.wantChanged {
				t.Errorf("copyIfOnlyAdded() = %v, want %v", gotChanged, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.wantDst, tc.dst); diff != "" {
				t.Errorf("copyIfOnlyAdded() dst mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCopyAddedByName(t *testing.T) {
	first := corev1.Toleration{Key: "first", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	second := corev1.Toleration{Key: "second", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule}
	template := func(tolerations ...corev1.Toleration) *corev1.PodTemplateSpec {
		return &corev1.PodTemplateSpec{Spec: corev1.PodSpec{Tolerations: tolerations}}
	}

	cases := map[string]struct {
		dst         map[string]*corev1.PodTemplateSpec
		src         map[string]*corev1.PodTemplateSpec
		wantDst     map[string]*corev1.PodTemplateSpec
		wantUpdated bool
	}{
		"equal": {
			dst:         map[string]*corev1.PodTemplateSpec{"a": template(first)},
			src:         map[string]*corev1.PodTemplateSpec{"a": template(first)},
			wantDst:     map[string]*corev1.PodTemplateSpec{"a": template(first)},
			wantUpdated: false,
		},
		"added toleration to one template": {
			dst:         map[string]*corev1.PodTemplateSpec{"a": template(first), "b": template(first)},
			src:         map[string]*corev1.PodTemplateSpec{"a": template(first, second), "b": template(first)},
			wantDst:     map[string]*corev1.PodTemplateSpec{"a": template(first, second), "b": template(first)},
			wantUpdated: true,
		},
		"removed toleration": {
			dst:         map[string]*corev1.PodTemplateSpec{"a": template(first, second)},
			src:         map[string]*corev1.PodTemplateSpec{"a": template(first)},
			wantDst:     map[string]*corev1.PodTemplateSpec{"a": template(first, second)},
			wantUpdated: false,
		},
		"no template with the same name": {
			dst:         map[string]*corev1.PodTemplateSpec{"a": template(first)},
			src:         map[string]*corev1.PodTemplateSpec{"b": template(first, second)},
			wantDst:     map[string]*corev1.PodTemplateSpec{"a": template(first)},
			wantUpdated: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotUpdated := CopyAddedByName(tc.dst, tc.src)
			if gotUpdated != tc.wantUpdated {
				t.Errorf("CopyAddedByName() = %v, want %v", gotUpdated, tc.wantUpdated)
			}
			if diff := cmp.Diff(tc.wantDst, tc.dst); diff != "" {
				t.Errorf("CopyAddedByName() dst mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
