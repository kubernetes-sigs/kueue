/*
Copyright 2024 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/testing"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Pod groups", func() {
	var (
		ns         *corev1.Namespace
		onDemandRF *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		if kubeVersion().LessThan(kubeversion.KubeVersion1_27) {
			ginkgo.Skip("Unsupported in versions older than 1.27")
		}
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		onDemandRF = testing.MakeResourceFlavor("on-demand").Label("instance-type", "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandRF)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
	})

	ginkgo.When("Single CQ", func() {
		var (
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should admit group that fits", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "1").
				MakeGroup(2)
			gKey := client.ObjectKey{Namespace: ns.Name, Name: "group"}
			for _, p := range group {
				gomega.Expect(k8sClient.Create(ctx, p)).To(gomega.Succeed())
				gomega.Expect(p.Spec.SchedulingGates).
					To(gomega.ContainElement(corev1.PodSchedulingGate{
						Name: pod.SchedulingGateName}))
			}
			ginkgo.By("Starting admission", func() {
				// Verify that the Pods start with the appropriate selector.
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
							"instance-type": "on-demand",
						}))
					}
				}).Should(gomega.Succeed())

				util.ExpectWorkloadToFinish(ctx, k8sClient, gKey)
			})

			ginkgo.By("Deleting finished Pods", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Delete(ctx, p)).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, p := range group {
						var pCopy corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &pCopy)).To(testing.BeNotFoundError())
					}
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, gKey, &wl)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval)
			})
		})

		ginkgo.It("Should only admit a complete group", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "1").
				MakeGroup(3)

			ginkgo.By("Incomplete group should not start", func() {
				// Create incomplete group.
				for _, p := range group[:2] {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}
				gomega.Consistently(func(g gomega.Gomega) {
					for _, origPod := range group[:2] {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).
							To(gomega.ContainElement(corev1.PodSchedulingGate{
								Name: pod.SchedulingGateName}))
					}
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Incomplete group can be deleted", func() {
				for _, p := range group[:2] {
					gomega.Expect(k8sClient.Delete(ctx, p)).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group[:2] {
						var p corev1.Pod
						err := k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)
						g.Expect(err).To(testing.BeNotFoundError())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Complete group runs successfully", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}

				util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"})
			})
		})

		ginkgo.It("Failed Pod can be replaced in group", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "1").
				MakeGroup(3)

			// First pod runs for much longer, so that there is time to terminate it.
			group[0].Spec.Containers[0].Args = []string{"-termination-code=1", "10m"}

			ginkgo.By("Group starts", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Fail a pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, group[0])).To(gomega.Succeed())
				gomega.Eventually(func() corev1.PodPhase {
					var p corev1.Pod
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					return p.Status.Phase
				}, util.Timeout, util.Interval).Should(gomega.Equal(corev1.PodFailed))
			})

			ginkgo.By("Replacement pod starts", func() {
				// Use a pod template that can succeed fast.
				rep := group[2].DeepCopy()
				rep.Name = "replacement"
				gomega.Expect(k8sClient.Create(ctx, rep)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
					g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Excess pod is deleted", func() {
				excess := group[2].DeepCopy()
				excess.Name = "excess"
				gomega.Expect(k8sClient.Create(ctx, excess)).To(gomega.Succeed())
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(excess), &corev1.Pod{})
				}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
			})

			util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"})
		})

		ginkgo.It("should allow to schedule a group of diverse pods", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "3").
				MakeGroup(2)
			gKey := client.ObjectKey{Namespace: ns.Name, Name: "group"}

			// make the group of pods diverse using different amount of resources
			group[0].Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")

			ginkgo.By("Group starts", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Group completes", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"})
			})
			ginkgo.By("Deleting finished Pods", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Delete(ctx, p)).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, p := range group {
						var pCopy corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &pCopy)).To(testing.BeNotFoundError())
					}
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, gKey, &wl)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval)
			})
		})

		ginkgo.It("should allow to preempt the lower priority group", func() {
			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass))
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			defaultPriorityGroup := podtesting.MakePod("default-priority-group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"-termination-code=1", "10m"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "2").
				MakeGroup(2)

			ginkgo.By("Default-priority group starts", func() {
				for _, p := range defaultPriorityGroup {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range defaultPriorityGroup {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highPriorityGroup := podtesting.MakePod("high-priority-group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"1ms"}).
				Queue(lq.Name).
				PriorityClass("high").
				Request(corev1.ResourceCPU, "1").
				MakeGroup(2)
			highGroupKey := client.ObjectKey{Namespace: ns.Name, Name: "high-priority-group"}

			ginkgo.By("High-priority group starts", func() {
				for _, p := range highPriorityGroup {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range highPriorityGroup {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("High priority group completes", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, highGroupKey)
			})

			var replacementPods []*corev1.Pod
			ginkgo.By("Recreate the replacement pods for the default-priority group", func() {
				for i := range defaultPriorityGroup {
					rep := defaultPriorityGroup[i].DeepCopy()
					// For replacement pods use image that completes fast.
					rep.Name = "replacement-for-" + rep.Name
					rep.Spec.Containers[0].Args = []string{"3s"}
					replacementPods = append(replacementPods, rep)
					gomega.Expect(k8sClient.Create(ctx, rep)).To(gomega.Succeed())
				}
			})

			ginkgo.By("Await for the replacement pods to be ungated", func() {
				for _, rep := range replacementPods {
					gomega.Eventually(func(g gomega.Gomega) {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})

			// TODO(#1557): verify the default-priority workload is finished
			ginkgo.By("Verify the replacement pods of the default priority workload complete", func() {
				for _, rep := range replacementPods {
					gomega.Eventually(func(g gomega.Gomega) {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
						g.Expect(p.Status.Phase).To(gomega.Equal(v1.PodSucceeded))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})
		})
	})
})

func kubeVersion() *version.Version {
	cfg, err := config.GetConfigWithContext("")
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	v, err := kubeversion.FetchServerVersion(discoveryClient)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return v
}
