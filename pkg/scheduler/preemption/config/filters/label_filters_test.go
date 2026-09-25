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

package filters

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestWorkloadLabelFilter_Matches(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)

	cases := map[string]struct {
		candidate *workload.Info
		selector  *metav1.LabelSelector
		wantMatch bool
	}{
		"matching labels returns true": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").Label("tier", "preemptible").Obj()),
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "preemptible"}},
			wantMatch: true,
		},
		"non-matching label value returns false": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").Label("tier", "guaranteed").Obj()),
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "preemptible"}},
			wantMatch: false,
		},
		"missing required label returns false": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").Label("other", "value").Obj()),
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "preemptible"}},
			wantMatch: false,
		},
		"workload with nil labels returns false": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").Obj()),
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"tier": "preemptible"}},
			wantMatch: false,
		},
		"matching matchExpressions returns true": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").Label("tier", "dev").Obj()),
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"dev", "test"}},
				},
			},
			wantMatch: true,
		},
		"non-matching matchExpressions returns false": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").Label("tier", "prod").Obj()),
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"dev", "test"}},
				},
			},
			wantMatch: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			filter, ok := buildWorkloadLabelFilter(log, tc.selector)
			if !ok || filter == nil {
				t.Fatalf("buildWorkloadLabelFilter failed unexpectedly")
			}
			if got := filter.Matches(tc.candidate); got != tc.wantMatch {
				t.Errorf("Matches(candidate) = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}
