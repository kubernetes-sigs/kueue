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

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
)

func TestClusterQueueLabelFilter_Matches(t *testing.T) {
	cases := map[string]struct {
		cq        *schdcache.ClusterQueueSnapshot
		selector  *metav1.LabelSelector
		wantMatch bool
	}{
		"matching labels returns true": {
			cq: &schdcache.ClusterQueueSnapshot{
				Name:   "cq1",
				Labels: map[string]string{"env": "prod", "team": "batch"},
			},
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
			wantMatch: true,
		},
		"non-matching label value returns false": {
			cq: &schdcache.ClusterQueueSnapshot{
				Name:   "cq1",
				Labels: map[string]string{"env": "dev"},
			},
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
			wantMatch: false,
		},
		"missing required label returns false": {
			cq: &schdcache.ClusterQueueSnapshot{
				Name:   "cq1",
				Labels: map[string]string{"team": "batch"},
			},
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
			wantMatch: false,
		},
		"ClusterQueue with nil labels returns false": {
			cq: &schdcache.ClusterQueueSnapshot{
				Name: "cq1",
			},
			selector:  &metav1.LabelSelector{MatchLabels: map[string]string{"env": "prod"}},
			wantMatch: false,
		},
		"matching matchExpressions returns true": {
			cq: &schdcache.ClusterQueueSnapshot{
				Name:   "cq1",
				Labels: map[string]string{"tier": "batch"},
			},
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"batch", "ml"}},
				},
			},
			wantMatch: true,
		},
		"non-matching matchExpressions returns false": {
			cq: &schdcache.ClusterQueueSnapshot{
				Name:   "cq1",
				Labels: map[string]string{"tier": "general"},
			},
			selector: &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{Key: "tier", Operator: metav1.LabelSelectorOpIn, Values: []string{"batch", "ml"}},
				},
			},
			wantMatch: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			filter, ok := buildClusterQueueLabelFilter(logr.Discard(), tc.selector)
			if !ok {
				t.Fatalf("buildClusterQueueLabelFilter failed unexpectedly")
			}
			if got := filter.Matches(tc.cq); got != tc.wantMatch {
				t.Errorf("Matches(cq) = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}
