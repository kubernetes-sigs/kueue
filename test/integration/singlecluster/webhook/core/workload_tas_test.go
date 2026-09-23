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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegatypes "github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload unhealthy-node eviction threshold validation", func() {
	var namespace *corev1.Namespace
	const annotation = kueue.UnhealthyNodesConcurrentEvictionThresholdAnnotation
	invalidAnnotation := gomega.SatisfyAll(
		utiltesting.BeForbiddenError(),
		gomega.MatchError(gomega.ContainSubstring("metadata.annotations["+annotation+"]")),
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
		namespace = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "threshold-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, namespace)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.DescribeTable("validates annotation values on create and update",
		func(enabled bool, value string, matcher gomegatypes.GomegaMatcher) {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, enabled)
			wl := utiltestingapi.MakeWorkload("create", namespace.Name).Annotation(annotation, value).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(matcher)

			wl = utiltestingapi.MakeWorkload("update", namespace.Name).Annotation(annotation, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				wl.Annotations[annotation] = value
				g.Expect(k8sClient.Update(ctx, wl)).To(matcher)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
		ginkgo.Entry("accepts the minimum", true, "1", gomega.Succeed()),
		ginkgo.Entry("accepts the maximum", true, "8", gomega.Succeed()),
		ginkgo.Entry("rejects zero", true, "0", invalidAnnotation),
		ginkgo.Entry("rejects values above the maximum", true, "9", invalidAnnotation),
		ginkgo.Entry("rejects non-numeric values", true, "many", invalidAnnotation),
		ginkgo.Entry("rejects empty values", true, "", invalidAnnotation),
		ginkgo.Entry("ignores invalid values when disabled", false, "many", gomega.Succeed()),
		ginkgo.Entry("ignores empty values when disabled", false, "", gomega.Succeed()),
	)

	ginkgo.DescribeTable("allows status updates beyond the annotation threshold",
		func(value string) {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, false)
			wl := utiltestingapi.MakeWorkload("legacy", namespace.Name).Annotation(annotation, value).Obj()
			util.MustCreate(ctx, k8sClient, wl)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				wl.Status.UnhealthyNodes = []kueue.UnhealthyNode{{Name: "node1"}, {Name: "node2"}}
				g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
		ginkgo.Entry("with a threshold of one", "1"),
		ginkgo.Entry("with an unchanged legacy invalid value", "many"),
		ginkgo.Entry("with an unchanged legacy empty value", ""),
	)

	ginkgo.DescribeTable("validates changes to legacy invalid annotations",
		func(value *string, matcher gomegatypes.GomegaMatcher) {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, false)
			wl := utiltestingapi.MakeWorkload("legacy", namespace.Name).Annotation(annotation, "many").Obj()
			util.MustCreate(ctx, k8sClient, wl)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				if value == nil {
					delete(wl.Annotations, annotation)
				} else {
					wl.Annotations[annotation] = *value
				}
				g.Expect(k8sClient.Update(ctx, wl)).To(matcher)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		},
		ginkgo.Entry("accepts correction", new("2"), gomega.Succeed()),
		ginkgo.Entry("accepts removal", (*string)(nil), gomega.Succeed()),
		ginkgo.Entry("rejects a different invalid value", new("0"), invalidAnnotation),
	)
})
