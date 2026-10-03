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
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const coldStartPaddingQueues = 300

var _ = ginkgo.Describe("Topology Aware Scheduling cold start", ginkgo.Ordered, func() {
	var (
		ns          *corev1.Namespace
		node        *corev1.Node
		topology    *kueue.Topology
		tasFlavor   *kueue.ResourceFlavor
		victimCQ    *kueue.ClusterQueue
		occupantCQ  *kueue.ClusterQueue
		victimLQ    *kueue.LocalQueue
		occupantLQ  *kueue.LocalQueue
		padding     []*kueue.ClusterQueue
		releaseHook func()
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.AfterEach(func() {
		if releaseHook != nil {
			releaseHook()
			releaseHook = nil
		}
		core.SetClusterQueueAddedHookForTest(nil)
		if ns == nil {
			return
		}

		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		if victimLQ != nil {
			gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, victimLQ))).To(gomega.Succeed())
		}
		if occupantLQ != nil {
			gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, occupantLQ))).To(gomega.Succeed())
		}
		for _, cq := range append([]*kueue.ClusterQueue{victimCQ, occupantCQ}, padding...) {
			if cq == nil {
				continue
			}
			gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cq))).To(gomega.Succeed())
		}
		gomega.Eventually(func(g gomega.Gomega) {
			var listed kueue.ClusterQueueList
			g.Expect(k8sClient.List(ctx, &listed)).To(gomega.Succeed())
			left := 0
			for i := range listed.Items {
				name := listed.Items[i].Name
				if name == "a-victim" || name == "z-occupant" || (len(name) >= 6 && name[:6] == "m-pad-") {
					left++
				}
			}
			g.Expect(left).To(gomega.Equal(0))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should not give the victim the occupied hostname after a cold start", func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-cold-")

		ginkgo.By("creating one hostname with capacity for a single pod", func() {
			node = testingnode.MakeNode("node-1").
				Label("node-group", "tas").
				Label(corev1.LabelHostname, "node-1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("1"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj()
			util.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})

			topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(topology.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
		})

		// a-victim sorts before the padding queues, and z-occupant sorts after
		// them. The create handler is synchronous, so pausing on a-victim holds
		// every later ClusterQueue, including the occupant, out of the cache.
		ginkgo.By("admitting the occupant on node-1 and leaving the victim pending", func() {
			// The shared cohort is what requeues the victim when the occupant
			// finishes. TAS capacity itself is shared through the flavor, not
			// the cohort.
			victimCQ = utiltestingapi.MakeClusterQueue("a-victim").
				Cohort("shared").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			occupantCQ = utiltestingapi.MakeClusterQueue("z-occupant").
				Cohort("shared").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, victimCQ, occupantCQ)

			victimLQ = utiltestingapi.MakeLocalQueue("victim-lq", ns.Name).ClusterQueue(victimCQ.Name).Obj()
			occupantLQ = utiltestingapi.MakeLocalQueue("occupant-lq", ns.Name).ClusterQueue(occupantCQ.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, victimLQ, occupantLQ)

			occupant := hostnameWorkload("occupant", ns.Name, occupantLQ.Name)
			util.MustCreate(ctx, k8sClient, occupant)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, occupant)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(occupant), occupant)).To(gomega.Succeed())
			gomega.Expect(assignedHostnames(occupant)).To(gomega.ContainElement("node-1"))

			victim := hostnameWorkload("victim", ns.Name, victimLQ.Name)
			util.MustCreate(ctx, k8sClient, victim)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, victim)
		})

		ginkgo.By(fmt.Sprintf("creating %d empty ClusterQueues so the victim handler is not last", coldStartPaddingQueues), func() {
			padding = make([]*kueue.ClusterQueue, 0, coldStartPaddingQueues)
			for i := range coldStartPaddingQueues {
				cq := utiltestingapi.MakeClusterQueue(fmt.Sprintf("m-pad-%03d", i)).Obj()
				util.MustCreate(ctx, k8sClient, cq)
				padding = append(padding, cq)
			}
		})

		releaseCh := make(chan struct{})
		var releaseOnce sync.Once
		releaseHook = func() {
			releaseOnce.Do(func() { close(releaseCh) })
		}
		entered := make(chan struct{})
		var enteredOnce sync.Once
		core.SetClusterQueueAddedHookForTest(func(cq *kueue.ClusterQueue) {
			if cq.Name != victimCQ.Name {
				return
			}
			enteredOnce.Do(func() { close(entered) })
			<-releaseCh
		})

		ginkgo.By("restarting the manager while the victim queue is cached and the occupant is not", func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup())
			gomega.Eventually(entered, util.Timeout).Should(gomega.BeClosed())
		})

		victimKey := client.ObjectKey{Namespace: ns.Name, Name: "victim"}
		ginkgo.By("holding the create handler long enough for an ungated scheduler to admit", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				assertVictimDoesNotOwnNode(g, victimKey)
			}, time.Second, util.ShortInterval).Should(gomega.Succeed())
			releaseHook()
		})

		ginkgo.By("checking the victim stays pending once the rest of the cache is loaded", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				assertVictimDoesNotOwnNode(g, victimKey)
			}, 5*time.Second, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the occupant and admitting the victim on node-1", func() {
			occupant := hostnameWorkload("occupant", ns.Name, occupantLQ.Name)
			util.FinishWorkloads(ctx, k8sClient, occupant)
			victim := hostnameWorkload("victim", ns.Name, victimLQ.Name)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)
			got := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, victimKey, got)).To(gomega.Succeed())
			gomega.Expect(assignedHostnames(got)).To(gomega.ContainElement("node-1"))
		})
	})
})

func hostnameWorkload(name, ns, localQueue string) *kueue.Workload {
	podSet := utiltestingapi.MakePodSet("worker", 1).
		RequiredTopologyRequest(corev1.LabelHostname).
		Request(corev1.ResourceCPU, "1")
	return utiltestingapi.MakeWorkload(name, ns).
		Queue(kueue.LocalQueueName(localQueue)).
		PodSets(*podSet.Obj()).
		Obj()
}

func assignedHostnames(wl *kueue.Workload) []string {
	if wl.Status.Admission == nil {
		return nil
	}
	var hosts []string
	for i := range wl.Status.Admission.PodSetAssignments {
		internal := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[i].TopologyAssignment)
		if internal == nil {
			continue
		}
		for _, domain := range internal.Domains {
			hosts = append(hosts, domain.Values...)
		}
	}
	return hosts
}

func assertVictimDoesNotOwnNode(g gomega.Gomega, key client.ObjectKey) {
	got := &kueue.Workload{}
	g.Expect(k8sClient.Get(ctx, key, got)).To(gomega.Succeed())
	g.Expect(workload.HasQuotaReservation(got)).To(gomega.BeFalse())
	g.Expect(assignedHostnames(got)).NotTo(gomega.ContainElement("node-1"))
}
