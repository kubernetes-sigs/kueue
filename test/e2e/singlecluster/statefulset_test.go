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

package e2e

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	statefulsettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("StatefulSet integration", func() {
	const (
		resourceFlavorName = "sts-rf"
		clusterQueueName   = "sts-cq"
		localQueueName     = "sts-lq"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sts-e2e-")

		rf = testing.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = testing.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*testing.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

		lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
		util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("StatefulSet created", func() {
		ginkgo.It("should admit group that fits", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}
			util.MustCreate(ctx, k8sClient, statefulSet)

			gomega.Eventually(func(g gomega.Gomega) {
				createdStatefulSet := &appsv1.StatefulSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
					To(gomega.Succeed())
				g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			createdWorkload := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

			ginkgo.By("Creating potentially conflicting stateful-set", func() {
				conflictingStatefulSet := statefulsettesting.MakeStatefulSet("sts-conflict", ns.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					TerminationGracePeriod(1).
					Replicas(1).
					Queue(lq.Name).
					Obj()
				conflictingWlLookupKey := types.NamespacedName{
					Name:      statefulset.GetWorkloadName(conflictingStatefulSet.Name),
					Namespace: ns.Name,
				}
				util.MustCreate(ctx, k8sClient, conflictingStatefulSet)
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(conflictingStatefulSet), createdStatefulSet)).
						To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				conflictingWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, conflictingWlLookupKey, conflictingWorkload)).To(gomega.Succeed())
				gomega.Expect(createdWorkload.Name).ToNot(gomega.Equal(conflictingWorkload.Name))
				util.ExpectObjectToBeDeleted(ctx, k8sClient, conflictingStatefulSet, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, conflictingWorkload, false, util.LongTimeout)
			})

			util.ExpectObjectToBeDeleted(ctx, k8sClient, statefulSet, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, createdWorkload, false, util.LongTimeout)

			ginkgo.By("Check all pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to update the PodTemplate in StatefulSet", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImageOld(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(lq.Name).
				Obj()
			ginkgo.By("Create a StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, statefulSet)
			})

			createdStatefulSet := &appsv1.StatefulSet{}
			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
						To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Update the PodTemplate in StatefulSet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Spec.Template.Spec.Containers).Should(gomega.HaveLen(1))
					createdStatefulSet.Spec.Template.Spec.Containers[0].Image = util.GetAgnHostImage()
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the Pods to be replaced with the new template", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(createdStatefulSet.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, p := range pods.Items {
						g.Expect(createdStatefulSet.Spec.Template.Spec.Containers).Should(gomega.HaveLen(1))
						g.Expect(p.Spec.Containers[0].Image).To(gomega.Equal(util.GetAgnHostImage()))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the StatefulSet to have all replicas ready again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).
						To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should delete all pods on scale down to zero", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, statefulSet)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Scale down replicas to zero", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](0)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas is deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is deleted", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload, false)
			})
		})

		ginkgo.It("should create pods after scale up from zero", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Replicas(0).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, statefulSet)
			})

			ginkgo.By("Scale up replicas", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](3)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is created", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to scale up after scale down to zero", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(lq.Name).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, statefulSet)
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})

			ginkgo.By("Scale down replicas to zero", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](0)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Wait for ReadyReplicas < 3", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.BeNumerically("<", 3))
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Scale up replicas to zero - retry as it may not be possible immediately", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Spec.Replicas = ptr.To[int32](3)
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow to change queue name if ReadyReplicas=0", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				Replicas(3).
				Queue(fmt.Sprintf("%s-invalid", localQueueName)).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, statefulSet)
			})

			ginkgo.By("Checking that replicas is not ready", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Update queue name", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					createdStatefulSet.Labels[controllerconstants.QueueLabel] = localQueueName
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for replicas to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is created", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			})
		})

		ginkgo.It("should delete all Pods if StatefulSet was deleted after being partially ready", func() {
			statefulSet := statefulsettesting.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(localQueueName).
				Obj()
			wlLookupKey := types.NamespacedName{Name: statefulset.GetWorkloadName(statefulSet.Name), Namespace: ns.Name}

			ginkgo.By("Create StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, statefulSet)
			})

			createdWorkload := &kueue.Workload{}
			ginkgo.By("Check workload is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
			})

			ginkgo.By("Waiting for replicas is partially ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdStatefulSet := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(statefulSet), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.BeNumerically(">", 0))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete StatefulSet", func() {
				gomega.Expect(k8sClient.Delete(ctx, statefulSet)).To(gomega.Succeed())
			})

			ginkgo.By("Check all pods are deleted", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("StatefulSet created with WorkloadPriorityClass", func() {
		var (
			highPriorityWPC *kueue.WorkloadPriorityClass
			lowPriorityWPC  *kueue.WorkloadPriorityClass
		)

		ginkgo.BeforeEach(func() {
			highPriorityWPC = testing.MakeWorkloadPriorityClass("high-priority").
				PriorityValue(5000).
				Obj()
			util.MustCreate(ctx, k8sClient, highPriorityWPC)

			lowPriorityWPC = testing.MakeWorkloadPriorityClass("low-priority").
				PriorityValue(1000).
				Obj()
			util.MustCreate(ctx, k8sClient, lowPriorityWPC)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityWPC, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityWPC, true)
		})

		ginkgo.It("should preempt low-priority StatefulSet", func() {
			lowPrioritySTS := statefulsettesting.MakeStatefulSet("low-priority", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(lq.Name).
				WorkloadPriorityClass(lowPriorityWPC.Name).
				Obj()

			ginkgo.By("Create a low-priority StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, lowPrioritySTS)
			})

			ginkgo.By("Waiting for replicas to be ready in low-priority StatefulSet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLowPrioritySTS := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPrioritySTS), createdLowPrioritySTS)).To(gomega.Succeed())
					g.Expect(createdLowPrioritySTS.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdLowPriorityWl := &kueue.Workload{}
			lowPriorityWlKey := types.NamespacedName{
				Name:      statefulset.GetWorkloadName(lowPrioritySTS.Name),
				Namespace: ns.Name,
			}
			ginkgo.By("Check the low-priority Workload is created and admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowPriorityWlKey, createdLowPriorityWl)).To(gomega.Succeed())
					g.Expect(createdLowPriorityWl.Spec.PriorityClassSource).To(gomega.Equal(constants.WorkloadPriorityClassSource))
					g.Expect(createdLowPriorityWl.Spec.PriorityClassName).To(gomega.Equal(lowPriorityWPC.Name))
					g.Expect(createdLowPriorityWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			highPrioritySTS := statefulsettesting.MakeStatefulSet("high-priority", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Replicas(3).
				Queue(lq.Name).
				WorkloadPriorityClass(highPriorityWPC.Name).
				Obj()

			ginkgo.By("Create a high-priority StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, highPrioritySTS)
			})

			ginkgo.By("Waiting for replicas to be ready in high-priority StatefulSet", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdHighPrioritySTS := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(highPrioritySTS), createdHighPrioritySTS)).To(gomega.Succeed())
					g.Expect(createdHighPrioritySTS.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Await for the low-priory StatefulSet to be preempted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLowPrioritySTS := &appsv1.StatefulSet{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPrioritySTS), createdLowPrioritySTS)).To(gomega.Succeed())
					g.Expect(createdLowPrioritySTS.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdHighPriorityWl := &kueue.Workload{}
			highPriorityWlKey := types.NamespacedName{
				Name:      statefulset.GetWorkloadName(highPrioritySTS.Name),
				Namespace: ns.Name,
			}
			ginkgo.By("Await for the high-priority Workload to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, highPriorityWlKey, createdHighPriorityWl)).To(gomega.Succeed())
					g.Expect(createdHighPriorityWl.Spec.PriorityClassSource).To(gomega.Equal(constants.WorkloadPriorityClassSource))
					g.Expect(createdHighPriorityWl.Spec.PriorityClassName).To(gomega.Equal(highPriorityWPC.Name))
					g.Expect(createdHighPriorityWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
