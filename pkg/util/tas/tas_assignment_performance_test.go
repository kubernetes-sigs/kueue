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

package tas

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"slices"
	"sort"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

// maxTopologyAssignmentJSONBytes is the test budget used to verify assignment
// scalability. The encoder does not branch on serialized size.
const maxTopologyAssignmentJSONBytes = 1_500_000

// Generate n hex numbers of given length,
// ensuring they're distinct (and otherwise quasi-random).
// For the health of tests, this function behaves deterministically.
func randomHexIDs(n, length int) []string {
	chosen := map[string]bool{}
	res := make([]string, n)
	rnd := rand.NewChaCha8([32]byte{})
	bytes := make([]byte, (length+1)/2)
	for i := range n {
		for {
			var _, _ = rnd.Read(bytes)
			id := hex.EncodeToString(bytes)[:length]
			if !chosen[id] {
				chosen[id] = true
				res[i] = id
				break
			}
		}
	}
	return res
}

func fixedID(length int) string {
	// This is really whatever.
	return strings.Repeat("a", length)
}

func consecutiveIPs(n int) []string {
	res := make([]string, n)
	for i := range n {
		// Picking 100.10x.xxx.xxx (instead of traditional 10.x.xxx.xxx)
		// to make the test scenario a bit more adverse.
		res[i] = fmt.Sprintf("100.%d.%d.%d",
			100+i/(1<<16),
			i%(1<<16)/(1<<8),
			i%(1<<8),
		)
	}
	return res
}

// namingScheme generates a list of node names according to a specific recipe.
//
// The list should be "well-prefixing", in that namingScheme(n)[:m] should be
// an "equally good representative" of the scheme as namingScheme(m).
// (This convention helps test performance; it's used in "approxMaxNodesFor").
// To achieve that, specific schemes should rotate node "properties"
// (like node pool, region etc.) using "%" operator rather than in big fixed chunks.
type namingScheme interface {
	generate(nodes int) []string
}

type poolAndNodeBasedNaming struct {
	fixedPrefixAndSuffixLength int
	pools                      int
	nodeIDLength               int
	poolIDLength               int
}

func (n poolAndNodeBasedNaming) generate(nodes int) []string {
	res := make([]string, nodes)
	nodeIDs := randomHexIDs(nodes, n.nodeIDLength)
	poolIDs := randomHexIDs(n.pools, n.poolIDLength)
	for i := range nodes {
		res[i] = fmt.Sprintf("%s-%s-%s-%s",
			fixedID(n.fixedPrefixAndSuffixLength),
			poolIDs[i%n.pools],
			nodeIDs[i],
			fixedID(n.fixedPrefixAndSuffixLength),
		)
	}
	return res
}

type regionAndIPBasedNaming struct {
	fixedPrefixAndSuffixLength int
	regions                    int
	regionIDLength             int
}

func (n regionAndIPBasedNaming) generate(nodes int) []string {
	res := make([]string, nodes)
	nodeIPs := consecutiveIPs(nodes)
	regionIDs := randomHexIDs(n.regions, n.regionIDLength)
	for i := range nodes {
		res[i] = fmt.Sprintf("%s-%s-%s-%s",
			fixedID(n.fixedPrefixAndSuffixLength),
			regionIDs[i%n.regions],
			nodeIPs[i],
			fixedID(n.fixedPrefixAndSuffixLength),
		)
	}
	return res
}

type nodeBasedNaming struct {
	fixedPrefixAndSuffixLength int
	nodeIDLength               int
}

func (config nodeBasedNaming) generate(nodes int) []string {
	res := make([]string, nodes)
	nodeIDs := randomHexIDs(nodes, config.nodeIDLength)
	for i := range nodes {
		res[i] = fmt.Sprintf("%s-%s-%s",
			fixedID(config.fixedPrefixAndSuffixLength),
			nodeIDs[i],
			fixedID(config.fixedPrefixAndSuffixLength),
		)
	}
	return res
}

type gkeNodePoolBasedNaming struct {
	pools int
}

func (n gkeNodePoolBasedNaming) generate(nodes int) []string {
	res := make([]string, nodes)
	for i := range nodes {
		poolID := i % n.pools
		nodeID := i / n.pools
		res[i] = fmt.Sprintf("gke-cluster-pool-%04d-%08x-%04x", poolID, poolID, nodeID)
	}
	return res
}

func internalSinglePodsOn(nodes []string) *TopologyAssignment {
	res := &TopologyAssignment{
		Levels:  []string{corev1.LabelHostname},
		Domains: make([]TopologyDomainAssignment, len(nodes)),
	}
	for i, n := range nodes {
		res.Domains[i] = TopologyDomainAssignment{
			Values: []string{n},
			Count:  1,
		}
	}
	return res
}

func jsonBytes(v any) []byte {
	bytes, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return bytes
}

func isTooLarge(ta *kueue.TopologyAssignment) bool {
	return len(jsonBytes(ta)) > maxTopologyAssignmentJSONBytes
}

func approxMaxNodesFor(tc performanceTestCase) int {
	step := 1_000 // We search with a reduced resolution, to speed up the test
	ceiling := 300_000
	nodeNames := tc.naming.generate(ceiling)
	found := sort.Search(ceiling/step, func(n int) bool {
		// Here we rely on "well-prefixing"; see the comment on "nodeNaming".
		return isTooLarge(V1Beta2From(internalSinglePodsOn(nodeNames[:n*step])))
	}) - 1
	return found * step
}

