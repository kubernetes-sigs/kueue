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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/discovery"
	"k8s.io/kube-openapi/pkg/util/proto"

	"sigs.k8s.io/kueue/test/util"
)

// Regression test for OpenAPI aggregation failures.
//
// When the visibility APIService is installed, its OpenAPI is aggregated into
// the cluster-wide /openapi/v2 served by the kube-apiserver. If that aggregated
// schema contains a broken $ref (dangling definition reference), kubectl can
// fail even for unrelated applies because it parses OpenAPI to build schemas.
var _ = ginkgo.Describe("Aggregated OpenAPI", ginkgo.Label("area:singlecluster", "feature:visibility"), func() {
	ginkgo.It("Should have resolvable OpenAPI v2 references", func() {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		gomega.Eventually(func(g gomega.Gomega) {
			doc, err := discoveryClient.OpenAPISchema()
			g.Expect(err).NotTo(gomega.HaveOccurred())

			// This is the same reference validation used by kubectl's OpenAPI schema loader.
			_, err = proto.NewOpenAPIData(doc)
			g.Expect(err).NotTo(gomega.HaveOccurred())
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})
})
