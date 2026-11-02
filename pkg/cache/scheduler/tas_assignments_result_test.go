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

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestTASAssignmentsResultFailure(t *testing.T) {
	cases := map[string]struct {
		result TASAssignmentsResult
		want   *FailureInfo
	}{
		"no PodSet failed": {
			result: TASAssignmentsResult{
				"leader":  {Flavor: "tas"},
				"workers": {Flavor: "tas"},
			},
		},
		"leader and workers fail with the same reason": {
			result: TASAssignmentsResult{
				"workers": {FailureReason: "does not fit", Flavor: "tas"},
				"leader":  {FailureReason: "does not fit", Flavor: "tas"},
			},
			want: &FailureInfo{PodSetName: "leader", Reason: "does not fit", Flavor: "tas"},
		},
		"PodSets fail in different flavors": {
			result: TASAssignmentsResult{
				"ps-c": {FailureReason: "no fit in tas-a", Flavor: "tas-a"},
				"ps-b": {FailureReason: "no fit in tas-b", Flavor: "tas-b"},
				"ps-a": {Flavor: "tas-a"},
			},
			want: &FailureInfo{PodSetName: "ps-b", Reason: "no fit in tas-b", Flavor: "tas-b"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Map iteration order is random, so ask several times to catch an unstable pick.
			for range 20 {
				if diff := cmp.Diff(tc.want, tc.result.Failure()); diff != "" {
					t.Fatalf("Failure() (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
