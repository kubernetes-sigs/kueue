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

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/resources"
)

// The two paths an exact Amount costs something on: the fold of a workload's
// usage into a queue's, which every admission and removal runs, and the clone
// a snapshot takes. Written against the API both sides of this change carry, so
// the numbers can be compared with benchstat.

var benchNode resourceNode

func benchQuantities(n int) resources.FlavorResourceQuantities {
	frq := make(resources.FlavorResourceQuantities, n)
	for i := range n {
		fr := resources.FlavorResource{Flavor: "f", Resource: corev1.ResourceName(string(rune('a' + i)))}
		frq[fr] = resources.NewAmount(int64(i+1) << 30)
	}
	return frq
}

func BenchmarkUpdateFlavorUsage(b *testing.B) {
	delta := benchQuantities(16)
	total := benchQuantities(16)
	for b.Loop() {
		updateFlavorUsage(delta, total, add)
		updateFlavorUsage(delta, total, subtract)
	}
}

func BenchmarkResourceNodeClone(b *testing.B) {
	n := NewResourceNode()
	n.Usage = benchQuantities(16)
	n.SubtreeQuota = benchQuantities(16)
	var out resourceNode
	for b.Loop() {
		out = n.Clone()
	}
	benchNode = out
}
