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

package flavorassigner

import (
	"testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestSearch(t *testing.T) {
	cases := map[string]struct {
		podSets     []kueue.PodSet
		lowerBounds []int32
		countLimit  int32
		wantCount   int32
		wantFound   bool
	}{
		"empty": {
			podSets:    []kueue.PodSet{},
			countLimit: 10,
			wantFound:  false,
			wantCount:  0,
		},
		"partial not available": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 1).Obj(),
				*utiltestingapi.MakePodSet("ps2", 2).SetMinimumCount(2).Obj(),
			},
			countLimit: 2,
			wantFound:  false,
			wantCount:  0,
		},
		"lowerBounds raise the floor above minCount (resize scale-up)": {
			// count=10, minCount=2, but lowerBound=admitted(5)+1=6 -> search [6,10].
			// countLimit=8 -> largest fitting count in [6,10] is 8.
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(2).Obj(),
			},
			lowerBounds: []int32{6},
			countLimit:  8,
			wantFound:   true,
			wantCount:   8,
		},
		"lowerBounds: nothing above admitted fits -> not found": {
			// search [6,10] but only <=5 would fit -> no candidate grows past admitted.
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps", 10).SetMinimumCount(2).Obj(),
			},
			lowerBounds: []int32{6},
			countLimit:  5,
			wantFound:   false,
			wantCount:   0,
		},
		"partial available": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltestingapi.MakePodSet("ps2", 5).SetMinimumCount(4).Obj(),
				*utiltestingapi.MakePodSet("ps3", 5).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps4", 5).SetMinimumCount(2).Obj(),
			},
			countLimit: 15,
			wantFound:  true,
			wantCount:  15,
		},
		"one partial available": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltestingapi.MakePodSet("ps2", 5).Obj(),
				*utiltestingapi.MakePodSet("ps3", 5).Obj(),
				*utiltestingapi.MakePodSet("ps4", 5).Obj(),
			},
			countLimit: 19,
			wantFound:  true,
			wantCount:  19,
		},
		"to min": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltestingapi.MakePodSet("ps2", 5).SetMinimumCount(4).Obj(),
				*utiltestingapi.MakePodSet("ps3", 5).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps4", 5).SetMinimumCount(2).Obj(),
			},
			countLimit: 10,
			wantFound:  true,
			wantCount:  10,
		},
		"to max": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltestingapi.MakePodSet("ps2", 5).SetMinimumCount(4).Obj(),
				*utiltestingapi.MakePodSet("ps3", 5).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps4", 5).SetMinimumCount(2).Obj(),
			},
			countLimit: 20,
			wantFound:  true,
			wantCount:  20,
		},
		"no overflow": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps2", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps3", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps4", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps5", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps6", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps7", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps8", 150_000).SetMinimumCount(1).Obj(),
			},
			countLimit: 150_000,
			wantFound:  true,
			wantCount:  150_000,
		},
		"max pods on 1.27": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 150_000).SetMinimumCount(1).Obj(),
				*utiltestingapi.MakePodSet("ps2", 1).Obj(),
				*utiltestingapi.MakePodSet("ps3", 1).Obj(),
				*utiltestingapi.MakePodSet("ps4", 1).Obj(),
				*utiltestingapi.MakePodSet("ps5", 1).Obj(),
				*utiltestingapi.MakePodSet("ps6", 1).Obj(),
				*utiltestingapi.MakePodSet("ps7", 1).Obj(),
				*utiltestingapi.MakePodSet("ps8", 1).Obj(),
			},
			countLimit: 150_000,
			wantFound:  true,
			wantCount:  150_000,
		},
		"podset with replica count 0": {
			podSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("ps1", 0).SetMinimumCount(0).Obj(),
			},
			countLimit: 0,
			wantFound:  false,
			wantCount:  0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			red := NewPodSetReducer(tc.podSets, tc.lowerBounds, func(counts []int32) (int32, bool) {
				total := int32(0)
				for _, v := range counts {
					total += v
				}
				return total, total <= tc.countLimit
			})
			count, found := red.Search()
			if count != tc.wantCount {
				t.Errorf("Unexpected count:%d, want: %d", count, tc.wantCount)
			}

			if found != tc.wantFound {
				t.Errorf("Unexpected found:%v, want: %v", found, tc.wantFound)
			}
		})
	}
}
