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
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	podtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Pod groups", ginkgo.Label("area:singlecluster", "feature:pod"), func() {
	var (
		ns             *corev1.Namespace
		onDemandRF     *kueue.ResourceFlavor
		flavorOnDemand string
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-e2e-")
		flavorOnDemand = "on-demand-" + ns.Name
		onDemandRF = utiltestingapi.MakeResourceFlavor(flavorOnDemand).NodeLabel("instance-type", "on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, onDemandRF)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Single CQ", func() {
		var (
			cq               *kueue.ClusterQueue
			lq               *kueue.LocalQueue
			clusterQueueName string
		)

		ginkgo.BeforeEach(func() {
			clusterQueueName = "cq-" + ns.Name
			cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("should admit group that fits", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				MakeGroup(2)
			gKey := client.ObjectKey{Namespace: ns.Name, Name: "group"}
			for _, p := range group {
				behavioral.MustCreate(ctx, k8sClient, p)
				gomega.Expect(p.Spec.SchedulingGates).
					To(gomega.ContainElement(corev1.PodSchedulingGate{
						Name: podconstants.SchedulingGateName}))
			}
			ginkgo.By("Verify that the Workload is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, gKey, &kueue.Workload{})).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Verify that the Workload is admitted", func() {
				behavioral.ExpectWorkloadsToHaveQuotaReservationByKey(ctx, k8sClient, cq.Name, gKey)
			})
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
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, gKey, behavioral.LongTimeout)
			})

			ginkgo.By("Deleting finished Pods", func() {
				for _, p := range group {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, p, true)
				}
				behavioral.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, gKey)
			})
		})

		ginkgo.It("Should only admit a complete group", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				MakeGroup(3)

			ginkgo.By("Incomplete group should not start", func() {
				// Create incomplete group.
				for _, p := range group[:2] {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}
				createdPod := &corev1.Pod{}
				gomega.Consistently(func(g gomega.Gomega) {
					for _, origPod := range group[:2] {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), createdPod)).To(gomega.Succeed())
						g.Expect(createdPod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
					}
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
			ginkgo.By("Incomplete group can be deleted", func() {
				for _, p := range group[:2] {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, p, true)
				}
			})
			ginkgo.By("Complete group runs successfully", func() {
				for _, p := range group {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}

				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"}, behavioral.LongTimeout)
			})
		})

		ginkgo.It("Failed Pod can be replaced in group", func() {
			eventList := eventsv1.EventList{}
			eventWatcher, err := k8sClient.Watch(ctx, &eventList, &client.ListOptions{
				Namespace: ns.Name,
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.DeferCleanup(func() {
				eventWatcher.Stop()
			})

			groupName := "group"
			group := podtesting.MakePod(groupName, ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				TerminationGracePeriod(1).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				MakeGroup(3)

			// First pod runs for much longer, so that there is time to terminate it.
			group[0].Spec.Containers[0].Args = behavioral.BehaviorWaitForDeletionFailOnExit

			ginkgo.By("Group starts", func() {
				for _, p := range group {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Wait for the pod to be running to allow fast termination by Kubelet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Fail a pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, group[0])).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).Should(gomega.Equal(corev1.PodFailed))
				}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Namespace: ns.Name, Name: groupName}

			ginkgo.By("Checking that WaitingForReplacementPods status is set to true", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:    kueue.WorkloadWaitingForReplacementPods,
							Status:  metav1.ConditionTrue,
							Reason:  pod.WorkloadPodsFailed,
							Message: "Some Failed pods need replacement",
						}, behavioral.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Replacement pod starts, and the failed one is deleted", func() {
				// Use a pod template that can succeed fast.
				rep := group[2].DeepCopy()
				rep.Name = "replacement"
				behavioral.MustCreate(ctx, k8sClient, rep)
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
					g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectPodsFinalizedOrGone(ctx, k8sClient, client.ObjectKeyFromObject(group[0]))
			})

			ginkgo.By("Excess pod is deleted", func() {
				excess := group[2].DeepCopy()
				excess.Name = "excess"
				excessPods := sets.New(client.ObjectKeyFromObject(excess))
				ginkgo.By("Create the excess pod", func() {
					behavioral.MustCreate(ctx, k8sClient, excess)
				})
				ginkgo.By("Use events to observe the excess pods are getting stopped", func() {
					behavioral.ExpectEventsForObjectsWithTimeout(eventWatcher, excessPods, func(e *eventsv1.Event) bool {
						return e.Regarding.Namespace == ns.Name && e.Reason == pod.ReasonExcessPodDeleted
					}, behavioral.MediumTimeout)
				})
				ginkgo.By("Verify the excess pod is deleted", func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, excess, false)
				})
			})

			ginkgo.By("Checking that WaitingForReplacementPods status is set to false", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:    kueue.WorkloadWaitingForReplacementPods,
							Status:  metav1.ConditionFalse,
							Reason:  kueue.WorkloadPodsReady,
							Message: "No pods need replacement",
						}, behavioral.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"}, behavioral.LongTimeout)
		})

		ginkgo.It("Unscheduled Pod which is deleted can be replaced in group", func() {
			eventList := eventsv1.EventList{}
			eventWatcher, err := k8sClient.Watch(ctx, &eventList, &client.ListOptions{
				Namespace: ns.Name,
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.DeferCleanup(func() {
				eventWatcher.Stop()
			})

			group := podtesting.MakePod("group", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				MakeGroup(2)

			// The first pod has a node selector for a missing node.
			group[0].Spec.NodeSelector = map[string]string{"missing-node-key": "missing-node-value"}

			ginkgo.By("Group starts", func() {
				for _, p := range group {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check the second pod is no longer pending", func() {
				// Since kueue is not involved in this transition (ungated pod to no pending)
				// it is acceptable to wait `LongTimeout` for it to happen.
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[1]), &p)).To(gomega.Succeed())
					g.Expect(p.Status.Phase).NotTo(gomega.Equal(corev1.PodPending))
					g.Expect(p.Spec.NodeName).NotTo(gomega.BeEmpty())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
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
					}, behavioral.IgnorePodConditionTimestampsMessageAndObservedGeneration)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the pod it remains Unschedulable", func() {
				gomega.Expect(k8sClient.Delete(ctx, group[0])).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(gomega.Succeed())
					g.Expect(p.DeletionTimestamp.IsZero()).NotTo(gomega.BeTrue())
					g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodPending))
					g.Expect(p.Spec.NodeName).To(gomega.BeEmpty())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Replacement pod is un-gated, and the failed one is deleted", func() {
				rep := group[0].DeepCopy()
				rep.Name = "replacement"
				behavioral.MustCreate(ctx, k8sClient, rep)
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rep), &p)).To(gomega.Succeed())
					g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(group[0]), &p)).To(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to schedule a group of diverse pods", func() {
			group := podtesting.MakePod("group", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "3").
				MakeGroup(2)
			gKey := client.ObjectKey{Namespace: ns.Name, Name: "group"}

			// make the group of pods diverse using different amount of resources
			group[0].Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")

			ginkgo.By("Group starts", func() {
				for _, p := range group {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range group {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Group completes", func() {
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, client.ObjectKey{Namespace: ns.Name, Name: "group"}, behavioral.LongTimeout)
			})
			ginkgo.By("Deleting finished Pods", func() {
				for _, p := range group {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, p, true)
				}
				behavioral.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, gKey)
			})
		})

		ginkgo.It("should allow to preempt the lower priority group", func() {
			eventList := eventsv1.EventList{}
			eventWatcher, err := k8sClient.Watch(ctx, &eventList, &client.ListOptions{
				Namespace: ns.Name,
			})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ginkgo.DeferCleanup(func() {
				eventWatcher.Stop()
			})

			highWorkloadPriorityClass := utiltestingapi.MakeWorkloadPriorityClass("high-" + ns.Name).PriorityValue(100).Obj()
			behavioral.MustCreate(ctx, k8sClient, highWorkloadPriorityClass)
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highWorkloadPriorityClass)).To(gomega.Succeed())
			})

			defaultPriorityGroup := podtesting.MakePod("default-priority-group", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletionFailOnExit).
				TerminationGracePeriod(1).
				Queue(lq.Name).
				RequestAndLimit(corev1.ResourceCPU, "2").
				MakeGroup(2)
			defaultGroupKey := client.ObjectKey{Namespace: ns.Name, Name: "default-priority-group"}
			defaultGroupPods := sets.New(
				client.ObjectKeyFromObject(defaultPriorityGroup[0]),
				client.ObjectKeyFromObject(defaultPriorityGroup[1]),
			)

			ginkgo.By("Default-priority group starts", func() {
				for _, p := range defaultPriorityGroup {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range defaultPriorityGroup {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			highPriorityGroup := podtesting.MakePod("high-priority-group", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				Queue(lq.Name).
				WorkloadPriorityClass(highWorkloadPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				MakeGroup(2)
			highGroupKey := client.ObjectKey{Namespace: ns.Name, Name: "high-priority-group"}

			ginkgo.By("Create the high-priority group", func() {
				for _, p := range highPriorityGroup {
					behavioral.MustCreate(ctx, k8sClient, p.DeepCopy())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range highPriorityGroup {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("The default priority workload is preempted", func() {
				var updatedWorkload kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, defaultGroupKey, &updatedWorkload)).To(gomega.Succeed())
				behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, &updatedWorkload)
			})

			ginkgo.By("Use events to observe the default-priority pods are getting preempted", func() {
				behavioral.ExpectEventsForObjectsWithTimeout(eventWatcher, defaultGroupPods, func(e *eventsv1.Event) bool {
					return e.Regarding.Namespace == ns.Name && e.Reason == jobframework.ReasonStopped && strings.Contains(e.Note, "Preempted")
				}, behavioral.MediumTimeout)
			})

			ginkgo.By("Wait for default-priority pods to fail", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range defaultPriorityGroup {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodFailed), fmt.Sprintf("%#v", p.Status))
					}
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			replacementPods := make([]client.ObjectKey, 0, len(defaultPriorityGroup))

			ginkgo.By("Create replacement pods", func() {
				for _, origPod := range defaultPriorityGroup {
					rep := origPod.DeepCopy()
					rep.Name = "replacement-for-" + rep.Name
					rep.Spec.Containers[0].Args = behavioral.BehaviorExitFast
					behavioral.MustCreate(ctx, k8sClient, rep)
					replacementPods = append(replacementPods, client.ObjectKeyFromObject(rep))
				}
			})

			ginkgo.By("Check that the preempted pods are deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var p corev1.Pod
					for _, origPod := range defaultPriorityGroup {
						origKey := client.ObjectKeyFromObject(origPod)
						g.Expect(k8sClient.Get(ctx, origKey, &p)).To(utiltesting.BeNotFoundError())
					}
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the high-priority pods are scheduled", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range highPriorityGroup {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Call high priority group pods to complete", func() {
				for _, p := range highPriorityGroup {
					listOpts := client.MatchingFields{metav1.ObjectNameField: p.Name}
					behavioral.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 1, 0, listOpts)
				}
			})

			ginkgo.By("Verify the high priority group completes", func() {
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, highGroupKey, behavioral.LongTimeout)
			})

			ginkgo.By("Await for the replacement pods to be ungated", func() {
				for _, replKey := range replacementPods {
					gomega.Eventually(func(g gomega.Gomega) {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, replKey, &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Verify the replacement pods of the default priority workload complete", func() {
				for _, replKey := range replacementPods {
					gomega.Eventually(func(g gomega.Gomega) {
						var p corev1.Pod
						g.Expect(k8sClient.Get(ctx, replKey, &p)).To(gomega.Succeed())
						g.Expect(p.Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
					}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Verify the default priority workload is finished", func() {
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, defaultGroupKey, behavioral.LongTimeout)
			})
		})

		ginkgo.It("Pod should be admitted after the group labels are added", func() {
			ginkgo.By("creating a pod", func() {
				p := podtesting.MakePod("pod-0", ns.Name).
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorExitFast).
					RequestAndLimit(corev1.ResourceCPU, "1").
					Label("app-role", "worker").
					Annotation(podconstants.SuspendedByParentAnnotation, "OtherController").
					KueueSchedulingGate().
					Obj()
				behavioral.MustCreate(ctx, k8sClient, p)

				gomega.Eventually(func(g gomega.Gomega) {
					var createdPod corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).To(gomega.ContainElement(corev1.PodSchedulingGate{Name: podconstants.SchedulingGateName}))
					g.Expect(createdPod.Annotations).To(gomega.HaveKey(podconstants.RoleHashAnnotation))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("Add the group annotations and labels to the pod", func() {
				var p corev1.Pod
				pKey := client.ObjectKey{Namespace: ns.Name, Name: "pod-0"}
				gomega.Expect(k8sClient.Get(ctx, pKey, &p)).To(gomega.Succeed())

				pod.SetPodGroupName(&p, "test-group")
				p.Annotations[podconstants.GroupTotalCountAnnotation] = "1"
				p.Annotations[podconstants.SuspendedByParentAnnotation] = "OtherController"
				p.Labels[controllerconsts.QueueLabel] = lq.Name
				p.Labels[constants.ManagedByKueueLabelKey] = constants.ManagedByKueueLabelValue
				gomega.Expect(k8sClient.Update(ctx, &p)).To(gomega.Succeed())

				var podWithHash corev1.Pod
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&p), &podWithHash)).To(gomega.Succeed())
				initialRoleHash := podWithHash.Annotations[podconstants.RoleHashAnnotation]
				gomega.Expect(initialRoleHash).NotTo(gomega.BeEmpty())
			})

			ginkgo.By("Verify pod has queue labels assigned", func() {
				pKey := client.ObjectKey{Namespace: ns.Name, Name: "pod-0"}
				gomega.Eventually(func(g gomega.Gomega) {
					var runningPod corev1.Pod
					g.Expect(k8sClient.Get(ctx, pKey, &runningPod)).To(gomega.Succeed())
					g.Expect(runningPod.Labels[constants.ClusterQueueLabel]).To(gomega.Equal(cq.Name))
					g.Expect(runningPod.Labels[constants.LocalQueueLabel]).To(gomega.Equal(lq.Name))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the pod is scheduled and runs", func() {
				pKey := client.ObjectKey{Namespace: ns.Name, Name: "pod-0"}
				gomega.Eventually(func(g gomega.Gomega) {
					var runningPod corev1.Pod
					g.Expect(k8sClient.Get(ctx, pKey, &runningPod)).To(gomega.Succeed())
					g.Expect(runningPod.Spec.SchedulingGates).To(gomega.BeEmpty())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				gKey := client.ObjectKey{Namespace: ns.Name, Name: "test-group"}
				behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, gKey, behavioral.LongTimeout)
			})

			ginkgo.By("Ensure the pod is deleted", func() {
				var p corev1.Pod
				pKey := client.ObjectKey{Namespace: ns.Name, Name: "pod-0"}
				gomega.Expect(k8sClient.Get(ctx, pKey, &p)).To(gomega.Succeed())
				gomega.Expect(k8sClient.Delete(ctx, &p)).To(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, pKey, &corev1.Pod{})).To(utiltesting.BeNotFoundError())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				gKey := client.ObjectKey{Namespace: ns.Name, Name: "test-group"}
				behavioral.ExpectWorkloadsFinalizedOrGone(ctx, k8sClient, gKey)
			})
		})
	})
})
