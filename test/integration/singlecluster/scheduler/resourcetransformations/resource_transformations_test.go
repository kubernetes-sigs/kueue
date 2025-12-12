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

package resourcetransformations

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Resource Transformations", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
		clusterQueue  *kueue.ClusterQueue
		localQueue    *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		// Configure resource transformations for testing
		transformations := []config.ResourceTransformation{
			{
				Input:    "nvidia.com/mig-1g.5gb",
				Strategy: ptr.To(config.Replace),
				Outputs: corev1.ResourceList{
					"example.com/accelerator-memory": resource.MustParse("5Ki"),
					"example.com/credits":            resource.MustParse("10"),
				},
			},
			{
				Input:      "nvidia.com/gpucores",
				Strategy:   ptr.To(config.Replace),
				MultiplyBy: "nvidia.com/gpu",
				Outputs: corev1.ResourceList{
					"nvidia.com/total-gpucores": resource.MustParse("1"),
				},
			},
			{
				Input:      "nvidia.com/gpumem",
				Strategy:   ptr.To(config.Replace),
				MultiplyBy: "nvidia.com/gpu",
				Outputs: corev1.ResourceList{
					"nvidia.com/total-gpumem": resource.MustParse("1"),
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(transformations))
	})

	ginkgo.BeforeEach(func() {
		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "resource-transformations-")

		clusterQueue = utiltestingapi.MakeClusterQueue("test-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource("example.com/accelerator-memory", "100Gi").
					Resource("example.com/credits", "1000").
					Resource("nvidia.com/gpu", "50").
					Resource("nvidia.com/total-gpucores", "1000").
					Resource("nvidia.com/total-gpumem", "102400").
					Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("should transform resources with MultiplyBy", func() {
		ginkgo.By("Creating a workload with vgpu resources")
		wl := utiltestingapi.MakeWorkload("multiply-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request("nvidia.com/gpu", "2").
			Request("nvidia.com/gpucores", "20").
			Request("nvidia.com/gpumem", "1024").
			Obj()
		util.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("Waiting for workload to be admitted")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		ginkgo.By("Verifying MultiplyBy transformation", func() {
			wlLookupKey := client.ObjectKeyFromObject(wl)
			createdWorkload := &kueue.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())

				resourceUsage := createdWorkload.Status.Admission.PodSetAssignments[0].ResourceUsage
				g.Expect(resourceUsage).To(gomega.BeComparableTo(corev1.ResourceList{
					"nvidia.com/gpu":            resource.MustParse("2"),
					"nvidia.com/total-gpucores": resource.MustParse("40"),
					"nvidia.com/total-gpumem":   resource.MustParse("2048"),
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should handle multiple PodSets with transformations", func() {
		ginkgo.By("Creating a workload with multiple PodSets")
		wl := utiltestingapi.MakeWorkload("multi-podset-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(
				*utiltestingapi.MakePodSet("ps01", 1).
					Request("nvidia.com/mig-1g.5gb", "2").
					Obj(),
				*utiltestingapi.MakePodSet("ps02", 2).
					Request("nvidia.com/gpu", "1").
					Request("nvidia.com/gpucores", "10").
					Obj(),
			).
			Obj()
		util.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("Waiting for workload to be admitted")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		ginkgo.By("Verifying transformations for each PodSet", func() {
			wlLookupKey := client.ObjectKeyFromObject(wl)
			createdWorkload := &kueue.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

				g.Expect(createdWorkload.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(createdWorkload.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))

				ps1Usage := createdWorkload.Status.Admission.PodSetAssignments[0].ResourceUsage
				g.Expect(ps1Usage).To(gomega.BeComparableTo(corev1.ResourceList{
					"example.com/accelerator-memory": resource.MustParse("10240"),
					"example.com/credits":            resource.MustParse("20"),
				}))

				ps2Usage := createdWorkload.Status.Admission.PodSetAssignments[1].ResourceUsage
				g.Expect(ps2Usage).To(gomega.BeComparableTo(corev1.ResourceList{
					"nvidia.com/gpu":            resource.MustParse("2"),
					"nvidia.com/total-gpucores": resource.MustParse("20"),
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
