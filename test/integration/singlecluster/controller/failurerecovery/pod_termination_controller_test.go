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

package failurerecovery

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	forcefulTerminationCheckTimeout = 2 * time.Second
)

var _ = ginkgo.Describe("Pod termination controller", func() {
	var ns *corev1.Namespace

	var matchingPodWrapper *testingpod.PodWrapper

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-fr-namespace-")

		matchingPodWrapper = testingpod.MakePod("matching-pod", ns.Name).
			StatusPhase(corev1.PodPending).
			TerminationGracePeriod(1).
			Annotation(constants.SafeToForcefullyDeleteAnnotationKey, constants.SafeToForcefullyDeleteAnnotationValue)
	})

	ginkgo.It("should forcefully terminate pods that opt-in, scheduled on unreachable nodes", func() {
		unreachableNode := testingnode.MakeNode("unreachable-node").
			NotReady().
			Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		util.MustCreate(ctx, k8sClient, unreachableNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, unreachableNode, true)
		})

		matchingPod := matchingPodWrapper.Clone().NodeName(unreachableNode.Name).Obj()
		util.MustCreate(ctx, k8sClient, matchingPod)
		gomega.Expect(k8sClient.Delete(ctx, matchingPod)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchingPod.Name, Namespace: matchingPod.Namespace}, matchingPod)).
				To(utiltesting.BeNotFoundError())
		}, forcefulTerminationCheckTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should trigger reconciliation when the node becomes unreachable", func() {
		thrashingNode := testingnode.MakeNode("thrashing-node").
			Ready().
			Obj()
		util.MustCreate(ctx, k8sClient, thrashingNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, thrashingNode, true)
		})

		var pod *corev1.Pod

		ginkgo.By("creating the pod on a reachable node", func() {
			pod = matchingPodWrapper.Clone().NodeName(thrashingNode.Name).Obj()
			util.MustCreate(ctx, k8sClient, pod)
		})

		ginkgo.By("marking the pod for deletion and verifying that it's not forcefully terminated", func() {
			gomega.Expect(k8sClient.Delete(ctx, pod)).To(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)).To(gomega.Succeed())
			}, forcefulTerminationCheckTimeout, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("tainting the previously reachable node as unreachable", func() {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: thrashingNode.Name}, thrashingNode)).To(gomega.Succeed())
			thrashingNode.Spec.Taints = append(thrashingNode.Spec.Taints, corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule})
			gomega.Expect(k8sClient.Update(ctx, thrashingNode)).To(gomega.Succeed())
		})

		ginkgo.By("verifying that the pod is forcefully terminated after the timeout elapses", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)).
					To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
	ginkgo.It("should trigger reconciliation when the node becomes unreachable", func() {
		thrashingNode := testingnode.MakeNode("thrashing-node").
			Ready().
			Obj()
		util.MustCreate(ctx, k8sClient, thrashingNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, thrashingNode, true)
		})

		var pod *corev1.Pod

		ginkgo.By("creating the pod on a reachable node", func() {
			pod = matchingPodWrapper.Clone().NodeName(thrashingNode.Name).Obj()
			util.MustCreate(ctx, k8sClient, pod)
		})

		ginkgo.By("marking the pod for deletion and verifying that it's not forcefully terminated", func() {
			gomega.Expect(k8sClient.Delete(ctx, pod)).To(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)).To(gomega.Succeed())
			}, forcefulTerminationCheckTimeout, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("tainting the previously reachable node as unreachable", func() {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: thrashingNode.Name}, thrashingNode)).To(gomega.Succeed())
			thrashingNode.Spec.Taints = append(thrashingNode.Spec.Taints, corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule})
			gomega.Expect(k8sClient.Update(ctx, thrashingNode)).To(gomega.Succeed())
		})

		ginkgo.By("verifying that the pod is forcefully terminated after the timeout elapses", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)).
					To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should resume forceful termination if the controller restarts", func() {
		node := testingnode.MakeNode("restart-node").
			Ready().
			Obj()
		util.MustCreate(ctx, k8sClient, node)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
		})

		pod := matchingPodWrapper.Clone().NodeName(node.Name).Obj()
		util.MustCreate(ctx, k8sClient, pod)

		ginkgo.By("stopping the controller manager", func() {
			fwk.StopManager(ctx)
		})

		ginkgo.By("mutating state while the controller is down", func() {
			// Mark pod for deletion (it will hang because there's no Kubelet)
			gomega.Expect(k8sClient.Delete(ctx, pod)).To(gomega.Succeed())

			// Taint the node as unreachable
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: node.Name}, node)).To(gomega.Succeed())
			node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule})
			gomega.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
		})

		ginkgo.By("restarting the controller manager", func() {
			fwk.StartManager(ctx, cfg, managerSetup)
		})

		ginkgo.By("verifying the pod is forcefully terminated upon restart", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, pod)).
					To(utiltesting.BeNotFoundError())
			}, forcefulTerminationCheckTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should not forcefully terminate matching pods that did not opt-in or are running on healthy nodes", func() {
		unhealthyNode := testingnode.MakeNode("unhealthy-node").
			NotReady().
			Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		util.MustCreate(ctx, k8sClient, unhealthyNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, unhealthyNode, true)
		})
		nonMatchingPod := matchingPodWrapper.
			Clone().
			Name("non-matching-pod").
			NodeName(unhealthyNode.Name).
			Annotation(constants.SafeToForcefullyDeleteAnnotationKey, "false").
			Obj()
		util.MustCreate(ctx, k8sClient, nonMatchingPod)
		gomega.Expect(k8sClient.Delete(ctx, nonMatchingPod)).To(gomega.Succeed())

		healthyNode := testingnode.MakeNode("healthy-node").
			Ready().
			Obj()
		util.MustCreate(ctx, k8sClient, healthyNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, healthyNode, true)
		})
		podOnHealthyNode := matchingPodWrapper.Clone().Name("healthy-pod").NodeName(healthyNode.Name).Obj()
		util.MustCreate(ctx, k8sClient, podOnHealthyNode)
		gomega.Expect(k8sClient.Delete(ctx, podOnHealthyNode)).To(gomega.Succeed())

		gomega.Consistently(func(g gomega.Gomega) {
			// Non-matching pod is left untouched.
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nonMatchingPod.Name, Namespace: nonMatchingPod.Namespace}, nonMatchingPod)).
				To(gomega.Succeed())
			g.Expect(nonMatchingPod.Status.Phase).Should(gomega.Equal(corev1.PodPending))

			// Healthy pod is left untouched.
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: podOnHealthyNode.Name, Namespace: podOnHealthyNode.Namespace}, podOnHealthyNode)).
				To(gomega.Succeed())
			g.Expect(podOnHealthyNode.Status.Phase).Should(gomega.Equal(corev1.PodPending))
		}, forcefulTerminationCheckTimeout, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("should forcefully terminate failed pods that opt-in, scheduled on unreachable nodes", func() {
		unreachableNode := testingnode.MakeNode("unreachable-node-failed-pod").
			NotReady().
			Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		util.MustCreate(ctx, k8sClient, unreachableNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, unreachableNode, true)
		})

		matchingPod := matchingPodWrapper.Clone().
			NodeName(unreachableNode.Name).
			StatusPhase(corev1.PodFailed).
			Obj()
		util.MustCreate(ctx, k8sClient, matchingPod)
		gomega.Expect(k8sClient.Delete(ctx, matchingPod)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchingPod.Name, Namespace: matchingPod.Namespace}, matchingPod)).
				To(utiltesting.BeNotFoundError())
		}, forcefulTerminationCheckTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should forcefully terminate succeeded pods that opt-in, scheduled on unreachable nodes", func() {
		unreachableNode := testingnode.MakeNode("unreachable-node-succeeded-pod").
			NotReady().
			Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule}).
			Obj()
		util.MustCreate(ctx, k8sClient, unreachableNode)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, unreachableNode, true)
		})

		matchingPod := matchingPodWrapper.Clone().
			NodeName(unreachableNode.Name).
			StatusPhase(corev1.PodSucceeded).
			Obj()
		util.MustCreate(ctx, k8sClient, matchingPod)
		gomega.Expect(k8sClient.Delete(ctx, matchingPod)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchingPod.Name, Namespace: matchingPod.Namespace}, matchingPod)).
				To(utiltesting.BeNotFoundError())
		}, forcefulTerminationCheckTimeout, util.Interval).Should(gomega.Succeed())
	})
})
