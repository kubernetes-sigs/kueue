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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	clientutil "sigs.k8s.io/kueue/pkg/util/client"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// GetTopologyDomainByNode returns a map from the name of every node that
// carries the given topology level label to its value at that level, e.g. the
// block the node belongs to.
func GetTopologyDomainByNode(ctx context.Context, c client.Client, levelLabel string) map[string]string {
	ginkgo.GinkgoHelper()
	nodes := &corev1.NodeList{}
	gomega.Expect(c.List(ctx, nodes, client.HasLabels{levelLabel})).To(gomega.Succeed())
	domains := make(map[string]string, len(nodes.Items))
	for _, node := range nodes.Items {
		domains[node.Name] = node.Labels[levelLabel]
	}
	return domains
}

// SetupTASNodes configures nodes carrying the given nodeGroupLabel to have the specified
// resource capacity and allocatable set to 1.
func SetupTASNodes(ctx context.Context, k8sClient client.Client, nodeGroupLabel string, extraResource corev1.ResourceName) {
	ginkgo.GinkgoHelper()

	nodes := &corev1.NodeList{}
	requiredLabels := client.MatchingLabels{}
	requiredLabelKeys := client.HasLabels{nodeGroupLabel}
	err := k8sClient.List(ctx, nodes, requiredLabels, requiredLabelKeys)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to list nodes for TAS")

	for _, n := range nodes.Items {
		gomega.Eventually(func(g gomega.Gomega) {
			node := &corev1.Node{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: n.Name}, node)).To(gomega.Succeed())
			err := clientutil.PatchStatus(ctx, k8sClient, node, func() (bool, error) {
				node.Status.Capacity[extraResource] = resource.MustParse("1")
				node.Status.Allocatable[extraResource] = resource.MustParse("1")
				return true, nil
			})
			g.Expect(err).NotTo(gomega.HaveOccurred())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
}