type performanceTestCase struct {
	name               string
	naming             namingScheme
	targetNodeCount    int
	sortNodeNames      bool
	skipApproxMaxNodes bool
}

func (tc performanceTestCase) generateNodeNames(nodes int) []string {
	nodeNames := tc.naming.generate(nodes)
	if tc.sortNodeNames {
		slices.Sort(nodeNames)
	}
	return nodeNames
}

var performanceTestCases = []performanceTestCase{
	{
		name: "pool-and-node-based naming (1000 node pools)",
		naming: poolAndNodeBasedNaming{
			pools:        1000, // happens in practice, at least in GKE
			nodeIDLength: 6,    // reached in AKS

			// reachable in AKS (<pool-name>-<8-char-id>-vmss, then let <pool-name> have 8 chars)
			poolIDLength: 22,

			fixedPrefixAndSuffixLength: 20,
		},
		targetNodeCount: 40_000,
	},
	{
		name: "pool-and-node-based naming (10 node pools)",
		naming: poolAndNodeBasedNaming{
			// Vendors tend to restrict "node pools" to 4k nodes or less,
			// so, as we care about 40k+ nodes, it seems 10+ pools will always exist.
			pools: 10,

			nodeIDLength:               6,
			poolIDLength:               22,
			fixedPrefixAndSuffixLength: 20,
		},
		targetNodeCount: 40_000,
	},
	{
		name: "region-and-IP-based naming (100 regions)",
		naming: regionAndIPBasedNaming{
			regions: 100, // EKS has 70, leaving room for growth

			// Reached in EKS ("ap-southeast-2") & ACK ("cn-zhangjiakou").
			// GKE reaches even 23 ("northamerica-northeast2") but luckily its naming is not region-based.
			regionIDLength: 14,

			fixedPrefixAndSuffixLength: 20,
		},
		targetNodeCount: 40_000,
	},
	{
		name: "region-and-IP-based naming (1 region)",
		naming: regionAndIPBasedNaming{
			regions:                    1,
			regionIDLength:             14,
			fixedPrefixAndSuffixLength: 20,
		},
		targetNodeCount: 40_000,
	},
	{
		name: "node-only-based naming",
		naming: nodeBasedNaming{
			nodeIDLength:               8, // reached in VKE
			fixedPrefixAndSuffixLength: 20,
		},
		targetNodeCount: 40_000,
	},
	{
		name: "GKE node-pool naming (1000 node pools)",
		naming: gkeNodePoolBasedNaming{
			pools: 1000,
		},
		targetNodeCount:    150_000,
		sortNodeNames:      true,
		skipApproxMaxNodes: true,
	},
}

func TestByteSizeLimit(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TASAssignmentsEncodingByHostnamePrefix, true)
	for _, tc := range performanceTestCases {
		t.Run(tc.name, func(t *testing.T) {
			ta := V1Beta2From(internalSinglePodsOn(tc.generateNodeNames(tc.targetNodeCount)))
			assertTopologyAssignmentSize(t, ta, tc.targetNodeCount)

			if tc.skipApproxMaxNodes {
				return
			}
			nodesLimit := approxMaxNodesFor(tc)
			if nodesLimit < tc.targetNodeCount {
				t.Errorf("Nodes limit for naming %q is too low: got approx. %d, want >= %d", tc.name, nodesLimit, tc.targetNodeCount)
			} else {
				t.Logf("Nodes limit for naming %q is approx. %d", tc.name, nodesLimit)
			}
		})
	}
}

func assertTopologyAssignmentSize(t *testing.T, ta *kueue.TopologyAssignment, wantDomains int) {
	t.Helper()
	if got := len(ta.Slices); got > maxTopologyAssignmentSlices {
		t.Errorf("unexpected slice count: got %d, want <= %d", got, maxTopologyAssignmentSlices)
	}
	for i, slice := range ta.Slices {
		if slice.DomainCount > maxDomainsPerTopologyAssignmentSlice {
			t.Errorf("slice %d has too many domains: got %d, want <= %d", i, slice.DomainCount, maxDomainsPerTopologyAssignmentSlice)
		}
	}
	if got := TotalDomainCount(ta); got != wantDomains {
		t.Errorf("unexpected total domain count: got %d, want %d", got, wantDomains)
	}
	if bytes := len(jsonBytes(ta)); bytes > maxTopologyAssignmentJSONBytes {
		t.Errorf("topology assignment is too large: got %d bytes, want <= %d", bytes, maxTopologyAssignmentJSONBytes)
	}
}

func BenchmarkV1Beta2From(b *testing.B) {
	features.SetFeatureGateDuringTest(b, features.TASAssignmentsEncodingByHostnamePrefix, true)
	for _, tc := range performanceTestCases {
		nodeNames := tc.naming.generate(tc.targetNodeCount)

		// For our current strategy (split into hostname prefix runs),
		// having node names sorted matches scheduler-created hostname-only assignments.
		// (This is because assignments are sorted by level values before encoding;
		// for multi-level assignments, hostnames are only sorted within higher levels).
		slices.Sort(nodeNames)
		ta := internalSinglePodsOn(nodeNames)

		desc := fmt.Sprintf("Naming scheme %q, %d nodes", tc.name, tc.targetNodeCount)
		b.Run(desc, func(b *testing.B) {
			for b.Loop() {
				var _ = V1Beta2From(ta)
			}
		})
	}
}
