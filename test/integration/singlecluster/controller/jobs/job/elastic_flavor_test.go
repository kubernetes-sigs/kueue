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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Elastic Job flavor selection at zero parallelism", ginkgo.Ordered, func() {
	var (
		ns                   *corev1.Namespace
		cpuFlavor, gpuFlavor *kueue.ResourceFlavor
		cq                   *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cpuFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, gpuFlavor, true)
	})

	ginkgo.It("Should choose a feasible flavor at zero parallelism and admit the scale-up slice", func() {
		const gpuResource = corev1.ResourceName("example.com/gpu")
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-flavor-")
		cpuFlavor = utiltestingapi.MakeResourceFlavor("cpu").NodeLabel("instance-type", "cpu").Obj()
		gpuFlavor = utiltestingapi.MakeResourceFlavor("gpu").NodeLabel("instance-type", "gpu").Obj()
		cq = utiltestingapi.MakeClusterQueue("elastic-flavor").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(cpuFlavor.Name).Resource(corev1.ResourceCPU, "10").Resource(gpuResource, "0").Obj(),
			*utiltestingapi.MakeFlavorQuotas(gpuFlavor.Name).Resource(corev1.ResourceCPU, "10").Resource(gpuResource, "4").Obj(),
		).Obj()
		lq := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, cpuFlavor)
		util.MustCreate(ctx, k8sClient, gpuFlavor)
		util.MustCreate(ctx, k8sClient, cq)
		util.MustCreate(ctx, k8sClient, lq)

		testJob := testingjob.MakeJob("elastic-flavor", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").RequestAndLimit(gpuResource, "1").
			Parallelism(0).Completions(2).Obj()
		util.MustCreate(ctx, k8sClient, testJob)

		var rootWorkloadName string
		ginkgo.By("admitting the zero-count slice on the GPU flavor without charging quota")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(1))
			wl := &workloads.Items[0]
			g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
			g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
			assignment := wl.Status.Admission.PodSetAssignments[0]
			g.Expect(assignment.Count).Should(gomega.HaveValue(gomega.Equal(int32(0))))
			g.Expect(assignment.Flavors).Should(gomega.Equal(map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "gpu", gpuResource: "gpu",
			}))
			g.Expect(assignment.ResourceUsage).Should(gomega.Equal(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("0"), gpuResource: resource.MustParse("0"),
			}))
			rootWorkloadName = wl.Name
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("unsuspending the Job with the GPU node selector before scaling up")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJob), testJob)).Should(gomega.Succeed())
			g.Expect(testJob.Spec.Suspend).Should(gomega.HaveValue(gomega.BeFalse()))
			g.Expect(testJob.Spec.Template.Spec.NodeSelector).Should(gomega.HaveKeyWithValue("instance-type", "gpu"))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("scaling the Job to two pods")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJob), testJob)).Should(gomega.Succeed())
			testJob.Spec.Parallelism = new(int32(2))
			g.Expect(k8sClient.Update(ctx, testJob)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("admitting the replacement slice on the GPU flavor and finishing the root slice")
		gomega.Eventually(func(g gomega.Gomega) {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			g.Expect(workloads.Items).Should(gomega.HaveLen(2))
			for i := range workloads.Items {
				wl := &workloads.Items[i]
				if wl.Name == rootWorkloadName {
					g.Expect(workloadfinish.IsFinished(wl)).Should(gomega.BeTrue())
					continue
				}
				g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
				assignment := wl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.Count).Should(gomega.HaveValue(gomega.Equal(int32(2))))
				g.Expect(assignment.Flavors).Should(gomega.Equal(map[corev1.ResourceName]kueue.ResourceFlavorReference{
					corev1.ResourceCPU: "gpu", gpuResource: "gpu",
				}))
				g.Expect(assignment.ResourceUsage).Should(gomega.Equal(corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"), gpuResource: resource.MustParse("2"),
				}))
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
