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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
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
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			eventList := corev1.EventList{}
			eventWatcher, err := k8sClient.Watch(ctx, &eventList, &client.ListOptions{
				Namespace: ns.Name,
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.DeferCleanup(func() {
				eventWatcher.Stop()
			})

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

			ginkgo.By("Replacement pod starts, and the failed one is deleted", func() {
				// Use a pod template that can succeed fast.
				rep := group[2].DeepCopy()
				rep.Name = "replacement"
				gomega.Expect(k8sClient.Create(ctx, rep)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
					g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Excess pod is deleted", func() {
				excess := group[2].DeepCopy()
				excess.Name = "excess"
				excessPods := sets.New(client.ObjectKeyFromObject(excess))
				ginkgo.By("Create the excess pod", func() {
					gomega.Expect(k8sClient.Create(ctx, excess)).To(gomega.Succeed())
				})
				ginkgo.By("Use events to observe the excess pods are getting stopped", func() {
					preemptedPods := sets.New[types.NamespacedName]()
					gomega.Eventually(func(g gomega.Gomega) sets.Set[types.NamespacedName] {
						select {
						case evt, ok := <-eventWatcher.ResultChan():
							g.Expect(ok).To(gomega.BeTrue())
							event, ok := evt.Object.(*corev1.Event)
							g.Expect(ok).To(gomega.BeTrue())
							if event.InvolvedObject.Namespace == ns.Name && event.Reason == "ExcessPodDeleted" {
								objKey := types.NamespacedName{Namespace: event.InvolvedObject.Namespace, Name: event.InvolvedObject.Name}
								preemptedPods.Insert(objKey)
							}
						default:
						}
						return preemptedPods
					}, util.Timeout, util.Interval).Should(gomega.Equal(excessPods))
				})
				ginkgo.By("Verify the excess pod is deleted", func() {
					gomega.Eventually(func() error {
						return k8sClient.Get(ctx, client.ObjectKeyFromObject(excess), &corev1.Pod{})
					}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
				})
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
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to preempt the lower priority group", func() {
			eventList := corev1.EventList{}
			eventWatcher, err := k8sClient.Watch(ctx, &eventList, &client.ListOptions{
				Namespace: ns.Name,
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.DeferCleanup(func() {
				eventWatcher.Stop()
			})

			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			defaultPriorityGroup := podtesting.MakePod("default-priority-group", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.1.0", []string{"-termination-code=1", "10min"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "2").
				MakeGroup(2)
			defaultGroupKey := client.ObjectKey{Namespace: ns.Name, Name: "default-priority-group"}
			defaultGroupPods := sets.New[types.NamespacedName](
				client.ObjectKeyFromObject(defaultPriorityGroup[0]),
				client.ObjectKeyFromObject(defaultPriorityGroup[1]),
			)

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

			ginkgo.By("Create the high-priority group", func() {
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

			ginkgo.By("The default priority workload is preempted", func() {
				var updatedWorkload kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, defaultGroupKey, &updatedWorkload)).To(gomega.Succeed())
				util.ExpectWorkloadsToBePreempted(ctx, k8sClient, &updatedWorkload)
			})

			ginkgo.By("Use events to observe the default-priority pods are getting preempted", func() {
				preemptedPods := sets.New[types.NamespacedName]()
				gomega.Eventually(func(g gomega.Gomega) sets.Set[types.NamespacedName] {
					select {
					case evt, ok := <-eventWatcher.ResultChan():
						g.Expect(ok).To(gomega.BeTrue())
						event, ok := evt.Object.(*corev1.Event)
						g.Expect(ok).To(gomega.BeTrue())
						if event.InvolvedObject.Namespace == ns.Name && event.Reason == "Stopped" {
							objKey := types.NamespacedName{Namespace: event.InvolvedObject.Namespace, Name: event.InvolvedObject.Name}
							preemptedPods.Insert(objKey)
						}
					default:
					}
					return preemptedPods
				}, util.Timeout, util.Interval).Should(gomega.Equal(defaultGroupPods))
			})

			replacementPods := make(map[types.NamespacedName]types.NamespacedName, len(defaultPriorityGroup))
			ginkgo.By("Create replacement pods as soon as the default-priority pods are Failed", func() {
				gomega.Eventually(func(g gomega.Gomega) int {
					for _, origPod := range defaultPriorityGroup {
						origKey := client.ObjectKeyFromObject(origPod)
						if _, found := replacementPods[origKey]; !found {
							var p corev1.Pod
							g.Expect(k8sClient.Get(ctx, origKey, &p)).To(gomega.Succeed())
							if p.Status.Phase == corev1.PodFailed {
								rep := origPod.DeepCopy()
								// For replacement pods use args that let it complete fast.
								rep.Name = "replacement-for-" + rep.Name
								rep.Spec.Containers[0].Args = []string{"1ms"}
								g.Expect(k8sClient.Create(ctx, rep)).To(gomega.Succeed())
								replacementPods[origKey] = client.ObjectKeyFromObject(rep)
							}
						}
					}
					return len(replacementPods)
				}, util.Timeout, util.Interval).Should(gomega.Equal(len(defaultPriorityGroup)))
			})
			ginkgo.By("Check that the preempted pods are deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					for _, origPod := range defaultPriorityGroup {
						origKey := client.ObjectKeyFromObject(origPod)
						g.Expect(k8sClient.Get(ctx, origKey, &p)).To(testing.BeNotFoundError())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the high-priority pods are scheduled", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range highPriorityGroup {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the high priority group completes", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, highGroupKey)
			})

			ginkgo.By("Await for the replacement pods to be ungated", func() {
				for _, replKey := range replacementPods {
					gomega.Eventually(func(g gomega.Gomega) {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, replKey, &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Verify the replacement pods of the default priority workload complete", func() {
				for _, replKey := range replacementPods {
					gomega.Eventually(func(g gomega.Gomega) {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, replKey, &p)).To(gomega.Succeed())
						g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Verify the default priority workload is finished", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, defaultGroupKey)
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
