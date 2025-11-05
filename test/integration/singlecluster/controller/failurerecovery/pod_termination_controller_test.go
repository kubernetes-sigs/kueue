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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	unreachableNodeName = "unreachable-node"
	reachableNodeName   = "reachable-node"
)

func createTerminatingPod(p *corev1.Pod) {
	ginkgo.GinkgoHelper()

	util.MustCreate(ctx, k8sClient, p)
	gomega.ExpectWithOffset(1, k8sClient.Delete(ctx, p)).To(gomega.Succeed())
}

var _ = ginkgo.Describe("Pod termination controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace
	var matchingPodWrapper *testingpod.PodWrapper

	ginkgo.BeforeAll(func() {
		kueueCfg := &config.Configuration{
			FailureRecoveryPolicy: &config.FailureRecoveryPolicy{
				Rules: []config.FailureRecoveryRule{
					{
						TerminatePod: &config.TerminatePodConfig{
							PodLabelSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"safe-to-fail": "true",
								},
							},
							ForcefulTerminationGracePeriod: metav1.Duration{Duration: time.Second},
						},
					},
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerSetup(kueueCfg))

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
			ManagedByKueueLabel().
			NodeName(unreachableNodeName).
			Label("safe-to-fail", "true")
	})

	ginkgo.It("forcefully terminates matching pods scheduled on unreachable nodes", func() {
		matchingPod := matchingPodWrapper.Clone().Obj()
		createTerminatingPod(matchingPod)

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: matchingPod.Name, Namespace: matchingPod.Namespace}, matchingPod)).
				To(gomega.Succeed())
			g.Expect(matchingPod.Status.Phase).Should(gomega.Equal(corev1.PodFailed))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("does not forcefully terminate non matching pods scheduled on unreachable nodes", func() {
		nonMatchingPod := matchingPodWrapper.Clone().Name("non-matching-pod").Label("safe-to-fail", "false").Obj()
		createTerminatingPod(nonMatchingPod)

		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nonMatchingPod.Name, Namespace: nonMatchingPod.Namespace}, nonMatchingPod)).
				To(gomega.Succeed())
			g.Expect(nonMatchingPod.Status.Phase).Should(gomega.Equal(corev1.PodPending))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("does not forcefully terminate non-Kueue pods scheduled on unreachable nodes", func() {
		nonKueuePod := matchingPodWrapper.Clone().Name("non-kueue-pod").Label(constants.ManagedByKueueLabelKey, "false").Obj()
		createTerminatingPod(nonKueuePod)

		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: nonKueuePod.Name, Namespace: nonKueuePod.Namespace}, nonKueuePod)).
				To(gomega.Succeed())
			g.Expect(nonKueuePod.Status.Phase).Should(gomega.Equal(corev1.PodPending))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("does not forcefully terminate matching pods scheduled on healthy nodes", func() {
		podOnHealthyNode := matchingPodWrapper.Clone().Name("healthy-pod").NodeName(reachableNodeName).Obj()
		createTerminatingPod(podOnHealthyNode)

		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: podOnHealthyNode.Name, Namespace: podOnHealthyNode.Namespace}, podOnHealthyNode)).
				To(gomega.Succeed())
			g.Expect(podOnHealthyNode.Status.Phase).Should(gomega.Equal(corev1.PodPending))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
