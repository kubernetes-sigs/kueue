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

package job

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	pkgconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Elastic partial Job resizing", ginkgo.Label("job:batch", "area:jobs"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		cq     *kueue.ClusterQueue
		job    *batchv1.Job
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
			features.ElasticJobsViaWorkloadSlices:                          true,
			features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp: true,
		})
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-partial-resize-")
		flavor = utiltestingapi.MakeResourceFlavor("elastic-partial-resize").Obj()
		cq = utiltestingapi.MakeClusterQueue("elastic-partial-resize").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		lq := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.MustCreate(ctx, k8sClient, lq)
		job = testingjob.MakeJob("job", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(pkgconstants.ElasticJobScaleUpStrategyAnnotationKey, pkgconstants.ElasticJobScaleUpStrategyPartial).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(4).
			Obj()
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
	})

	ginkgo.It("Should update the Workload in place when an admitted partial Job scales below its minimum", func() {
		behavioral.MustCreate(ctx, k8sClient, job)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, kueue.DefaultPodSetName, 4)
		gomega.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(4))))
		uid := wl.UID

		ginkgo.By("scaling the admitted Job from four pods to one")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			job.Spec.Parallelism = new(int32(1))
			g.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(uid))
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
			g.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(1))))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)
	})

	ginkgo.It("Should keep the lowered minimum in the new workload slice when a partial Job scales up after scaling below its minimum", func() {
		behavioral.MustCreate(ctx, k8sClient, job)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, kueue.DefaultPodSetName, 4)
		gomega.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(4))))

		ginkgo.By("scaling the admitted Job from four pods to one")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			job.Spec.Parallelism = new(int32(1))
			g.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
			g.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(1))))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("scaling the Job from one pod to five")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			job.Spec.Parallelism = new(int32(5))
			g.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		newSlice := behavioral.ExpectNewWorkloadSlice(ctx, k8sClient, wl)
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, newSlice, kueue.DefaultPodSetName, 5)
		gomega.Expect(newSlice.Spec.PodSets[0].Count).To(gomega.Equal(int32(5)))
		gomega.Expect(newSlice.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(1))))
	})

	ginkgo.It("Should admit a pending partial Job after it scales below its minimum", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			behavioral.SetResourceNominalQuota(cq, corev1.ResourceCPU, "1")
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.MustCreate(ctx, k8sClient, job)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		gomega.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(4))))
		uid := wl.UID

		ginkgo.By("scaling the pending Job to fit the quota")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			job.Spec.Parallelism = new(int32(1))
			g.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, kueue.DefaultPodSetName, 1)
		gomega.Expect(wl.UID).To(gomega.Equal(uid))
		gomega.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(1)))
		gomega.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(1))))
		behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)
	})

	ginkgo.It("Should require the updated full count for a partial Job scaled up before its first admission", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			behavioral.SetResourceNominalQuota(cq, corev1.ResourceCPU, "2")
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.MustCreate(ctx, k8sClient, job)
		wl := &behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)[0]
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
		uid := wl.UID

		ginkgo.By("scaling up before the first admission")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).To(gomega.Succeed())
			job.Spec.Parallelism = new(int32(8))
			g.Expect(k8sClient.Update(ctx, job)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(wl.UID).To(gomega.Equal(uid))
			g.Expect(wl.Spec.PodSets[0].Count).To(gomega.Equal(int32(8)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("keeping the Job pending when only five of eight pods fit")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			behavioral.SetResourceNominalQuota(cq, corev1.ResourceCPU, "5")
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
			g.Expect(wl.Spec.PodSets[0].MinCount).To(gomega.Equal(new(int32(8))))
		}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("admitting all eight pods once the full quota is available")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
			behavioral.SetResourceNominalQuota(cq, corev1.ResourceCPU, "8")
			g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectPodSetAdmittedCount(ctx, k8sClient, wl, kueue.DefaultPodSetName, 8)
		gomega.Expect(wl.UID).To(gomega.Equal(uid))
		behavioral.ExpectWorkloadsInNamespace(ctx, k8sClient, ns.Name, 1)
	})
})
