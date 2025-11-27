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
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	unreachableNodeName             = "unreachable-node"
	reachableNodeName               = "reachable-node"
	forcefulTerminationCheckTimeout = 2 * time.Second
)

var _ = ginkgo.Describe("Pod termination controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace
	var matchingPodWrapper *testingpod.PodWrapper

	ginkgo.BeforeAll(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "pod-fr-namespace-")
		nodes := []corev1.Node{
			*testingnode.MakeNode(unreachableNodeName).
				NotReady().
				Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable, Effect: corev1.TaintEffectNoSchedule}).
				Obj(),
			*testingnode.MakeNode(reachableNodeName).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		matchingPodWrapper = testingpod.MakePod("matching-pod", ns.Name).
			StatusPhase(corev1.PodPending).
			TerminationGracePeriod(1).
			NodeName(unreachableNodeName).
			Annotation(constants.SafeToForcefullyTerminateAnnotationKey, constants.SafeToForcefullyTerminateAnnotationValue)
	})

	ginkgo.It("forcefully terminates pods that opt-in, scheduled on unreachable nodes", func() {
		matchingPod := matchingPodWrapper.Clone().Obj()
		util.MustCreate(ctx, k8sClient, matchingPod)
		gomega.Expect(k8sClient.Delete(ctx, matchingPod)).To(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchingPod.Name, Namespace: matchingPod.Namespace}, matchingPod)).
				To(gomega.Succeed())
			g.Expect(matchingPod.Status.Phase).Should(gomega.Equal(corev1.PodFailed))
		}, forcefulTerminationCheckTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("does not forcefully terminate matching pods that did not opt-in or are running on healthy nodes", func() {
		nonMatchingPod := matchingPodWrapper.
			Clone().
			Name("non-matching-pod").
			Annotation(constants.SafeToForcefullyTerminateAnnotationKey, "false").
			Obj()
		util.MustCreate(ctx, k8sClient, nonMatchingPod)
		gomega.Expect(k8sClient.Delete(ctx, nonMatchingPod)).To(gomega.Succeed())

		podOnHealthyNode := matchingPodWrapper.Clone().Name("healthy-pod").NodeName(reachableNodeName).Obj()
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
		}, forcefulTerminationCheckTimeout, util.Interval).Should(gomega.Succeed())
	})
})
