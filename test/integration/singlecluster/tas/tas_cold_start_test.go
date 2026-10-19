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
	"sync"
	"sync/atomic"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Topology Aware Scheduling cold start", ginkgo.Ordered, func() {
	var (
		ns          *corev1.Namespace
		nodes       []corev1.Node
		topology    *kueue.Topology
		tasFlavor   *kueue.ResourceFlavor
		cqs         []*kueue.ClusterQueue
		lqs         []*kueue.LocalQueue
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

		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, lq := range lqs {
			gomega.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, lq))).To(gomega.Succeed())
		}
		for _, cq := range cqs {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// Two ClusterQueues each own one admitted workload on its own hostname
	// and one pending workload that needs a whole hostname. On restart, the
	// informer replays ClusterQueues in no fixed order (the initial list goes
	// through a map backed store), so the test pauses after whichever of the
	// two is added first. At that point the other queue, and the usage of its
	// admitted workload, is not in the cache, so the other hostname looks free
	// to a scheduler that does not wait. The setup is symmetric, so the bug is
	// reachable for either replay order.
	ginkgo.It("should not admit onto a hostname used by a ClusterQueue that is not cached yet", func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-cold-")

		ginkgo.By("creating two hostnames with capacity for a single pod each", func() {
			for _, name := range []string{"node-a", "node-b"} {
				nodes = append(nodes, *testingnode.MakeNode(name).
					Label("node-group", "tas").
					Label(corev1.LabelHostname, name).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj())
			}
			integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

			topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
			behavioral.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(topology.Name).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, tasFlavor)
		})

		ginkgo.By("filling both hostnames and leaving one pending workload per ClusterQueue", func() {
			// The shared cohort requeues the pending workloads when the
			// running ones finish. TAS capacity itself is shared through the
			// flavor, not the cohort.
			for _, name := range []string{"cq-a", "cq-b"} {
				cqs = append(cqs, utiltestingapi.MakeClusterQueue(name).
					Cohort("shared").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
					Obj())
			}
			behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqs...)
			for _, cq := range cqs {
				lqs = append(lqs, utiltestingapi.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj())
			}
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqs...)

			for _, lq := range lqs {
				running := hostnameWorkload("running-"+lq.Name, ns.Name, lq.Name)
				behavioral.MustCreate(ctx, k8sClient, running)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, running)
			}
			for _, lq := range lqs {
				pending := hostnameWorkload("pending-"+lq.Name, ns.Name, lq.Name)
				behavioral.MustCreate(ctx, k8sClient, pending)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pending)
			}
		})

		releaseCh := make(chan struct{})
		var releaseOnce sync.Once
		releaseHook = func() {
			releaseOnce.Do(func() { close(releaseCh) })
		}
		var (
			firstCached atomic.Pointer[string]
			entered     = make(chan struct{})
		)
		core.SetClusterQueueAddedHookForTest(func(cq *kueue.ClusterQueue) {
			if cq.Name != cqs[0].Name && cq.Name != cqs[1].Name {
				return
			}
			if !firstCached.CompareAndSwap(nil, &cq.Name) {
				return
			}
			close(entered)
			<-releaseCh
		})

		ginkgo.By("restarting the manager and pausing after the first of the two ClusterQueues is cached", func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup())
			gomega.Eventually(entered, behavioral.Timeout).Should(gomega.BeClosed())
			ginkgo.GinkgoLogr.Info("Paused the ClusterQueue create handler", "cached", *firstCached.Load())
		})

		ginkgo.By("holding the create handler long enough for a scheduler that does not wait to admit", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				expectNoPendingWorkloadAdmitted(g, ns.Name, lqs)
			}, 2*time.Second, behavioral.ShortInterval).Should(gomega.Succeed())
			releaseHook()
		})

		ginkgo.By("checking the pending workloads stay pending once the rest of the cache is loaded", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				expectNoPendingWorkloadAdmitted(g, ns.Name, lqs)
			}, 3*time.Second, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the running workloads and admitting the pending ones", func() {
			var running, pending []*kueue.Workload
			for _, lq := range lqs {
				running = append(running, hostnameWorkload("running-"+lq.Name, ns.Name, lq.Name))
				pending = append(pending, hostnameWorkload("pending-"+lq.Name, ns.Name, lq.Name))
			}
			integration.FinishWorkloads(ctx, k8sClient, running...)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pending...)
			hosts := sets.New[string]()
			for _, wl := range pending {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				hosts.Insert(assignedHostnames(wl)...)
			}
			gomega.Expect(sets.List(hosts)).To(gomega.Equal([]string{"node-a", "node-b"}))
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

func expectNoPendingWorkloadAdmitted(g gomega.Gomega, namespace string, lqs []*kueue.LocalQueue) {
	for _, lq := range lqs {
		got := &kueue.Workload{}
		g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: namespace, Name: "pending-" + lq.Name}, got)).To(gomega.Succeed())
		g.Expect(workload.HasQuotaReservation(got)).To(gomega.BeFalse(), "workload %s got quota while both hostnames are in use", got.Name)
	}
}
