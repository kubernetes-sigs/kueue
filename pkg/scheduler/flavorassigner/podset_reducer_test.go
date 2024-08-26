/*
Copyright 2024 The Kubernetes Authors.

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestSearch(t *testing.T) {
	cases := map[string]struct {
		podSets    []kueue.PodSet
		countLimit int32
		wantCount  int32
		wantFound  bool
	}{
		"empty": {
			podSets:    []kueue.PodSet{},
			countLimit: 10,
			wantFound:  false,
			wantCount:  0,
		},
		"partial not available": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 1).Obj(),
				*utiltesting.MakePodSet("ps2", 2).SetMinimumCount(2).Obj(),
			},
			countLimit: 2,
			wantFound:  false,
			wantCount:  0,
		},
		"partial available": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltesting.MakePodSet("ps2", 5).SetMinimumCount(4).Obj(),
				*utiltesting.MakePodSet("ps3", 5).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps4", 5).SetMinimumCount(2).Obj(),
			},
			countLimit: 15,
			wantFound:  true,
			wantCount:  15,
		},
		"one partial available": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltesting.MakePodSet("ps2", 5).Obj(),
				*utiltesting.MakePodSet("ps3", 5).Obj(),
				*utiltesting.MakePodSet("ps4", 5).Obj(),
			},
			countLimit: 19,
			wantFound:  true,
			wantCount:  19,
		},
		"to min": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltesting.MakePodSet("ps2", 5).SetMinimumCount(4).Obj(),
				*utiltesting.MakePodSet("ps3", 5).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps4", 5).SetMinimumCount(2).Obj(),
			},
			countLimit: 10,
			wantFound:  true,
			wantCount:  10,
		},
		"to max": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 5).SetMinimumCount(3).Obj(),
				*utiltesting.MakePodSet("ps2", 5).SetMinimumCount(4).Obj(),
				*utiltesting.MakePodSet("ps3", 5).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps4", 5).SetMinimumCount(2).Obj(),
			},
			countLimit: 20,
			wantFound:  true,
			wantCount:  20,
		},
		"no overflow": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps2", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps3", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps4", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps5", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps6", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps7", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps8", 150_000).SetMinimumCount(1).Obj(),
			},
			countLimit: 150_000,
			wantFound:  true,
			wantCount:  150_000,
		},
		"max pods on 1.27": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 150_000).SetMinimumCount(1).Obj(),
				*utiltesting.MakePodSet("ps2", 1).Obj(),
				*utiltesting.MakePodSet("ps3", 1).Obj(),
				*utiltesting.MakePodSet("ps4", 1).Obj(),
				*utiltesting.MakePodSet("ps5", 1).Obj(),
				*utiltesting.MakePodSet("ps6", 1).Obj(),
				*utiltesting.MakePodSet("ps7", 1).Obj(),
				*utiltesting.MakePodSet("ps8", 1).Obj(),
			},
			countLimit: 150_000,
			wantFound:  true,
			wantCount:  150_000,
		},
		"podset with replica count 0": {
			podSets: []kueue.PodSet{
				*utiltesting.MakePodSet("ps1", 0).SetMinimumCount(0).Obj(),
			},
			countLimit: 0,
			wantFound:  false,
			wantCount:  0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			red := NewPodSetReducer(tc.podSets, func(counts []int32) (int32, bool) {
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
