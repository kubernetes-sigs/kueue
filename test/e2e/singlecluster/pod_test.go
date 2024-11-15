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
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/util/testing"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Pod groups", func() {
	var (
		ns         *corev1.Namespace
		onDemandRF *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		onDemandRF = testing.MakeResourceFlavor("on-demand").NodeLabel("instance-type", "on-demand").Obj()
		gomega.Expect(k8sClient.Create(ctx, onDemandRF)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should admit group that fits", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
							"instance-type": "on-demand",
						}))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, gKey)
			})
		})

		ginkgo.It("Should only admit a complete group", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
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
						g.Expect(p.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: pod.SchedulingGateName}))
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(testing.BeNotFoundError())
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

			groupName := "group"
			group := podtesting.MakePod(groupName, ns.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
				TerminationGracePeriod(1).
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Wait for the pod to be running to allow fast termination by Kubelet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Fail a pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, group[0])).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).Should(gomega.Equal(corev1.PodFailed))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}

			ginkgo.By("Checking that WaitingForReplacementPods status is set to true", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:    pod.WorkloadWaitingForReplacementPods,
							Status:  metav1.ConditionTrue,
							Reason:  pod.WorkloadPodsFailed,
							Message: "Some Failed pods need replacement",
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectPodsFinalizedOrGone(ctx, k8sClient, client.ObjectKeyFromObject(group[0]))
			})

			ginkgo.By("Excess pod is deleted", func() {
				excess := group[2].DeepCopy()
				excess.Name = "excess"
				excessPods := sets.New(client.ObjectKeyFromObject(excess))
				ginkgo.By("Create the excess pod", func() {
					gomega.Expect(k8sClient.Create(ctx, excess)).To(gomega.Succeed())
				})
				ginkgo.By("Use events to observe the excess pods are getting stopped", func() {
					util.ExpectEventsForObjects(eventWatcher, excessPods, func(e *corev1.Event) bool {
						return e.InvolvedObject.Namespace == ns.Name && e.Reason == "ExcessPodDeleted"
					})
				})
				ginkgo.By("Verify the excess pod is deleted", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, excess, false)
				})
			})

			ginkgo.By("Checking that WaitingForReplacementPods status is set to false", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:    pod.WorkloadWaitingForReplacementPods,
							Status:  metav1.ConditionFalse,
							Reason:  kueue.WorkloadPodsReady,
							Message: "No pods need replacement",
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"})
		})

		ginkgo.It("Unscheduled Pod which is deleted can be replaced in group", func() {
			eventList := corev1.EventList{}
			eventWatcher, err := k8sClient.Watch(ctx, &eventList, &client.ListOptions{
				Namespace: ns.Name,
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.DeferCleanup(func() {
				eventWatcher.Stop()
			})

			group := podtesting.MakePod("group", ns.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "1").
				MakeGroup(2)

			// The first pod has a node selector for a missing node.
			group[0].Spec.NodeSelector = map[string]string{"missing-node-key": "missing-node-value"}

			ginkgo.By("Group starts", func() {
				for _, p := range group {
					gomega.Expect(k8sClient.Create(ctx, p.DeepCopy())).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the second pod is no longer pending", func() {
				// Since kueue is not involved in this transition (ungated pod to no pending)
				// it is acceptable to wait `LongTimeout` for it to happen.
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[1]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).NotTo(gomega.Equal(corev1.PodPending))
					g.Expect(p.Spec.NodeName).NotTo(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the first pod is Unschedulable", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodPending))
					g.Expect(p.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonUnschedulable,
					}, cmpopts.IgnoreFields(corev1.PodCondition{}, "LastProbeTime", "LastTransitionTime", "Message"))))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the pod it remains Unschedulable", func() {
				gomega.Expect(k8sClient.Delete(ctx, group[0])).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.DeletionTimestamp.IsZero()).NotTo(gomega.BeTrue())
					g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodPending))
					g.Expect(p.Spec.NodeName).To(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Replacement pod is un-gated, and the failed one is deleted", func() {
				rep := group[0].DeepCopy()
				rep.Name = "replacement"
				gomega.Expect(k8sClient.Create(ctx, rep)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
					g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(testing.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to schedule a group of diverse pods", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
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
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, gKey)
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
				Image(util.E2eTestSleepImage, []string{"-termination-code=1", "10m"}).
				TerminationGracePeriod(1).
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highPriorityGroup := podtesting.MakePod("high-priority-group", ns.Name).
				Image(util.E2eTestSleepImage, []string{"1ms"}).
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
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
				util.ExpectEventsForObjects(eventWatcher, defaultGroupPods, func(e *corev1.Event) bool {
					return e.InvolvedObject.Namespace == ns.Name && e.Reason == "Stopped"
				})
			})

			ginkgo.By("Wait for default-priority pods to fail", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range defaultPriorityGroup {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodFailed))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			replacementPods := make([]client.ObjectKey, 0, len(defaultPriorityGroup))

			ginkgo.By("Create replacement pods", func() {
				for _, origPod := range defaultPriorityGroup {
					rep := origPod.DeepCopy()
					rep.Name = "replacement-for-" + rep.Name
					rep.Spec.Containers[0].Args = []string{"1ms"}
					gomega.Expect(k8sClient.Create(ctx, rep)).To(gomega.Succeed())
					replacementPods = append(replacementPods, client.ObjectKeyFromObject(rep))
				}
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
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
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
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Verify the default priority workload is finished", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, defaultGroupKey)
			})
		})
	})
})
