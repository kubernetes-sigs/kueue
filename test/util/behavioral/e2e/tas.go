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

package e2e

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// ExpectRanksOnIncreasingNodes asserts that gotAssignment maps the given ranks to strictly increasing node names
// (1-1 pod-node fit).
func ExpectRanksOnIncreasingNodes(gotAssignment map[string]string, ranks []string) {
	ginkgo.GinkgoHelper()
	gomega.Expect(len(ranks)).To(gomega.BeNumerically(">=", 2), "expected at least 2 ranks to verify order")
	for i, rank := range ranks {
		node := gotAssignment[rank]
		gomega.Expect(node).NotTo(gomega.BeEmpty(), "missing assignment for rank %q", rank)
		if i > 0 {
			prevNode := gotAssignment[ranks[i-1]]
			gomega.Expect(node > prevNode).To(gomega.BeTrue(),
				"expected increasing node order: rank %q (node %q) > rank %q (node %q), got assignments %v", rank, node, ranks[i-1], prevNode, gotAssignment)
		}
	}
}

// ExpectRanksOnNonDecreasingNodes asserts that gotAssignment maps the given ranks to non-decreasing node names
// (allowing multiple pods to share a node).
func ExpectRanksOnNonDecreasingNodes(gotAssignment map[string]string, ranks []string) {
	ginkgo.GinkgoHelper()
	gomega.Expect(len(ranks)).To(gomega.BeNumerically(">=", 2), "expected at least 2 ranks to verify order")
	for i, rank := range ranks {
		node := gotAssignment[rank]
		gomega.Expect(node).NotTo(gomega.BeEmpty(), "missing assignment for rank %q", rank)
		if i > 0 {
			prevNode := gotAssignment[ranks[i-1]]
			gomega.Expect(node >= prevNode).To(gomega.BeTrue(),
				"expected non-decreasing node order: rank %q (node %q) >= rank %q (node %q), got assignments %v", rank, node, ranks[i-1], prevNode, gotAssignment)
		}
	}
}

// ExpectSameTopologyDomain verifies that all given nodes belong to the same topology domain for the given level.
func ExpectSameTopologyDomain(ctx context.Context, c client.Client, level string, nodeNames ...string) {
	ginkgo.GinkgoHelper()
	gomega.Expect(nodeNames).NotTo(gomega.BeEmpty(), "nodeNames must not be empty")
	domainByNode := GetTopologyDomainByNode(ctx, c, level)
	domains := sets.New[string]()
	for _, node := range nodeNames {
		domain := domainByNode[node]
		gomega.Expect(domain).NotTo(gomega.BeEmpty(), "node %q missing label %q", node, level)
		domains.Insert(domain)
	}
	gomega.Expect(domains).To(gomega.HaveLen(1), "expected all nodes %v to share a single %q domain, got %v", nodeNames, level, domains.UnsortedList())
}

// ExpectDistinctTopologyDomains verifies that each group of nodes belongs to a single topology domain,
// and that distinct domains are used across all groups.
func ExpectDistinctTopologyDomains(ctx context.Context, c client.Client, level string, nodeGroups ...[]string) {
	ginkgo.GinkgoHelper()
	gomega.Expect(nodeGroups).NotTo(gomega.BeEmpty(), "nodeGroups must not be empty")
	domainByNode := GetTopologyDomainByNode(ctx, c, level)
	usedDomains := sets.New[string]()
	for _, group := range nodeGroups {
		groupDomains := sets.New[string]()
		for _, node := range group {
			domain := domainByNode[node]
			gomega.Expect(domain).NotTo(gomega.BeEmpty(), "node %q missing label %q", node, level)
			groupDomains.Insert(domain)
		}
		gomega.Expect(groupDomains).To(gomega.HaveLen(1), "expected group nodes %v to share a single %q domain, got %v", group, level, groupDomains.UnsortedList())
		usedDomains.Insert(groupDomains.UnsortedList()...)
	}
	gomega.Expect(usedDomains).To(gomega.HaveLen(len(nodeGroups)), "expected %d distinct %q domains across groups, got %v", len(nodeGroups), level, usedDomains.UnsortedList())
}
