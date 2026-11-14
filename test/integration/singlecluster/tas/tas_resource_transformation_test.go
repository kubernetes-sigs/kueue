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

package tas

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

// Tests the "Retain" resource transformation strategy: regular CPU requests are
// transformed into cpu_credits (1:1 mapping) while preserving the original CPU
// request for scheduling against on-demand/spot flavors.
//
// Goal: verify that transformed capacity (cpu_credits quota) correctly gates
// admission, while original CPU still participates in flavor selection / fitting.
//
// See: https://kueue.sigs.k8s.io/docs/tasks/manage/share_quotas_across_flavors/
var _ = ginkgo.Describe("Topology Aware Scheduling – Resource Transformation: Retain CPU → cpu_credits (Share Quotas Across Flavors)", func() {
	const cpuCredits = "cpu_credits"

	var (
		nodes    []corev1.Node
		ns       *corev1.Namespace
		topology *kueue.Topology
		onDemand *kueue.ResourceFlavor
		spot     *kueue.ResourceFlavor
		credits  *kueue.ResourceFlavor
		cq       *kueue.ClusterQueue
		lq       *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		// Starts the manager with a single retain transformation: 1 CPU → 1 cpu_credits
		fwk.StartManagerForSpec(ctx, cfg, managerSetup(config.ResourceTransformation{
			Input:    corev1.ResourceCPU,
			Strategy: new(config.Retain),
			Outputs:  corev1.ResourceList{cpuCredits: resource.MustParse("1")},
		}))

		nodes = []corev1.Node{
			*testingnode.MakeNode("on-demand").
				Label("node-type", "on-demand").
				Label(corev1.LabelHostname, "on-demand").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("9"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj(),
			*testingnode.MakeNode("spot").
				Label("node-type", "spot").
				Label(corev1.LabelHostname, "spot").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("9"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")

		topology = utiltestingapi.MakeDefaultOneLevelTopology("topology")
		behavioral.MustCreate(ctx, k8sClient, topology)

		onDemand = utiltestingapi.MakeResourceFlavor("on-demand").
			NodeLabel("node-type", "on-demand").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, onDemand)

		spot = utiltestingapi.MakeResourceFlavor("spot").
			NodeLabel("node-type", "spot").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, spot)

		credits = utiltestingapi.MakeResourceFlavor("credits").Obj()
		behavioral.MustCreate(ctx, k8sClient, credits)

		// ClusterQueue setup:
		//   - 9 CPU on on-demand + 9 CPU on spot → total 18 "original" CPU capacity
		//   - 8 cpu_credits on credits flavor → transformed capacity is the real limit
		cq = utiltestingapi.MakeClusterQueue("team-cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(onDemand.Name).Resource(corev1.ResourceCPU, "9").Obj(),
				*utiltestingapi.MakeFlavorQuotas(spot.Name).Resource(corev1.ResourceCPU, "9").Obj(),
			).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(credits.Name).Resource(cpuCredits, "8").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("team-queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemand, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spot, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, credits, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
	})

	ginkgo.When("workloads request regular CPU but are transformed to cpu_credits", func() {
		const (
			highPriorityClassName = "high"
			highPriority          = 1000
			lowPriorityClassName  = "low"
			lowPriority           = 100

			podsPerWorkload = 1
			// each workload requests 3 CPU → 3 cpu_credits after transformation
			cpuRequestPerPod = "3"
			// 8 credits available → floor(8/3) = 2 workloads fit initially
			initialFitCount = 2
		)

		var (
			workloadWrapper *utiltestingapi.WorkloadWrapper
			admitted        []*kueue.Workload
			pendingWl       *kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			workloadWrapper = utiltestingapi.MakeWorkload("tmpl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				WorkloadPriorityClassRef(highPriorityClassName).
				Priority(highPriority).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, podsPerWorkload).
					Request(corev1.ResourceCPU, cpuRequestPerPod).
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj())

			admitted = make([]*kueue.Workload, 0, initialFitCount)

			ginkgo.By(fmt.Sprintf("Creating %d high-priority workloads (should fit within 8 credits)", initialFitCount), func() {
				for i := range initialFitCount {
					wl := workloadWrapper.Clone().Name(fmt.Sprintf("wl-%d", i+1)).Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)
					admitted = append(admitted, wl)
				}
			})

			ginkgo.By("Verifying initial workloads are admitted", func() {
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, admitted...)
			})

			ginkgo.By("Creating a 3rd workload that should stay pending (not enough credits)", func() {
				pendingWl = workloadWrapper.Clone().Name("wl").Obj()
				behavioral.MustCreate(ctx, k8sClient, pendingWl)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pendingWl)
			})
		})

		ginkgo.It("should admit the pending workload after one running workload finishes", func() {
			ginkgo.By("Marking one admitted workload as finished (frees 3 credits)", func() {
				integration.FinishWorkloads(ctx, k8sClient, admitted[0])
			})

			ginkgo.By("Waiting for the pending workload to be admitted (credits available again)", func() {
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pendingWl)
			})
		})

		ginkgo.It("should admit the pending workload after preempting a lower-priority workload", func() {
			ginkgo.By("Setting low priority of one admitted workload → making it preemptable", func() {
				createdWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admitted[0]), createdWl)).To(gomega.Succeed())
					createdWl.Spec.PriorityClassRef.Name = lowPriorityClassName
					createdWl.Spec.Priority = new(int32(lowPriority))
					g.Expect(k8sClient.Update(ctx, createdWl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.FinishEvictionForWorkloads(ctx, k8sClient, admitted[0])
			})

			ginkgo.By("Waiting for the pending workload to be admitted", func() {
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pendingWl)
			})
		})
	})
})
