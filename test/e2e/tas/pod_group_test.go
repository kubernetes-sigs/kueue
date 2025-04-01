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

package tase2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for Pod group", func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-pod-group-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Pod group", func() {
		var (
			topology     *kueuealpha.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = testing.MakeDefaultThreeLevelTopology("datacenter")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = testing.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			ginkgo.By("Creating pod group with 4 pods")
			numPods := 4
			basePod := testingpod.MakePod("test-pod", ns.Name).
				Queue("test-queue").
				RequestAndLimit(extraResource, "1").
				Limit(extraResource, "1").
				Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
				Annotation(kueuealpha.PodSetRequiredTopologyAnnotation, testing.DefaultBlockTopologyLevel)
			podGroup := basePod.MakeIndexedGroup(numPods)

			for _, pod := range podGroup {
				util.MustCreate(ctx, k8sClient, pod)
			}

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(basePod.Labels))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, numPods)
				for _, pod := range pods.Items {
					index := pod.Labels[kueuealpha.PodGroupPodIndexLabel]
					gotAssignment[index] = pod.Spec.NodeName
				}
				wantAssignment := map[string]string{
					"0": "kind-worker",
					"1": "kind-worker2",
					"2": "kind-worker3",
					"3": "kind-worker4",
				}
				gomega.Expect(wantAssignment).Should(gomega.BeComparableTo(gotAssignment))
			})
		})

		ginkgo.It("Should schedule Pods without explicit TAS annotation", func() {
			ginkgo.By("Creating pod group with 4 pods")
			numPods := 4
			basePod := testingpod.MakePod("test-pod", ns.Name).
				Queue("test-queue").
				Request(extraResource, "1").
				Limit(extraResource, "1")
			podGroup := basePod.MakeIndexedGroup(numPods)

			for _, pod := range podGroup {
				util.MustCreate(ctx, k8sClient, pod)
			}

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment for the Pods was using TAS", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(basePod.Labels))).To(gomega.Succeed())
				var gotAssignment []string
				for _, pod := range pods.Items {
					gotAssignment = append(gotAssignment, pod.Spec.NodeName)
				}
				wantAssignment := []string{
					"kind-worker",
					"kind-worker2",
					"kind-worker3",
					"kind-worker4",
				}
				gomega.Expect(wantAssignment).Should(gomega.ConsistOf(gotAssignment))
			})
		})
	})

	ginkgo.When("Creating a Pod group which is not using TAS", func() {
		var (
			flavor       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			flavor = testing.MakeResourceFlavor("flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).Obj()
			util.MustCreate(ctx, k8sClient, flavor)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = testing.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should let the Job scheduled", func() {
			ginkgo.By("Creating pod group with 4 pods")
			numPods := 4
			basePod := testingpod.MakePod("test-pod", ns.Name).
				Queue("test-queue").
				Request(extraResource, "1").
				Limit(extraResource, "1")
			podGroup := basePod.MakeIndexedGroup(numPods)

			for _, pod := range podGroup {
				util.MustCreate(ctx, k8sClient, pod)
			}

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
