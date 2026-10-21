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
	"k8s.io/component-base/featuregate"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestRequestsForDomain(t *testing.T) {
	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		levels       []string
		allocatable  corev1.ResourceList
		admitted     workload.TASFlavorUsage
		domainID     tas.TopologyDomainID
		delegation   *DRADelegation
		want         resources.Requests
	}{
		"a node publishing the delegated resource is charged for it": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRADeviceFeasibility: true},
			levels:       []string{corev1.LabelHostname},
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
				"example.com/gpu":  resource.MustParse("1"),
			},
			domainID: "x1",
			delegation: &DRADelegation{
				Resources:   []corev1.ResourceName{"example.com/gpu"},
				Undelegated: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
			},
			want: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
		},
		"a node publishing the delegated resource is charged for it even when none is left": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRADeviceFeasibility: true},
			levels:       []string{corev1.LabelHostname},
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
				"example.com/gpu":  resource.MustParse("1"),
			},
			admitted: workload.TASFlavorUsage{{
				Values:            []string{"x1"},
				SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{"example.com/gpu": 1}),
				Count:             1,
			}},
			domainID: "x1",
			delegation: &DRADelegation{
				Resources:   []corev1.ResourceName{"example.com/gpu"},
				Undelegated: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
			},
			want: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
		},
		"a node not publishing the delegated resource is not charged for it": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRADeviceFeasibility: true},
			levels:       []string{corev1.LabelHostname},
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
			},
			domainID: "x1",
			delegation: &DRADelegation{
				Resources:   []corev1.ResourceName{"example.com/gpu"},
				Undelegated: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
			},
			want: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
		},
		"a domain above the node is not charged for it": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRADeviceFeasibility:      true,
				features.TASNodeFeasibilityForAllLevels: true,
			},
			levels: []string{utiltesting.DefaultRackTopologyLevel},
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
				"example.com/gpu":  resource.MustParse("1"),
			},
			domainID: "r1",
			delegation: &DRADelegation{
				Resources:   []corev1.ResourceName{"example.com/gpu"},
				Undelegated: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
			},
			want: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
		},
		"no delegation": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRADeviceFeasibility: true},
			levels:       []string{corev1.LabelHostname},
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
				"example.com/gpu":  resource.MustParse("1"),
			},
			domainID: "x1",
			want:     resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
		},
		"KueueDRADeviceFeasibility off": {
			featureGates: map[featuregate.Feature]bool{features.KueueDRADeviceFeasibility: false},
			levels:       []string{corev1.LabelHostname},
			allocatable: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
				"example.com/gpu":  resource.MustParse("1"),
			},
			domainID: "x1",
			delegation: &DRADelegation{
				Resources:   []corev1.ResourceName{"example.com/gpu"},
				Undelegated: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000, "example.com/gpu": 1}),
			},
			want: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000}),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			_, log := utiltesting.ContextWithLog(t)
			nodeObj := node.MakeNode("x1").
				Label(utiltesting.DefaultRackTopologyLevel, "r1").
				Label(corev1.LabelHostname, "x1").
				StatusAllocatable(tc.allocatable).
				Ready().
				Obj()
			snapshot := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "tas-topology"},
				newTopologyTree(tc.levels, []*corev1.Node{nodeObj}, 0), newDefaultSimulator())
			snapshot.updateTASUsageForHeldDomains(tc.admitted, add)
			requests := resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 1000})
			got := snapshot.RequestsForDomain(tc.domainID, requests, tc.delegation)
			if diff := cmp.Diff(tc.want, got, cmp.Comparer(resources.Equal)); diff != "" {
				t.Errorf("RequestsForDomain() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
