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

package configurablepreemptions

import (
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/tas"
	kueuetestalpha1 "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("ConfigurablePreemptions", ginkgo.Label("feature:configurablepreemptions"), func() {
	var (
		ns *corev1.Namespace
	)

	var createWorkloadWithPriority = func(queue string, extraResourceRequests string, priority int32, nodeSelector map[string]string) *kueue.Workload {
		wl := utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
			Priority(priority).
			Queue(kueue.LocalQueueName(queue)).
			Label(extraResource, extraResourceRequests).
			PodSets(*utiltestingapi.MakePodSet("worker", 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(extraResource, extraResourceRequests).
				NodeSelector(nodeSelector).Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	var createWorkload = func(queue string, extraResourceRequests string, nodeSelector map[string]string) *kueue.Workload {
		return createWorkloadWithPriority(queue, extraResourceRequests, 0, nodeSelector)
	}

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "configurablepreemptions-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Defragmentation is configured", func() {
		var (
			flavor   *kueue.ResourceFlavor
			topology *kueue.Topology
			cqA      *kueue.ClusterQueue
			cqB      *kueue.ClusterQueue
			lqA      *kueue.LocalQueue
			lqB      *kueue.LocalQueue
			config   *kueuealpha.PreemptionConfig
		)

		ginkgo.BeforeEach(func() {
			nodes := []corev1.Node{
				*testingnode.MakeNode("node-a").
					Label(commonLabelKey, commonLabelValue).
					Label(corev1.LabelHostname, "host-a").
					StatusAllocatable(corev1.ResourceList{
						extraResource:       resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("2"),
					}).
					Ready().Obj(),
				*testingnode.MakeNode("node-b").
					Label(commonLabelKey, commonLabelValue).
					Label(corev1.LabelHostname, "host-b").
					StatusAllocatable(corev1.ResourceList{
						extraResource:       resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("2"),
					}).
					Ready().Obj(),
			}
			integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

			defragPreemptionConfigName := "preemption-configuration"
			config = kueuetestalpha1.MakePreemptionConfig(defragPreemptionConfigName).
				Rule("defrag-smaller-tpu-workloads",
					kueuealpha.QuotaFeasibleAndInsufficientTopology,
					kueuealpha.PreemptionConfigPreemptionCandidateSelector{
						Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
							Mode:       kueuealpha.Boosted,
							Comparison: kueuealpha.LessThanOrEqual,
						},
						Scope: kueuealpha.AnyClusterQueue,
						NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
							{
								Key:           extraResource,
								Comparison:    new(kueuealpha.LessThan),
								FallbackValue: new(int32(0)),
							},
						},
					}).Obj()
			behavioral.MustCreate(ctx, k8sClient, config)

			topology = utiltestingapi.MakeDefaultOneLevelTopology("defrag-topology")
			behavioral.MustCreate(ctx, k8sClient, topology)

			flavor = utiltestingapi.MakeResourceFlavor("rf-defrag").
				// NodeLabel is required when TopologyName exists
				NodeLabel(commonLabelKey, commonLabelValue).
				TopologyName(topology.Name).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cqA = utiltestingapi.MakeClusterQueue("cq-defrag-a").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(extraResource, "2").
					Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, defragPreemptionConfigName).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA)
			lqA = utiltestingapi.MakeLocalQueue("lq-a", ns.Name).ClusterQueue(cqA.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA)

			cqB = utiltestingapi.MakeClusterQueue("cq-defrag-b").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(extraResource, "2").
					Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, defragPreemptionConfigName).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqB)
			lqB = utiltestingapi.MakeLocalQueue("lq-b", ns.Name).ClusterQueue(cqB.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqB)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lqA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lqB, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("Should reschedule running workload and schedule incoming", func() {
			var wlA *kueue.Workload
			ginkgo.By("Scheduling small workload on topology domain", func() {
				wlA = createWorkload("lq-a", "1", map[string]string{})
				behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqA.Name, wlA)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)
			})

			var wlAHostnameBeforeReschedule string
			ginkgo.By("Save hostname of small workload before reschedule", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlA), wlA)).Should(gomega.Succeed())
				nodesA := slices.Collect(tas.LowestLevelValues(wlA.Status.Admission.PodSetAssignments[0].TopologyAssignment))
				gomega.Expect(nodesA).To(gomega.HaveLen(1))
				wlAHostnameBeforeReschedule = nodesA[0]
			})

			var wlB *kueue.Workload
			ginkgo.By("Large workload requires same domain - needing defrag", func() {
				// Simulate already taken topology by requiring workload to schedule on the same node as first workload.
				wlB = createWorkload("lq-b", "2", map[string]string{corev1.LabelHostname: wlAHostnameBeforeReschedule})
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
				behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqB.Name, wlB)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)
			})

			ginkgo.By("Verify small workload was rescheduled", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlA), wlA)).Should(gomega.Succeed())
				nodesA := slices.Collect(tas.LowestLevelValues(wlA.Status.Admission.PodSetAssignments[0].TopologyAssignment))
				gomega.Expect(nodesA).To(gomega.HaveLen(1))
				wlAHostnameAfterReschedule := nodesA[0]

				gomega.Expect(wlAHostnameAfterReschedule).ShouldNot(gomega.Equal(wlAHostnameBeforeReschedule))
			})

			ginkgo.By("Same size workload requiring same domain remains pending", func() {
				wlC := createWorkload("lq-a", "2", map[string]string{corev1.LabelHostname: wlAHostnameBeforeReschedule})
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlC)
			})
		})
	})

	ginkgo.When("Defragmentation is configured across overlapping flavors", func() {
		const overlapLabelKey = "overlapTestingKey"
		var (
			nodes    []corev1.Node
			flavorA  *kueue.ResourceFlavor
			flavorB  *kueue.ResourceFlavor
			topology *kueue.Topology
			cqA      *kueue.ClusterQueue
			cqB      *kueue.ClusterQueue
			lqA      *kueue.LocalQueue
			lqB      *kueue.LocalQueue
			config   *kueuealpha.PreemptionConfig
		)

		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
				*testingnode.MakeNode("node-overlap-a").
					Label(overlapLabelKey, "true").
					Label(corev1.LabelHostname, "host-overlap-a").
					StatusAllocatable(corev1.ResourceList{
						extraResource:       resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("2"),
					}).
					Ready().Obj(),
				*testingnode.MakeNode("node-overlap-b").
					Label(overlapLabelKey, "true").
					Label(corev1.LabelHostname, "host-overlap-b").
					StatusAllocatable(corev1.ResourceList{
						extraResource:       resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("2"),
					}).
					Ready().Obj(),
			}
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			defragPreemptionConfigName := "overlap-preemption-configuration"
			config = kueuetestalpha1.MakePreemptionConfig(defragPreemptionConfigName).
				Rule("defrag-smaller-tpu-workloads",
					kueuealpha.QuotaFeasibleAndInsufficientTopology,
					kueuealpha.PreemptionConfigPreemptionCandidateSelector{
						Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
							Mode:       kueuealpha.Boosted,
							Comparison: kueuealpha.LessThanOrEqual,
						},
						Scope: kueuealpha.AnyClusterQueue,
						NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
							{
								Key:           extraResource,
								Comparison:    new(kueuealpha.LessThan),
								FallbackValue: new(int32(0)),
							},
						},
					}).Obj()
			util.MustCreate(ctx, k8sClient, config)

			topology = utiltestingapi.MakeDefaultOneLevelTopology("overlap-topology")
			util.MustCreate(ctx, k8sClient, topology)

			// Both flavors select the same nodes, so that, with
			// TASHandleOverlappingFlavors, the workloads of either flavor use the
			// node capacity seen by the other one.
			flavorA = utiltestingapi.MakeResourceFlavor("rf-overlap-a").
				NodeLabel(overlapLabelKey, "true").
				TopologyName(topology.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, flavorA)
			flavorB = utiltestingapi.MakeResourceFlavor("rf-overlap-b").
				NodeLabel(overlapLabelKey, "true").
				TopologyName(topology.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, flavorB)

			cqA = utiltestingapi.MakeClusterQueue("cq-overlap-a").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorA.Name).
					Resource(extraResource, "2").
					Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, defragPreemptionConfigName).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA)
			lqA = utiltestingapi.MakeLocalQueue("lq-overlap-a", ns.Name).ClusterQueue(cqA.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA)

			cqB = utiltestingapi.MakeClusterQueue("cq-overlap-b").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorB.Name).
					Resource(extraResource, "2").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqB)
			lqB = utiltestingapi.MakeLocalQueue("lq-overlap-b", ns.Name).ClusterQueue(cqB.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqB)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lqB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
			for i := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})

		ginkgo.It("Should reschedule running workload of the other flavor and schedule incoming", func() {
			var wlB *kueue.Workload
			ginkgo.By("Scheduling small workload of the other flavor on topology domain", func() {
				wlB = createWorkload(lqB.Name, "1", map[string]string{})
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqB.Name, wlB)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
			})

			var wlBHostnameBeforeReschedule string
			ginkgo.By("Save hostname of small workload before reschedule", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlB), wlB)).Should(gomega.Succeed())
				nodesB := slices.Collect(tas.LowestLevelValues(wlB.Status.Admission.PodSetAssignments[0].TopologyAssignment))
				gomega.Expect(nodesB).To(gomega.HaveLen(1))
				wlBHostnameBeforeReschedule = nodesB[0]
			})

			var wlA *kueue.Workload
			ginkgo.By("Large workload requires same domain - needing defrag", func() {
				// The quota of the ClusterQueue of the large workload is unused, but
				// the small workload holds half of the node it requires.
				wlA = createWorkload(lqA.Name, "2", map[string]string{corev1.LabelHostname: wlBHostnameBeforeReschedule})
				util.FinishEvictionForWorkloads(ctx, k8sClient, wlB)
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqA.Name, wlA)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
			})

			ginkgo.By("Verify small workload was rescheduled", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlB), wlB)).Should(gomega.Succeed())
				nodesB := slices.Collect(tas.LowestLevelValues(wlB.Status.Admission.PodSetAssignments[0].TopologyAssignment))
				gomega.Expect(nodesB).To(gomega.HaveLen(1))
				gomega.Expect(nodesB[0]).ShouldNot(gomega.Equal(wlBHostnameBeforeReschedule))
			})
		})
	})

	ginkgo.When("ClusterQueue configured for hero case", func() {
		var (
			flavor *kueue.ResourceFlavor
			cqA    *kueue.ClusterQueue
			cqHero *kueue.ClusterQueue
			lqA    *kueue.LocalQueue
			lqHero *kueue.LocalQueue
			config *kueuealpha.PreemptionConfig
		)

		ginkgo.BeforeEach(func() {
			heroJobConfiguration := "hero-job-preemption-configuration"
			config = kueuetestalpha1.MakePreemptionConfig(heroJobConfiguration).
				Rule("hero-preemption",
					kueuealpha.Always,
					kueuealpha.PreemptionConfigPreemptionCandidateSelector{
						Scope: kueuealpha.AnyClusterQueue,
					}).Obj()
			behavioral.MustCreate(ctx, k8sClient, config)

			flavor = utiltestingapi.MakeResourceFlavor("rf").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cqA = utiltestingapi.MakeClusterQueue("cq-regular").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "2").Obj()).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA)
			lqA = utiltestingapi.MakeLocalQueue("lq-regular", ns.Name).ClusterQueue(cqA.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA)

			cqHero = utiltestingapi.MakeClusterQueue("cq-hero").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, heroJobConfiguration).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqHero)
			lqHero = utiltestingapi.MakeLocalQueue("lq-hero", ns.Name).ClusterQueue(cqHero.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqHero)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lqA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lqHero, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqHero, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("Should preempt regular jobs to schedule hero-job", func() {
			wlA := utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
				Queue(kueue.LocalQueueName("lq-regular")).
				Request(corev1.ResourceCPU, "2").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wlA)
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqA.Name, wlA)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)

			wlHero := utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
				Queue(kueue.LocalQueueName("lq-hero")).
				Request(corev1.ResourceCPU, "2").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wlHero)
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqHero.Name, wlHero)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlHero)

			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlA)
		})
	})

	ginkgo.When("an Always selector cannot be built", func() {
		var (
			flavor *kueue.ResourceFlavor
			cq     *kueue.ClusterQueue
			lq     *kueue.LocalQueue
			config *kueuealpha.PreemptionConfig
		)

		ginkgo.BeforeEach(func() {
			preemptorSelector := &metav1.LabelSelector{MatchLabels: map[string]string{"preemptor": "true"}}
			config = kueuetestalpha1.MakePreemptionConfig("invalid-always-selector").
				RuleWithPreemptorSelector("select-a", kueuealpha.Always, preemptorSelector,
					kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						LabelSelector(&metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								// This unsupported operator makes the candidate selector fail to build.
								{Key: "preemptible", Operator: "invalid", Values: []string{"true"}},
							},
						}).Obj(),
				).
				RuleWithPreemptorSelector("select-b", kueuealpha.InsufficientQuota, preemptorSelector,
					kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"fallback": "true"}}).Obj(),
				).Obj()
			behavioral.MustCreate(ctx, k8sClient, config)

			flavor = utiltestingapi.MakeResourceFlavor("rf-invalid-selector").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cq = utiltestingapi.MakeClusterQueue("cq-invalid-selector").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "2").Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, config.Name).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
			lq = utiltestingapi.MakeLocalQueue("lq-invalid-selector", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("stops before evaluating the InsufficientQuota rule", func() {
			wlA := utiltestingapi.MakeWorkload("a", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Label("preemptible", "true").
				Priority(10).
				Request(corev1.ResourceCPU, "1").Obj()
			wlB := utiltestingapi.MakeWorkload("b", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Label("fallback", "true").
				Priority(10).
				Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, wlA)
			behavioral.MustCreate(ctx, k8sClient, wlB)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA, wlB)

			_ = fwk.ObservedLogs.TakeAll()
			newPreemptor := func() *kueue.Workload {
				return utiltestingapi.MakeWorkload("c", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Label("preemptor", "true").
					Priority(100).
					Request(corev1.ResourceCPU, "1").Obj()
			}
			wlC := newPreemptor()
			behavioral.MustCreate(ctx, k8sClient, wlC)
			gomega.Eventually(func() int {
				return len(fwk.ObservedLogs.FilterMessage("Failed to get candidates for preemption").All())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeNumerically(">", 0))
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlC)
			gomega.Consistently(func(g gomega.Gomega) {
				for _, wl := range []*kueue.Workload{wlA, wlB} {
					updated := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updated)).To(gomega.Succeed())
					g.Expect(updated.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(meta.IsStatusConditionTrue(updated.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
				}
			}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wlC, true)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(config), config)).To(gomega.Succeed())
			config.Spec.Rules[0].CandidateSelectors[0].LabelSelector.MatchExpressions[0].Operator = metav1.LabelSelectorOpIn
			gomega.Expect(k8sClient.Update(ctx, config)).To(gomega.Succeed())

			wlC = newPreemptor()
			behavioral.MustCreate(ctx, k8sClient, wlC)
			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlC)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlB), wlB)).To(gomega.Succeed())
			gomega.Expect(meta.IsStatusConditionTrue(wlB.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
		})
	})
})
