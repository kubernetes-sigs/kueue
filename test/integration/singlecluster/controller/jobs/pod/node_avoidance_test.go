/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
you may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pod

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"time"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testing "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Pod Node Avoidance", func() {
	var (
		ns           *corev1.Namespace
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "pod-node-avoidance-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, ns)).To(gomega.Succeed())
		})
	})

	ginkgo.Context("With FailureAwareScheduling enabled", func() {
		ginkgo.BeforeEach(func() {
			gomega.Expect(features.SetEnable(features.FailureAwareScheduling, true)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(features.SetEnable(features.FailureAwareScheduling, false)).To(gomega.Succeed())
				fwk.StopManager(ctx)
			})
			fwk.StartManager(ctx, cfg, managerSetup(false, true, nil, jobframework.WithUnhealthyNodeLabel("unhealthy")))

			gomega.Expect(k8sClient.Create(ctx, testing.MakeResourceFlavor("default-flavor").Obj())).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue-" + ns.Name).
				ResourceGroup(
					*testing.MakeFlavorQuotas("default-flavor").Resource(corev1.ResourceCPU, "1").Obj(),
				).
				Creation(time.Now()). // Ensure it's active
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			})

			localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())

			wpc := testing.MakeWorkloadPriorityClass("test-wpc").Obj()
			gomega.Expect(k8sClient.Create(ctx, wpc)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wpc, true)
			})

			pc := utiltesting.MakePriorityClass("test-wpc").Obj()
			gomega.Expect(k8sClient.Create(ctx, pc)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, pc, true)
			})
		})

		ginkgo.It("Should inject NodeAffinity for DisallowUnhealthy policy", func() {
			pod := testingpod.MakePod("pod", ns.Name).
				Queue(localQueue.Name).
				Annotation("kueue.x-k8s.io/node-avoidance-policy", "disallow-unhealthy").
				PriorityClass("test-wpc").
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
				gomega.Expect(k8sClient.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name))).To(gomega.Succeed())
			})

			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Spec.Affinity).ToNot(gomega.BeNil())
				g.Expect(createdPod.Spec.Affinity.NodeAffinity).ToNot(gomega.BeNil())
				g.Expect(createdPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution).ToNot(gomega.BeNil())
				g.Expect(createdPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms).To(gomega.HaveLen(1))

				terms := createdPod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
				g.Expect(terms[0].MatchExpressions).To(gomega.ContainElement(corev1.NodeSelectorRequirement{
					Key:      "unhealthy",
					Operator: corev1.NodeSelectorOpDoesNotExist,
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should inject NodeAffinity for PreferHealthy policy", func() {
			pod := testingpod.MakePod("pod", ns.Name).
				Queue(localQueue.Name).
				Annotation("kueue.x-k8s.io/node-avoidance-policy", "prefer-healthy").
				PriorityClass("test-wpc").
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, pod)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
				gomega.Expect(k8sClient.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(ns.Name))).To(gomega.Succeed())
			})

			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Spec.Affinity).ToNot(gomega.BeNil())
				g.Expect(createdPod.Spec.Affinity.NodeAffinity).ToNot(gomega.BeNil())
				g.Expect(createdPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution).To(gomega.HaveLen(1))

				terms := createdPod.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution
				g.Expect(terms[0].Preference.MatchExpressions).To(gomega.ContainElement(corev1.NodeSelectorRequirement{
					Key:      "unhealthy",
					Operator: corev1.NodeSelectorOpDoesNotExist,
				}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
