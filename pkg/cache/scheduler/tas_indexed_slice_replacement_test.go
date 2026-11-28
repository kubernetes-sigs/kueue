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
	"fmt"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestRequiredReplacementDomainIndexedSlices(t *testing.T) {
	const blockLabel = "example.com/block"
	const rackLabel = "example.com/rack"
	assignment := &tas.TopologyAssignment{
		Levels: []string{corev1.LabelHostname},
		Domains: []tas.TopologyDomainAssignment{
			{Values: []string{"a-failed"}, Count: 1},
			{Values: []string{"a-peer"}, Count: 1},
			{Values: []string{"b-failed"}, Count: 1},
			{Values: []string{"b-peer"}, Count: 1},
		},
	}
	cases := map[string]struct {
		failedNode            string
		domains               []tas.TopologyDomainAssignment
		includeFailed         bool
		nonIndexed            bool
		multiLayer            bool
		grouped               bool
		wantDomain            tas.TopologyDomainID
		wantUnpinnedPreferred bool
		wantError             string
	}{
		"first of two missing nodes follows its own cohort": {
			failedNode: "a-failed",
			wantDomain: "b1,r1",
		},
		"two failed ranks straddle two partially surviving cohorts": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-peer"}, Count: 1},
				{Values: []string{"a-failed"}, Count: 2},
				{Values: []string{"a-spare"}, Count: 1},
			},
			wantDomain: "b1,r1",
		},
		"multi-layer straddling ranks pin the innermost partial cohort": {
			failedNode: "a-failed",
			multiLayer: true,
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-peer"}, Count: 1},
				{Values: []string{"a-failed"}, Count: 2},
				{Values: []string{"a-spare"}, Count: 1},
			},
			wantDomain: "b1,r1",
		},
		"repeated failed node intervals each straddle cohorts": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-peer"}, Count: 1},
				{Values: []string{"a-failed"}, Count: 2},
				{Values: []string{"a-spare"}, Count: 2},
				{Values: []string{"a-failed"}, Count: 2},
				{Values: []string{"a-peer"}, Count: 1},
			},
			wantDomain: "b1,r1",
		},
		"whole slice loss retains only the overall required domain": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-failed"}, Count: 2},
				{Values: []string{"b-peer"}, Count: 2},
			},
			wantDomain:            "b1",
			wantUnpinnedPreferred: true,
		},
		"adjacent failed entries together form a whole slice": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-failed"}, Count: 1},
				{Values: []string{"a-failed"}, Count: 1},
				{Values: []string{"b-peer"}, Count: 2},
			},
			wantDomain:            "b1",
			wantUnpinnedPreferred: true,
		},
		"straddling missing cohorts fail closed": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"also-missing"}, Count: 1},
				{Values: []string{"a-failed"}, Count: 2},
				{Values: []string{"also-missing"}, Count: 1},
				{Values: []string{"b-peer"}, Count: 2},
			},
			wantError: `cannot determine original slice topology domain for unhealthy node "a-failed"`,
		},
		"second of two missing nodes follows its own cohort": {
			failedNode: "b-failed",
			wantDomain: "b1,r2",
		},
		"failed node still in snapshot identifies its domain": {
			failedNode:    "b-failed",
			includeFailed: true,
			wantDomain:    "b1,r2",
		},
		"multi-layer constraints use the innermost broken cohort": {
			failedNode: "b-failed",
			multiLayer: true,
			wantDomain: "b1,r2",
		},
		"whole inner slice still respects its outer cohort": {
			failedNode: "b-failed",
			multiLayer: true,
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-peer"}, Count: 2},
				{Values: []string{"b-failed"}, Count: 2},
			},
			wantDomain: "b1",
		},
		"group anchor does not choose another workers slice": {
			failedNode: "b-failed",
			grouped:    true,
			wantDomain: "b1,r2",
		},
		"missing cohort fails closed": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-failed"}, Count: 1},
				{Values: []string{"also-missing"}, Count: 1},
				{Values: []string{"b-peer"}, Count: 2},
			},
			wantError: `cannot determine original slice topology domain for unhealthy node "a-failed"`,
		},
		"surviving cohort identifies the domain of a node spanning cohorts": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-failed"}, Count: 3},
				{Values: []string{"a-peer"}, Count: 1},
				{Values: []string{"b-peer"}, Count: 2},
			},
			wantDomain: "b1,r1",
		},
		"conflicting cohort domains fail closed": {
			failedNode: "a-failed",
			domains: []tas.TopologyDomainAssignment{
				{Values: []string{"a-failed"}, Count: 1},
				{Values: []string{"b-peer"}, Count: 1},
			},
			includeFailed: true,
			wantError:     `indexed slices containing unhealthy node "a-failed" span multiple "example.com/rack" domains`,
		},
		"nonindexed assignment retains aggregate selection": {
			failedNode: "b-failed",
			nonIndexed: true,
			wantDomain: "b1,r1",
		},
	}
	for name, tc := range cases {
		for _, required := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/required=%t", name, required), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.TASMultiLayerTopology, true)
				_, log := utiltesting.ContextWithLog(t)
				nodes := []*corev1.Node{
					node.MakeNode("a-peer").Label(blockLabel, "b1").Label(rackLabel, "r1").Label(corev1.LabelHostname, "a-peer").Obj(),
					node.MakeNode("a-spare").Label(blockLabel, "b1").Label(rackLabel, "r1").Label(corev1.LabelHostname, "a-spare").Obj(),
					node.MakeNode("b-peer").Label(blockLabel, "b1").Label(rackLabel, "r2").Label(corev1.LabelHostname, "b-peer").Obj(),
					node.MakeNode("b-spare").Label(blockLabel, "b1").Label(rackLabel, "r2").Label(corev1.LabelHostname, "b-spare").Obj(),
				}
				if tc.includeFailed {
					nodes = append(nodes,
						node.MakeNode("a-failed").Label(blockLabel, "b1").Label(rackLabel, "r1").Label(corev1.LabelHostname, "a-failed").Obj(),
						node.MakeNode("b-failed").Label(blockLabel, "b1").Label(rackLabel, "r2").Label(corev1.LabelHostname, "b-failed").Obj())
				}
				tree := newTopologyTree([]string{blockLabel, rackLabel, corev1.LabelHostname}, nodes, 0)
				snapshot := newTASFlavorSnapshot(log, flavorInformation{TopologyName: "default"}, tree, newDefaultSimulator())
				original := *assignment
				original.Domains = slices.Clone(assignment.Domains)
				if tc.domains != nil {
					original.Domains = slices.Clone(tc.domains)
				}
				remaining := original
				ps := utiltestingapi.MakePodSet("workers", int(tas.CountPodsInAssignment(&original))).
					SliceRequiredTopologyRequest(rackLabel).SliceSizeTopologyRequest(2).
					PodIndexLabel(new("batch.kubernetes.io/job-completion-index"))
				if required {
					ps.RequiredTopologyRequest(blockLabel)
				} else {
					ps.PreferredTopologyRequest(blockLabel)
				}
				if tc.nonIndexed {
					ps.PodIndexLabel(nil)
				}
				if tc.multiLayer {
					ps.SliceRequiredTopologyConstraints(
						kueue.PodsetSliceRequiredTopologyConstraint{Topology: blockLabel, Size: 4},
						kueue.PodsetSliceRequiredTopologyConstraint{Topology: rackLabel, Size: 2})
				}
				var anchor []tas.TopologyDomainAssignment
				if tc.grouped {
					ps.PodSetGroup("group")
					anchor = []tas.TopologyDomainAssignment{{Values: []string{"a-peer"}, Count: 1}}
				}
				tr := TASPodSetRequests{PodSet: ps.Obj(), Count: deleteDomain(&remaining, tc.failedNode)}
				got, err := snapshot.requiredReplacementDomain(&tr, &remaining, &original, tc.failedNode, anchor)
				gotError := ""
				if err != nil {
					gotError = err.Error()
				}
				if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
					t.Errorf("unexpected error (-want,+got):\n%s", diff)
				}
				wantDomain := tc.wantDomain
				if tc.wantUnpinnedPreferred && !required {
					wantDomain = ""
				}
				if diff := cmp.Diff(wantDomain, got); diff != "" {
					t.Errorf("unexpected domain (-want,+got):\n%s", diff)
				}
				if diff := cmp.Diff(assignment.Domains, original.Domains); tc.domains == nil && diff != "" {
					t.Errorf("original assignment was modified (-want,+got):\n%s", diff)
				}
			})
		}
	}
}
