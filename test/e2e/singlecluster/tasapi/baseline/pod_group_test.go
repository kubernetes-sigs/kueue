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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("TopologyAwareScheduling for Pod group", ginkgo.Label(e2e.Shard1, "area:tas", "feature:pod"), func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-pod-group-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Pod group", func() {
		var (
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
			behavioral.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).TopologyName(topology.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "8").
						Resource(corev1.ResourceCPU, "2").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should place pods based on the ranks-ordering", func() {
			ginkgo.By("Creating pod group with 4 pods")
			numPods := 4
			basePod := testingpod.MakePod("test-pod", ns.Name).
				Queue("test-queue").
				RequestAndLimit(extraResource, "1").
				Limit(extraResource, "1").
				Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
				Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel)
			podGroup := basePod.MakeIndexedGroup(numPods)

			for _, pod := range podGroup {
				behavioral.MustCreate(ctx, k8sClient, pod)
			}

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the assignment of pods are as expected with rank-based ordering", func() {
				gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels(basePod.Labels))).To(gomega.Succeed())
				gotAssignment := make(map[string]string, numPods)
				for _, pod := range pods.Items {
					index := pod.Labels[kueue.PodGroupPodIndexLabel]
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
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				Request(extraResource, "1").
				Limit(extraResource, "1")
			podGroup := basePod.TerminationGracePeriod(1).MakeIndexedGroup(numPods)

			for _, pod := range podGroup {
				behavioral.MustCreate(ctx, k8sClient, pod)
			}

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
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

		ginkgo.It("Should spread multiple pod groups across blocks with topology spreading", func() {
			// Every pod carries the spread-group label, which labelKeysToCopy
			// copies onto each pod group's Workload, so workloadLabelSelectors
			// spreads the 4 groups against each other. With a 0.5 per-block
			// allowance they must end up 2 and 2; at 10m CPU all groups would
			// otherwise fit in a single block.
			const (
				numGroups        = 4
				podsPerGroup     = 2
				spreadGroupLabel = "spread-group"
				spreadGroupValue = "pod-group-topology-spreading"
			)
			spreadingAnnotation := fmt.Sprintf(
				`{"workloadLabelSelectors":[{"key":%q,"operator":"In","values":[%q]}],`+
					`"rules":[{"topologyKey":%q,"maxShareAllowingPlacement":"0.5","enforcementMode":"Required"}]}`,
				spreadGroupLabel, spreadGroupValue, utiltesting.DefaultBlockTopologyLevel,
			)

			basePod := testingpod.MakePod("group", ns.Name).
				Queue("test-queue").
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "10m").
				Label(spreadGroupLabel, spreadGroupValue).
				Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
				Annotation(kueue.PodSetTopologySpreadingAnnotation, spreadingAnnotation).
				TerminationGracePeriod(1)

			ginkgo.By("Creating 4 pod groups with 2 pods each", func() {
				for i := range numGroups {
					podGroup := basePod.Clone().Name(fmt.Sprintf("group-%d", i)).MakeGroup(podsPerGroup)
					for _, pod := range podGroup {
						behavioral.MustCreate(ctx, k8sClient, pod)
					}
				}
			})

			pods := &corev1.PodList{}
			ginkgo.By("Ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numGroups * podsPerGroup))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify every pod group's Workload is labelled with the spread group", func() {
				workloads := &kueue.WorkloadList{}
				gomega.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
				gomega.Expect(workloads.Items).To(gomega.HaveLen(numGroups))
				for _, wl := range workloads.Items {
					gomega.Expect(wl.Labels).To(gomega.HaveKeyWithValue(spreadGroupLabel, spreadGroupValue), "workload %s", wl.Name)
				}
			})

			ginkgo.By("Verify each pod group lands in a single block, and each block holds exactly 2 of the 4 groups", func() {
				blockOfNode := behavioral.GetTopologyDomainByNode(ctx, k8sClient, utiltesting.DefaultBlockTopologyLevel)
				blockByGroup := make(map[string]string, numGroups)
				for _, pod := range pods.Items {
					group := pod.Labels[podconstants.GroupNameLabel]
					block, found := blockOfNode[pod.Spec.NodeName]
					gomega.Expect(found).To(gomega.BeTrue(), "pod %s landed on unexpected node %s", pod.Name, pod.Spec.NodeName)
					if existing, ok := blockByGroup[group]; ok {
						gomega.Expect(block).To(gomega.Equal(existing),
							"pod group %s has pods split across blocks %s and %s", group, existing, block)
					} else {
						blockByGroup[group] = block
					}
				}
				gomega.Expect(blockByGroup).To(gomega.HaveLen(numGroups))

				groupsPerBlock := make(map[string]int, 2)
				for _, block := range blockByGroup {
					groupsPerBlock[block]++
				}
				wantGroupsPerBlock := map[string]int{
					"b1": 2,
					"b2": 2,
				}
				gomega.Expect(groupsPerBlock).To(gomega.BeComparableTo(wantGroupsPerBlock))
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
			flavor = utiltestingapi.MakeResourceFlavor("flavor").
				NodeLabel(tasNodeGroupLabel, instanceType).Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor").
						Resource(extraResource, "8").
						Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("test-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should let the Job scheduled", func() {
			ginkgo.By("Creating pod group with 4 pods")
			numPods := 4
			basePod := testingpod.MakePod("test-pod", ns.Name).
				Queue("test-queue").
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				Request(extraResource, "1").
				Limit(extraResource, "1")
			podGroup := basePod.TerminationGracePeriod(1).MakeIndexedGroup(numPods)

			for _, pod := range podGroup {
				behavioral.MustCreate(ctx, k8sClient, pod)
			}

			pods := &corev1.PodList{}
			ginkgo.By("ensure all pods are created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("ensure all pods are scheduled", func() {
				listOpts := &client.ListOptions{
					FieldSelector: fields.OneTermNotEqualSelector("spec.nodeName", ""),
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), listOpts)).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(numPods))
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})
