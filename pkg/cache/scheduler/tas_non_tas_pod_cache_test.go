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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// verifyNodeUsageConsistency recomputes usage from podUsage (old O(pods) algorithm)
// and verifies it matches the incremental nodeUsage.
func verifyNodeUsageConsistency(t *testing.T, cache *nonTasUsageCache) {
	t.Helper()
	expected := make(map[string]resources.Requests)
	for _, pv := range cache.podUsage {
		if _, found := expected[pv.node]; !found {
			expected[pv.node] = resources.Requests{}
		}
		expected[pv.node].Add(pv.usage)
		expected[pv.node][corev1.ResourcePods]++
	}
	got := cache.usagePerNode()
	if diff := cmp.Diff(expected, got); diff != "" {
		t.Errorf("nodeUsage inconsistent with podUsage (-recomputed +nodeUsage):\n%s", diff)
	}
}

func makePod(name, namespace, node string, cpu string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.PodSpec{
			NodeName: node,
			Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse(cpu),
					},
				},
			}},
		},
	}
}

func TestNonTasUsageCacheIncrementalUpdates(t *testing.T) {
	testCases := map[string]struct {
		initialPods []*corev1.Pod
		// podsUpdate, when set, mutates the initialPods in place after they
		// are first applied to the cache. Each mutated pod is then re-applied
		// via cache.update — exercising in-place transitions like a pod
		// moving between nodes or transitioning to PodSucceeded.
		podsUpdate func(pods []*corev1.Pod)
		// podsDelete is the set of pod keys to delete from the cache after
		// initialPods are applied (and after podsUpdate, if any).
		podsDelete    []client.ObjectKey
		wantNodeUsage map[string]resources.Requests
	}{
		"add then delete same pod": {
			initialPods: []*corev1.Pod{makePod("pod1", "ns", "node-a", "2")},
			podsDelete:  []client.ObjectKey{{Namespace: "ns", Name: "pod1"}},
		},
		"multiple pods on same node": {
			initialPods: []*corev1.Pod{
				makePod("pod1", "ns", "node-a", "1"),
				makePod("pod2", "ns", "node-a", "2"),
			},
			wantNodeUsage: map[string]resources.Requests{
				"node-a": {corev1.ResourceCPU: 3000, corev1.ResourcePods: 2},
			},
		},
		"pod moves between nodes cleans up source node": {
			initialPods: []*corev1.Pod{makePod("pod1", "ns", "node-a", "4")},
			podsUpdate: func(pods []*corev1.Pod) {
				pods[0].Spec.NodeName = "node-b"
			},
			wantNodeUsage: map[string]resources.Requests{
				"node-b": {corev1.ResourceCPU: 4000, corev1.ResourcePods: 1},
			},
		},
		"pod resize on same node updates usage to new requests": {
			initialPods: []*corev1.Pod{makePod("pod1", "ns", "node-a", "1")},
			podsUpdate: func(pods []*corev1.Pod) {
				pods[0].Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("4")
			},
			wantNodeUsage: map[string]resources.Requests{
				"node-a": {corev1.ResourceCPU: 4000, corev1.ResourcePods: 1},
			},
		},
		"resize one of two pods on same node updates only that pod's contribution": {
			initialPods: []*corev1.Pod{
				makePod("pod1", "ns", "node-a", "1"),
				makePod("pod2", "ns", "node-a", "2"),
			},
			podsUpdate: func(pods []*corev1.Pod) {
				pods[0].Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("3")
			},
			wantNodeUsage: map[string]resources.Requests{
				"node-a": {corev1.ResourceCPU: 5000, corev1.ResourcePods: 2},
			},
		},
		"terminated pod removes usage": {
			initialPods: []*corev1.Pod{makePod("pod1", "ns", "node-a", "2")},
			podsUpdate: func(pods []*corev1.Pod) {
				pods[0].Status.Phase = corev1.PodSucceeded
			},
		},
		"delete non-existent key is no-op": {
			podsDelete: []client.ObjectKey{{Namespace: "ns", Name: "ghost"}},
		},
		"removing one of two pods leaves node entry": {
			initialPods: []*corev1.Pod{
				makePod("pod1", "ns", "node-a", "1"),
				makePod("pod2", "ns", "node-a", "1"),
			},
			podsDelete: []client.ObjectKey{{Namespace: "ns", Name: "pod1"}},
			wantNodeUsage: map[string]resources.Requests{
				"node-a": {corev1.ResourceCPU: 1000, corev1.ResourcePods: 1},
			},
		},
		"removing last pod cleans up node entry": {
			initialPods: []*corev1.Pod{
				makePod("pod1", "ns", "node-a", "1"),
				makePod("pod2", "ns", "node-a", "1"),
			},
			podsDelete: []client.ObjectKey{
				{Namespace: "ns", Name: "pod1"},
				{Namespace: "ns", Name: "pod2"},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cache := &nonTasUsageCache{
				podUsage:  make(map[types.NamespacedName]podUsageValue),
				nodeUsage: make(map[string]resources.Requests),
			}

			for _, pod := range tc.initialPods {
				cache.update(pod, log)
			}
			if tc.podsUpdate != nil {
				tc.podsUpdate(tc.initialPods)
				for _, pod := range tc.initialPods {
					cache.update(pod, log)
				}
			}
			for _, key := range tc.podsDelete {
				cache.delete(key, log)
			}

			verifyNodeUsageConsistency(t, cache)

			wantUsage := tc.wantNodeUsage
			if wantUsage == nil {
				wantUsage = map[string]resources.Requests{}
			}
			if diff := cmp.Diff(wantUsage, cache.usagePerNode()); diff != "" {
				t.Errorf("usagePerNode() mismatch (-want +got):\n%s", diff)
			}

			// Explicitly verify the internal nodeUsage map has no leftover
			// entries — a missing delete() in removeNodeUsage would leave
			// zeroed Requests behind and leak memory as nodes turn over.
			if len(cache.nodeUsage) != len(wantUsage) {
				t.Errorf("len(cache.nodeUsage)=%d, want %d (zeroed entries must be removed to avoid memory leak)",
					len(cache.nodeUsage), len(wantUsage))
			}
			for node := range wantUsage {
				if _, found := cache.nodeUsage[node]; !found {
					t.Errorf("cache.nodeUsage missing expected entry for node %q", node)
				}
			}
		})
	}
}
