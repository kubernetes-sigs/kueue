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

package baseline

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("WAS PodGroups", ginkgo.Label("area:was", "feature:was", "feature:pod"), func() {
	var (
		ns         *corev1.Namespace
		rf         *kueue.ResourceFlavor
		flavorName string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "was-e2e-")
		flavorName = "on-demand-" + ns.Name
		rf = utiltestingapi.MakeResourceFlavor(flavorName).NodeLabel("instance-type", "on-demand").Obj()
		util.MustCreate(ctx, k8sClient, rf)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Pod group opts in to WAS PodGroups", func() {
		var (
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = utiltestingapi.MakeClusterQueue("cq-" + ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorName).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("should create a native PodGroup and default podGroupName on pods", func() {
			groupName := "test-group"
			numPods := 3
			group := podtesting.MakePod(groupName, ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				WASPodGroupAnnotation().
				MakeGroup(numPods)

			ginkgo.By("Creating the pod group")
			for _, p := range group {
				util.MustCreate(ctx, k8sClient, p)
			}

			ginkgo.By("Verifying that pods have schedulingGroup.podGroupName defaulted")
			for i := range numPods {
				podKey := types.NamespacedName{Namespace: ns.Name, Name: groupName + fmt.Sprintf("-%d", i)}
				gomega.Eventually(func(g gomega.Gomega) {
					var pod corev1.Pod
					g.Expect(k8sClient.Get(ctx, podKey, &pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.SchedulingGroup).NotTo(gomega.BeNil(), "schedulingGroup should be set")
					g.Expect(ptr.Deref(pod.Spec.SchedulingGroup.PodGroupName, "")).To(
						gomega.Equal(groupName),
						"podGroupName should match the pod group name",
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verifying the workload is created")
			wlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			createdWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, createdWl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying a native scheduling.k8s.io Workload is created")
			nativeWlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			nativeWl := &schedulingv1alpha3.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, nativeWlKey, nativeWl)).To(gomega.Succeed())
				g.Expect(nativeWl.Labels).To(gomega.HaveKeyWithValue(
					constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue,
				), "Workload should be managed by Kueue")
				g.Expect(nativeWl.Spec.PodGroupTemplates).To(gomega.HaveLen(1), "Workload should have one PodGroupTemplate")
				g.Expect(nativeWl.Spec.PodGroupTemplates[0].SchedulingPolicy.Gang).NotTo(gomega.BeNil(), "template Gang policy should be set")
				g.Expect(nativeWl.Spec.PodGroupTemplates[0].SchedulingPolicy.Gang.MinCount).To(
					gomega.Equal(int32(numPods)),
					"template MinCount should equal the total number of pods in the group",
				)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the native Workload is owned by the Kueue workload")
			gomega.Expect(nativeWl.OwnerReferences).To(gomega.ContainElement(
				gomega.SatisfyAll(
					gomega.HaveField("Name", createdWl.Name),
					gomega.HaveField("Kind", "Workload"),
				),
			))

			ginkgo.By("Verifying a native scheduling.k8s.io PodGroup is created and linked to the Workload")
			pgKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			podGroup := &schedulingv1alpha3.PodGroup{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, pgKey, podGroup)).To(gomega.Succeed())
				g.Expect(podGroup.Labels).To(gomega.HaveKeyWithValue(
					constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue,
				), "PodGroup should be managed by Kueue")
				g.Expect(podGroup.Spec.SchedulingPolicy.Gang).NotTo(gomega.BeNil(), "Gang scheduling policy should be set")
				g.Expect(podGroup.Spec.SchedulingPolicy.Gang.MinCount).To(
					gomega.Equal(int32(numPods)),
					"MinCount should equal the total number of pods in the group",
				)
				g.Expect(podGroup.Spec.PodGroupTemplateRef).NotTo(gomega.BeNil(), "PodGroup should reference a template")
				g.Expect(podGroup.Spec.PodGroupTemplateRef.Workload).NotTo(gomega.BeNil(), "PodGroup should reference the native Workload")
				g.Expect(podGroup.Spec.PodGroupTemplateRef.Workload.WorkloadName).To(
					gomega.Equal(nativeWl.Name),
					"PodGroup should reference the created native Workload",
				)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the PodGroup is owned by the workload")
			gomega.Expect(podGroup.OwnerReferences).To(gomega.ContainElement(
				gomega.SatisfyAll(
					gomega.HaveField("Name", createdWl.Name),
					gomega.HaveField("Kind", "Workload"),
				),
			))

			ginkgo.By("Verifying the workload finishes")
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, util.LongTimeout)
		})

		ginkgo.It("should preserve externally set podGroupName and not create a native PodGroup", func() {
			groupName := "ext-group"
			externalPGName := "external-pg"
			numPods := 2
			group := podtesting.MakePod(groupName, ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				WASPodGroupAnnotation().
				SchedulingGroupPodGroupName(externalPGName).
				MakeGroup(numPods)

			ginkgo.By("Creating the pod group with pre-set podGroupName")
			for _, p := range group {
				util.MustCreate(ctx, k8sClient, p)
			}

			ginkgo.By("Verifying that pods retain the externally set podGroupName")
			for i := range numPods {
				podKey := types.NamespacedName{Namespace: ns.Name, Name: groupName + fmt.Sprintf("-%d", i)}
				gomega.Eventually(func(g gomega.Gomega) {
					var pod corev1.Pod
					g.Expect(k8sClient.Get(ctx, podKey, &pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.SchedulingGroup).NotTo(gomega.BeNil(), "schedulingGroup should be set")
					g.Expect(ptr.Deref(pod.Spec.SchedulingGroup.PodGroupName, "")).To(
						gomega.Equal(externalPGName),
						"podGroupName should be the externally set value",
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verifying the workload finishes")
			wlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, util.LongTimeout)
		})

		ginkgo.It("should not set podGroupName when WAS annotation is absent", func() {
			groupName := "no-was-group"
			numPods := 2
			group := podtesting.MakePod(groupName, ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				MakeGroup(numPods)

			ginkgo.By("Creating the pod group without WAS annotation")
			for _, p := range group {
				util.MustCreate(ctx, k8sClient, p)
			}

			ginkgo.By("Verifying that pods do not have schedulingGroup set")
			for i := range numPods {
				podKey := types.NamespacedName{Namespace: ns.Name, Name: groupName + fmt.Sprintf("-%d", i)}
				gomega.Eventually(func(g gomega.Gomega) {
					var pod corev1.Pod
					g.Expect(k8sClient.Get(ctx, podKey, &pod)).To(gomega.Succeed())
					// Pod should not have schedulingGroup set since WAS annotation is absent.
					if pod.Spec.SchedulingGroup != nil {
						g.Expect(ptr.Deref(pod.Spec.SchedulingGroup.PodGroupName, "")).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verifying the workload finishes")
			wlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, util.LongTimeout)
		})

		ginkgo.It("should create a native PodGroup with correct minCount for multi-pod group", func() {
			groupName := "large-group"
			numPods := 4
			group := podtesting.MakePod(groupName, ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				WASPodGroupAnnotation().
				MakeGroup(numPods)

			ginkgo.By("Creating the pod group")
			for _, p := range group {
				util.MustCreate(ctx, k8sClient, p)
			}

			ginkgo.By("Verifying the native PodGroup has the correct minCount")
			pgKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			gomega.Eventually(func(g gomega.Gomega) {
				podGroup := &schedulingv1alpha3.PodGroup{}
				g.Expect(k8sClient.Get(ctx, pgKey, podGroup)).To(gomega.Succeed())
				g.Expect(podGroup.Spec.SchedulingPolicy.Gang).NotTo(gomega.BeNil())
				g.Expect(podGroup.Spec.SchedulingPolicy.Gang.MinCount).To(gomega.Equal(int32(numPods)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying all pods have consistent podGroupName")
			for i := range numPods {
				podKey := types.NamespacedName{Namespace: ns.Name, Name: groupName + fmt.Sprintf("-%d", i)}
				gomega.Eventually(func(g gomega.Gomega) {
					var pod corev1.Pod
					g.Expect(k8sClient.Get(ctx, podKey, &pod)).To(gomega.Succeed())
					g.Expect(pod.Spec.SchedulingGroup).NotTo(gomega.BeNil())
					g.Expect(ptr.Deref(pod.Spec.SchedulingGroup.PodGroupName, "")).To(gomega.Equal(groupName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Verifying the workload finishes")
			wlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, util.LongTimeout)
		})

		ginkgo.It("should map the Kueue workload priority onto the native Workload and PodGroup", func() {
			const priorityValue int32 = 12345
			wpc := utiltestingapi.MakeWorkloadPriorityClass("was-high-" + ns.Name).PriorityValue(priorityValue).Obj()
			util.MustCreate(ctx, k8sClient, wpc)
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wpc, true)
			})

			groupName := "prio-group"
			numPods := 2
			group := podtesting.MakePod(groupName, ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				WASPodGroupAnnotation().
				WorkloadPriorityClass(wpc.Name).
				MakeGroup(numPods)

			ginkgo.By("Creating the pod group with a workload priority class")
			for _, p := range group {
				util.MustCreate(ctx, k8sClient, p)
			}

			ginkgo.By("Verifying the Kueue workload carries the resolved priority")
			wlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			createdWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, createdWl)).To(gomega.Succeed())
				g.Expect(createdWl.Spec.Priority).NotTo(gomega.BeNil())
				g.Expect(*createdWl.Spec.Priority).To(gomega.Equal(priorityValue))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the native Workload template priority matches")
			nativeWlKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			gomega.Eventually(func(g gomega.Gomega) {
				nativeWl := &schedulingv1alpha3.Workload{}
				g.Expect(k8sClient.Get(ctx, nativeWlKey, nativeWl)).To(gomega.Succeed())
				g.Expect(nativeWl.Spec.PodGroupTemplates).To(gomega.HaveLen(1))
				g.Expect(ptr.Deref(nativeWl.Spec.PodGroupTemplates[0].Priority, 0)).To(gomega.Equal(priorityValue))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the native PodGroup priority matches")
			pgKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}
			gomega.Eventually(func(g gomega.Gomega) {
				podGroup := &schedulingv1alpha3.PodGroup{}
				g.Expect(k8sClient.Get(ctx, pgKey, podGroup)).To(gomega.Succeed())
				g.Expect(ptr.Deref(podGroup.Spec.Priority, 0)).To(gomega.Equal(priorityValue))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the workload finishes")
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlKey, util.LongTimeout)
		})
	})
})
