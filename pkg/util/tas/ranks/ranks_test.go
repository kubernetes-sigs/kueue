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

package ranks

import (
	"maps"
	"testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestPodSetRankOffsets(t *testing.T) {
	makePsa := func(name kueue.PodSetReference, count int32) kueue.PodSetAssignment {
		return kueue.PodSetAssignment{Name: name, Count: new(count)}
	}
	groupedRequest := func(group string) *kueue.PodSetTopologyRequest {
		return &kueue.PodSetTopologyRequest{PodSetGroupName: new(group)}
	}
	cases := map[string]struct {
		psas                    []kueue.PodSetAssignment
		psNameToTopologyRequest map[kueue.PodSetReference]*kueue.PodSetTopologyRequest
		want                    map[kueue.PodSetReference]int32
	}{
		"single PodSet without a group": {
			psas: []kueue.PodSetAssignment{makePsa("main", 4)},
			want: map[kueue.PodSetReference]int32{"main": 0},
		},
		"multiple PodSets without groups get independent zero offsets": {
			psas: []kueue.PodSetAssignment{makePsa("driver", 1), makePsa("executor", 4)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"driver":   {},
				"executor": {},
			},
			want: map[kueue.PodSetReference]int32{"driver": 0, "executor": 0},
		},
		"grouped leader and workers: workers offset by leader count": {
			psas: []kueue.PodSetAssignment{makePsa("leader", 1), makePsa("workers", 4)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"leader":  groupedRequest("lws-group"),
				"workers": groupedRequest("lws-group"),
			},
			want: map[kueue.PodSetReference]int32{"leader": 0, "workers": 1},
		},
		"grouped PodSets listed larger-first: smaller still gets offset 0": {
			psas: []kueue.PodSetAssignment{makePsa("workers", 4), makePsa("leader", 1)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"leader":  groupedRequest("lws-group"),
				"workers": groupedRequest("lws-group"),
			},
			want: map[kueue.PodSetReference]int32{"leader": 0, "workers": 1},
		},
		"grouped pair next to an independent PodSet": {
			psas: []kueue.PodSetAssignment{makePsa("leader", 1), makePsa("workers", 4), makePsa("main", 2)},
			psNameToTopologyRequest: map[kueue.PodSetReference]*kueue.PodSetTopologyRequest{
				"leader":  groupedRequest("lws-group"),
				"workers": groupedRequest("lws-group"),
				"main":    {},
			},
			want: map[kueue.PodSetReference]int32{"leader": 0, "workers": 1, "main": 0},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := PodSetRankOffsets(tc.psas, tc.psNameToTopologyRequest); !maps.Equal(got, tc.want) {
				t.Errorf("PodSetRankOffsets() = %v, want %v", got, tc.want)
			}
		})
	}
}
