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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
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
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should admit group that fits", func() {
			g1 := podtesting.MakePod("g1", ns.Name).
				Image("gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"1s"}).
				Queue(lq.Name).
				Request(corev1.ResourceCPU, "1").
				MakeGroup(2)
			g1Key := client.ObjectKey{Namespace: ns.Name, Name: "g1"}
			for _, p := range g1 {
				gomega.Expect(k8sClient.Create(ctx, p)).To(gomega.Succeed())
				gomega.Expect(p.Spec.SchedulingGates).
					To(gomega.ContainElement(corev1.PodSchedulingGate{
						Name: pod.SchedulingGateName}))
			}
			ginkgo.By("Starting admission", func() {
				util.UnholdQueue(ctx, k8sClient, cq)

				// Verify that the Pods start with the appropriate selector.
				gomega.Eventually(func(g gomega.Gomega) {
					for _, origPod := range g1 {
						var p corev1.Pod
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(origPod), &p)).To(gomega.Succeed())
						g.Expect(p.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(p.Spec.NodeSelector).To(gomega.Equal(map[string]string{
							"instance-type": "on-demand",
						}))
					}
				}).Should(gomega.Succeed())
				// Verify that the Workload finishes.
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, g1Key, &wl)).To(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished)).
						To(gomega.BeTrueBecause("it's finished"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting finished Pods", func() {
				for _, p := range g1 {
					gomega.Expect(k8sClient.Delete(ctx, p)).To(gomega.Succeed())
				}
				gomega.Eventually(func(g gomega.Gomega) {
					for _, p := range g1 {
						var pCopy corev1.Pod
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(p), &pCopy)).To(testing.BeNotFoundError())
					}
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, g1Key, &wl)).Should(testing.BeNotFoundError())
				}, util.Timeout, util.Interval)
			})
		})
	})
})
