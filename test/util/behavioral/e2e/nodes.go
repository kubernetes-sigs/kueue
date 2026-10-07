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
	"sigs.k8s.io/controller-runtime/pkg/client"
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
