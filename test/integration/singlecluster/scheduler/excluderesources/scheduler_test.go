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

package excluderesources

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("SchedulerWithExcludeResourcePrefixes", func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
		cq            *kueue.ClusterQueue
		lq            *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		configuration := &config.Configuration{
			Resources: &config.Resources{
				ExcludeResourcePrefixes: []string{
					"networking.example.com/",
					"storage.example.com/",
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configuration))

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "exclude-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("test-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "10Gi").
					Resource("networking.other.com/vpc", "1000").
					Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("should admit workload ignoring excluded resource", func() {
		ginkgo.By("Creating a workload that requests an excluded resource")
		wl := utiltestingapi.MakeWorkload("test-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").
			Request("networking.example.com/vpc", "100").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		wlKey := types.NamespacedName{
			Name:      wl.Name,
			Namespace: ns.Name,
		}

		ginkgo.By("Verifying workload gets admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying excluded resource is not in admission resource usage")
		gomega.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
		gomega.Expect(resourceUsage).NotTo(gomega.HaveKey("networking.example.com/vpc"))
	})

	ginkgo.It("should ignore multiple excluded resource prefixes", func() {
		ginkgo.By("Creating a workload that requests resources from multiple excluded prefixes")
		wl := utiltestingapi.MakeWorkload("multi-exclude-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "2").
			Request("networking.example.com/vpc", "10").
			Request("storage.example.com/disk", "50").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		wlKey := types.NamespacedName{
			Name:      wl.Name,
			Namespace: ns.Name,
		}

		ginkgo.By("Verifying workload admission")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying both excluded resources are not in resource usage")
		gomega.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
		gomega.Expect(resourceUsage).NotTo(gomega.HaveKey("networking.example.com/vpc"))
		gomega.Expect(resourceUsage).NotTo(gomega.HaveKey("storage.example.com/disk"))
	})

	ginkgo.It("should enforce quota for non-excluded resources", func() {
		ginkgo.By("Creating a workload that consumes most of the CPU quota")
		wl1 := utiltestingapi.MakeWorkload("quota-wl-1", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "8").
			Request("networking.example.com/vpc", "1000").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

		wl1Key := types.NamespacedName{Name: wl1.Name, Namespace: ns.Name}

		ginkgo.By("Waiting for first workload to be admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl1Key, wl1)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl1)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating a second workload that would exceed CPU quota")
		wl2 := utiltestingapi.MakeWorkload("quota-wl-2", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "5").
			Request("networking.example.com/vpc", "1").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

		wl2Key := types.NamespacedName{Name: wl2.Name, Namespace: ns.Name}

		ginkgo.By("Verifying second workload is not admitted due to CPU quota")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl2Key, wl2)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl2)).Should(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("should use exact prefix matching", func() {
		ginkgo.By("Creating a workload with a resource that doesn't match the excluded prefix")
		wl := utiltestingapi.MakeWorkload("prefix-test-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").
			Request("networking.other.com/vpc", "100").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		wlKey := types.NamespacedName{Name: wl.Name, Namespace: ns.Name}

		ginkgo.By("Verifying workload is admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying non-matching resource is NOT excluded (appears in resource usage)")
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceName("networking.other.com/vpc")))
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
	})
})
