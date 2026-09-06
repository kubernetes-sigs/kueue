/*
Copyright 2026 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

// Regression coverage for the KEP-12100 partial scale-up Workload shapes.
//
// The elastic-job reconcilers (e.g. the RayCluster controller with the
// ElasticJobScaleUpStrategy=partial annotation) produce Workloads that carry
// the workload-slicing enabled annotation together with podSets that set
// minCount. This suite exercises the real API server with the Workload
// mutating and validating webhooks installed (see util.WebhookPath in the
// suite setup): those shapes must be admitted while the partial scale-up
// feature gate is on, with and without the classic PartialAdmission gate, and
// must stay rejected while the partial scale-up gate is off.
// See https://github.com/kubernetes-sigs/kueue/issues/15249.
var _ = ginkgo.Describe("Workload webhooks admit KEP-12100 partial scale-up shapes", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.It("accepts the first-create shape (one minCount podSet) with the partial scale-up gate on", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialAdmission, true)

		wl := utiltestingapi.MakeWorkload("elastic-partial-first-create", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(
				*utiltestingapi.MakePodSet("head", 1).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 4).
					SetMinimumCount(4).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			).
			Obj()

		ginkgo.By("creating the Workload")
		gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

		ginkgo.By("the minCount survives the mutating webhook")
		gomega.Expect(wl.Spec.PodSets[1].MinCount).ShouldNot(gomega.BeNil())
		gomega.Expect(*wl.Spec.PodSets[1].MinCount).Should(gomega.Equal(int32(4)))
	})

	ginkgo.It("accepts the scale-up probe shape (multiple minCount podSets) with the partial scale-up gate on", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialAdmission, true)

		wl := utiltestingapi.MakeWorkload("elastic-partial-probe", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(
				*utiltestingapi.MakePodSet("workers-group-0", 8).
					SetMinimumCount(5).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakePodSet("workers-group-1", 6).
					SetMinimumCount(3).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			).
			Obj()

		ginkgo.By("creating the Workload")
		gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

		ginkgo.By("all minCounts survive the mutating webhook")
		gomega.Expect(wl.Spec.PodSets[0].MinCount).ShouldNot(gomega.BeNil())
		gomega.Expect(wl.Spec.PodSets[1].MinCount).ShouldNot(gomega.BeNil())
	})

	ginkgo.It("accepts the partial shapes with PartialAdmission off and preserves minCount", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialAdmission, false)

		wl := utiltestingapi.MakeWorkload("elastic-partial-no-pa", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(
				*utiltestingapi.MakePodSet("head", 1).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 4).
					SetMinimumCount(4).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			).
			Obj()

		ginkgo.By("creating the Workload")
		gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

		ginkgo.By("the partial minCount is honored without PartialAdmission and survives the webhooks")
		gomega.Expect(wl.Spec.PodSets[1].MinCount).ShouldNot(gomega.BeNil())
		gomega.Expect(*wl.Spec.PodSets[1].MinCount).Should(gomega.Equal(int32(4)))
	})

	ginkgo.It("rejects elastic minCount shapes while the partial scale-up gate is off", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, false)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialAdmission, true)

		wl := utiltestingapi.MakeWorkload("elastic-partial-gate-off", ns.Name).
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(
				*utiltestingapi.MakePodSet("head", 1).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakePodSet("workers-group-0", 4).
					SetMinimumCount(4).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			).
			Obj()

		ginkgo.By("creating the Workload")
		err := k8sClient.Create(ctx, wl)
		gomega.Expect(err).Should(gomega.HaveOccurred())
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		gomega.Expect(err.Error()).To(gomega.ContainSubstring("partial admission and elastic job cannot be used together"))
	})
})
