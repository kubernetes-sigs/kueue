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

	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestMatchesComparison(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)

	unsupported := kueuealpha.NumericComparison("Unsupported")

	cases := map[string]struct {
		comparison   *kueuealpha.NumericComparison
		candidateVal int64
		preemptorVal int64
		wantMatch    bool
	}{
		"nil comparison returns true": {
			comparison:   nil,
			candidateVal: 10,
			preemptorVal: 5,
			wantMatch:    true,
		},
		"LessThan: strictly less matches": {
			comparison:   ptr.To(kueuealpha.LessThan),
			candidateVal: 5,
			preemptorVal: 10,
			wantMatch:    true,
		},
		"LessThan: equal rejected": {
			comparison:   ptr.To(kueuealpha.LessThan),
			candidateVal: 10,
			preemptorVal: 10,
			wantMatch:    false,
		},
		"LessThan: greater rejected": {
			comparison:   ptr.To(kueuealpha.LessThan),
			candidateVal: 15,
			preemptorVal: 10,
			wantMatch:    false,
		},
		"LessThanOrEqual: strictly less matches": {
			comparison:   ptr.To(kueuealpha.LessThanOrEqual),
			candidateVal: 5,
			preemptorVal: 10,
			wantMatch:    true,
		},
		"LessThanOrEqual: equal matches": {
			comparison:   ptr.To(kueuealpha.LessThanOrEqual),
			candidateVal: 10,
			preemptorVal: 10,
			wantMatch:    true,
		},
		"LessThanOrEqual: greater rejected": {
			comparison:   ptr.To(kueuealpha.LessThanOrEqual),
			candidateVal: 15,
			preemptorVal: 10,
			wantMatch:    false,
		},
		"GreaterThan: strictly greater matches": {
			comparison:   ptr.To(kueuealpha.GreaterThan),
			candidateVal: 15,
			preemptorVal: 10,
			wantMatch:    true,
		},
		"GreaterThan: equal rejected": {
			comparison:   ptr.To(kueuealpha.GreaterThan),
			candidateVal: 10,
			preemptorVal: 10,
			wantMatch:    false,
		},
		"GreaterThan: less rejected": {
			comparison:   ptr.To(kueuealpha.GreaterThan),
			candidateVal: 5,
			preemptorVal: 10,
			wantMatch:    false,
		},
		"GreaterThanOrEqual: strictly greater matches": {
			comparison:   ptr.To(kueuealpha.GreaterThanOrEqual),
			candidateVal: 15,
			preemptorVal: 10,
			wantMatch:    true,
		},
		"GreaterThanOrEqual: equal matches": {
			comparison:   ptr.To(kueuealpha.GreaterThanOrEqual),
			candidateVal: 10,
			preemptorVal: 10,
			wantMatch:    true,
		},
		"GreaterThanOrEqual: less rejected": {
			comparison:   ptr.To(kueuealpha.GreaterThanOrEqual),
			candidateVal: 5,
			preemptorVal: 10,
			wantMatch:    false,
		},
		"unsupported comparison returns false": {
			comparison:   &unsupported,
			candidateVal: 10,
			preemptorVal: 10,
			wantMatch:    false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := matchesComparison(log, tc.comparison, tc.candidateVal, tc.preemptorVal)
			if got != tc.wantMatch {
				t.Errorf("matchesComparison() = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}
