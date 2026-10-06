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
			behavioral.CreateNodesWithStatus(ctx, k8sClient, nodes)

			defragPreemptionConfigName := "preemption-configuration"
			config = kueuetestalpha1.MakePreemptionConfig(defragPreemptionConfigName).
				Rule("defrag-smaller-tpu-workloads",
					kueuealpha.QuotaFeasibleAndInsufficientTopology,
					kueuealpha.PreemptionConfigPreemptionCandidateSelector{
						Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
							Mode:       new(kueuealpha.Boosted),
							Comparison: new(kueuealpha.LessThanOrEqual),
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
				Rules(
					kueuetestalpha1.MakePreemptionRule("select-a", kueuealpha.Always,
						kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
							LabelSelector(&metav1.LabelSelector{
								MatchExpressions: []metav1.LabelSelectorRequirement{
									// This unsupported operator makes the candidate selector fail to build.
									{Key: "preemptible", Operator: "invalid", Values: []string{"true"}},
								},
							}).Obj(),
					).PreemptorSelector(preemptorSelector).Obj(),
					kueuetestalpha1.MakePreemptionRule("select-b", kueuealpha.InsufficientQuota,
						kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
							LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"fallback": "true"}}).Obj(),
					).PreemptorSelector(preemptorSelector).Obj(),
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

	ginkgo.When("WithinClusterQueue preemption is restricted to low-priority classes", func() {
		var (
			flavor *kueue.ResourceFlavor
			cq     *kueue.ClusterQueue
			lq     *kueue.LocalQueue
			config *kueuealpha.PreemptionConfig
		)

		ginkgo.BeforeEach(func() {
			preemptLowPriorityConfig := "preempt-same-cq-low-priority"
			config = kueuetestalpha1.MakePreemptionConfig(preemptLowPriorityConfig).
				Rule("preempt-same-cq-low-priority",
					kueuealpha.InsufficientQuota,
					kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.Base, kueuealpha.LessThan).
						PriorityMatchNames("low-priority").
						Obj(),
				).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, config)

			flavor = utiltestingapi.MakeResourceFlavor("rf-same-cq-priority").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cq = utiltestingapi.MakeClusterQueue("cq-shared-priority").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "2").Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, preemptLowPriorityConfig).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq-shared-priority", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("Should preempt low-priority workloads for high-priority while protecting mid-priority workloads", func() {
			wlMid := utiltestingapi.MakeWorkloadWithGeneratedName("wl-mid-", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				WorkloadPriorityClassRef("mid-priority").
				Priority(500).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wlLow := utiltestingapi.MakeWorkloadWithGeneratedName("wl-low-", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				WorkloadPriorityClassRef("low-priority").
				Priority(100).
				Request(corev1.ResourceCPU, "1").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wlMid)
			behavioral.MustCreate(ctx, k8sClient, wlLow)
			behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wlMid, wlLow)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlMid, wlLow)

			var wlHigh1 *kueue.Workload
			ginkgo.By("Admitting first high-priority workload by preempting low-priority while sparing mid-priority", func() {
				wlHigh1 = utiltestingapi.MakeWorkloadWithGeneratedName("wl-high-", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					WorkloadPriorityClassRef("high-priority").
					Priority(1000).
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wlHigh1)
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlLow)
				behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wlHigh1)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlHigh1, wlMid)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlLow)
			})

			ginkgo.By("Keeping second high-priority workload pending because mid-priority is protected from preemption", func() {
				wlHigh2 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-high-", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					WorkloadPriorityClassRef("high-priority").
					Priority(1000).
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wlHigh2)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlHigh2, wlLow)
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(behavioral.FilterEvictedWorkloads(ctx, k8sClient, wlMid, wlHigh1)).To(gomega.BeEmpty())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlHigh1, wlMid)
			})
		})
	})
})
