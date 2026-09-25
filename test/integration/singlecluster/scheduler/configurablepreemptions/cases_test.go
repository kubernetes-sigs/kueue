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
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util"
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
		util.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	var createWorkload = func(queue string, extraResourceRequests string, nodeSelector map[string]string) *kueue.Workload {
		return createWorkloadWithPriority(queue, extraResourceRequests, 0, nodeSelector)
	}

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup())
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "configurablepreemptions-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
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
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			defragPreemptionConfigName := "preemption-configuration"
			config = &kueuealpha.PreemptionConfig{
				Name: defragPreemptionConfigName,
				Spec: kueuealpha.PreemptionConfigSpec{
					Rules: []kueuealpha.PreemptionConfigPreemptionRule{
						{
							Name:             "defrag-smaller-tpu-workloads",
							ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{Trigger: kueuealpha.QuotaFeasibleAndInsufficientTopology},
							CandidateSelectors: []kueuealpha.PreemptionConfigPreemptionCandidateSelector{
								{
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
								},
							},
						},
					},
				},
			}
			util.MustCreate(ctx, k8sClient, config)

			topology = utiltestingapi.MakeDefaultOneLevelTopology("defrag-topology")
			util.MustCreate(ctx, k8sClient, topology)

			flavor = utiltestingapi.MakeResourceFlavor("rf-defrag").
				// NodeLabel is required when TopologyName exists
				NodeLabel(commonLabelKey, commonLabelValue).
				TopologyName(topology.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, flavor)

			cqA = utiltestingapi.MakeClusterQueue("cq-defrag-a").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(extraResource, "2").
					Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, defragPreemptionConfigName).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA)
			lqA = utiltestingapi.MakeLocalQueue("lq-a", ns.Name).ClusterQueue(cqA.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA)

			cqB = utiltestingapi.MakeClusterQueue("cq-defrag-b").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(extraResource, "2").
					Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, defragPreemptionConfigName).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqB)
			lqB = utiltestingapi.MakeLocalQueue("lq-b", ns.Name).ClusterQueue(cqB.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqB)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lqB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("Should reschedule running workload and schedule incoming", func() {
			wlA := createWorkload("lq-a", "1", map[string]string{})
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqA.Name, wlA)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlA), wlA)).Should(gomega.Succeed())
			nodesA := slices.Collect(tas.LowestLevelValues(wlA.Status.Admission.PodSetAssignments[0].TopologyAssignment))
			gomega.Expect(nodesA).To(gomega.HaveLen(1))
			wlAHostnameBeforeReschedule := nodesA[0]

			// Simulate already taken topology by requiring workload to schedule on the same node as first workload.
			wlB := createWorkload("lq-b", "2", map[string]string{corev1.LabelHostname: wlAHostnameBeforeReschedule})
			util.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqB.Name, wlB)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlA), wlA)).Should(gomega.Succeed())
			nodesA = slices.Collect(tas.LowestLevelValues(wlA.Status.Admission.PodSetAssignments[0].TopologyAssignment))
			gomega.Expect(nodesA).To(gomega.HaveLen(1))
			wlAHostnameAfterReschedule := nodesA[0]

			gomega.Expect(wlAHostnameAfterReschedule).ShouldNot(gomega.Equal(wlAHostnameBeforeReschedule))

			wlC := createWorkload("lq-a", "2", map[string]string{corev1.LabelHostname: wlAHostnameBeforeReschedule})
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wlC)
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
			config = &kueuealpha.PreemptionConfig{
				Name: heroJobConfiguration,
				Spec: kueuealpha.PreemptionConfigSpec{
					Rules: []kueuealpha.PreemptionConfigPreemptionRule{
						{
							Name:             "hero-preemption",
							ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{Trigger: kueuealpha.Always},
							CandidateSelectors: []kueuealpha.PreemptionConfigPreemptionCandidateSelector{
								{
									Scope: kueuealpha.AnyClusterQueue,
								},
							},
						},
					},
				},
			}
			util.MustCreate(ctx, k8sClient, config)

			flavor = utiltestingapi.MakeResourceFlavor("rf").Obj()
			util.MustCreate(ctx, k8sClient, flavor)

			cqA = utiltestingapi.MakeClusterQueue("cq-regular").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "2").Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA)
			lqA = utiltestingapi.MakeLocalQueue("lq-regular", ns.Name).ClusterQueue(cqA.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA)

			cqHero = utiltestingapi.MakeClusterQueue("cq-hero").
				Cohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, heroJobConfiguration).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqHero)
			lqHero = utiltestingapi.MakeLocalQueue("lq-hero", ns.Name).ClusterQueue(cqHero.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqHero)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lqHero, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqHero, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, config, true)
		})

		ginkgo.It("Should preempt regular jobs to schedule hero-job", func() {
			wlA := utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
				Queue(kueue.LocalQueueName("lq-regular")).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wlA)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqA.Name, wlA)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)

			wlHero := utiltestingapi.MakeWorkloadWithGeneratedName("workload-", ns.Name).
				Queue(kueue.LocalQueueName("lq-hero")).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, wlHero)
			util.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cqHero.Name, wlHero)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlHero)

			util.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wlA)
		})
	})
})
