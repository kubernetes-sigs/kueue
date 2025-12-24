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

package podset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestFindPodSetByName(t *testing.T) {
	testCases := map[string]struct {
		podSets    []kueue.PodSet
		targetName kueue.PodSetReference
		want       *kueue.PodSet
	}{
		"empty podSets": {
			podSets:    []kueue.PodSet{},
			targetName: "main",
			want:       nil,
		},
		"single podSet match": {
			podSets: []kueue.PodSet{
				{Name: "main", Count: 1},
			},
			targetName: "main",
			want:       &kueue.PodSet{Name: "main", Count: 1},
		},
		"multiple podSets, match first": {
			podSets: []kueue.PodSet{
				{Name: "main", Count: 1},
				{Name: "worker", Count: 5},
			},
			targetName: "main",
			want:       &kueue.PodSet{Name: "main", Count: 1},
		},
		"multiple podSets, match second": {
			podSets: []kueue.PodSet{
				{Name: "main", Count: 1},
				{Name: "worker", Count: 5},
			},
			targetName: "worker",
			want:       &kueue.PodSet{Name: "worker", Count: 5},
		},
		"no match": {
			podSets: []kueue.PodSet{
				{Name: "main", Count: 1},
				{Name: "worker", Count: 5},
			},
			targetName: "launcher",
			want:       nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := FindPodSetByName(tc.podSets, tc.targetName)

			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("FindPodSetByName() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
