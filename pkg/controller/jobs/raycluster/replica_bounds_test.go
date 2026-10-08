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

package raycluster

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestWorkerPodCountsRespectReplicaBounds(t *testing.T) {
	cases := map[string]struct {
		group     rayv1.WorkerGroupSpec
		wantCount int32
	}{
		"allocation timeout lowers replicas below minimum": {
			group:     rayv1.WorkerGroupSpec{Replicas: new(int32(1)), MinReplicas: new(int32(2)), MaxReplicas: new(int32(2))},
			wantCount: 2,
		},
		"replicas above maximum": {
			group:     rayv1.WorkerGroupSpec{Replicas: new(int32(4)), MinReplicas: new(int32(1)), MaxReplicas: new(int32(2))},
			wantCount: 2,
		},
		"replicas within bounds": {
			group:     rayv1.WorkerGroupSpec{Replicas: new(int32(3)), MinReplicas: new(int32(2)), MaxReplicas: new(int32(4))},
			wantCount: 3,
		},
		"multi-host replicas below minimum": {
			group:     rayv1.WorkerGroupSpec{Replicas: new(int32(1)), MinReplicas: new(int32(2)), MaxReplicas: new(int32(3)), NumOfHosts: 4},
			wantCount: 8,
		},
		"multi-host replicas above maximum": {
			group:     rayv1.WorkerGroupSpec{Replicas: new(int32(4)), MinReplicas: new(int32(1)), MaxReplicas: new(int32(2)), NumOfHosts: 3},
			wantCount: 6,
		},
		"absent API defaults": {wantCount: 1},
		"absent replicas with minimum": {
			group:     rayv1.WorkerGroupSpec{MinReplicas: new(int32(2))},
			wantCount: 2,
		},
		"zero replicas and zero minimum": {
			group:     rayv1.WorkerGroupSpec{Replicas: new(int32(0)), MinReplicas: new(int32(0)), MaxReplicas: new(int32(2))},
			wantCount: 0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.group.GroupName = "workers"
			spec := &rayv1.RayClusterSpec{WorkerGroupSpecs: []rayv1.WorkerGroupSpec{tc.group}}
			before := spec.DeepCopy()
			podSets, err := BuildPodSets(spec, nil)
			if err != nil {
				t.Fatalf("BuildPodSets: %v", err)
			}
			if got := podSets[1].Count; got != tc.wantCount {
				t.Errorf("worker PodSet count = %d, want %d", got, tc.wantCount)
			}
			wantCounts := map[kueue.PodSetReference]int32{"workers": tc.wantCount}
			if diff := cmp.Diff(wantCounts, WorkerGroupPodCounts(spec)); diff != "" {
				t.Errorf("WorkerGroupPodCounts mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(before, spec); diff != "" {
				t.Errorf("count derivation mutated the RayCluster spec (-before +after):\n%s", diff)
			}
		})
	}
}
