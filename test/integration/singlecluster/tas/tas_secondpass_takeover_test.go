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
	"context"
	"sync/atomic"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

// A scheduler that starts after the workload recovered must not act on the second-pass copy it queued before.
var _ = ginkgo.Describe("TopologyAwareScheduling: second pass copy of a repaired workload", func() {
	var (
		ns        *corev1.Namespace
		topology  *kueue.Topology
		tasFlavor *kueue.ResourceFlavor
		tasCQ     *kueue.ClusterQueue
		tasLQ     *kueue.LocalQueue
		plainCQ   *kueue.ClusterQueue
		plainLQ   *kueue.LocalQueue
		nodeX1    *corev1.Node
		// The spec sets the target before creating it; the wrapper counts no reads while it is nil.
		wlKey atomic.Pointer[types.NamespacedName]
		reads atomic.Int32
	)

	ginkgo.BeforeEach(func() {
		// The scheduler is held back so that the second pass only fires while nobody consumes it.
		schedulerStartGate = make(chan struct{})
		queueClientTransform = func(c client.Client) client.Client {
			return &secondPassReadClient{Client: c, wlKey: &wlKey, reads: &reads}
		}
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithConfig(&config.Configuration{}))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-takeover-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("tas-node", "true").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		tasCQ = utiltestingapi.MakeClusterQueue("tas-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		tasLQ = utiltestingapi.MakeLocalQueue("tas-local-queue", ns.Name).ClusterQueue(tasCQ.Name).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, tasCQ)
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, tasLQ)

		// A workload outside the topology proves that the released scheduler runs its cycles.
		plainFlavor := utiltestingapi.MakeResourceFlavor("plain-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, plainFlavor)
		plainCQ = utiltestingapi.MakeClusterQueue("plain-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(plainFlavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		plainLQ = utiltestingapi.MakeLocalQueue("plain-local-queue", ns.Name).ClusterQueue(plainCQ.Name).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, plainCQ)
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, plainLQ)

		nodeX1 = testingnode.MakeNode("x1").
			Label("tas-node", "true").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*nodeX1})
	})

	ginkgo.AfterEach(func() {
		schedulerStartGate = nil
		queueClientTransform = nil
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasLQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, plainLQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, plainCQ, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX1, true)
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	runTakeoverScenario := func(useMergePatch bool) {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRequestUseMergePatch, useMergePatch)
		reads.Store(0)
		wlKey.Store(&types.NamespacedName{Namespace: ns.Name, Name: "wl"})

		newWorkload := func(name string, lq *kueue.LocalQueue, tas bool) *kueue.Workload {
			podSet := utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1")
			if tas {
				podSet = podSet.RequiredTopologyRequest(corev1.LabelHostname)
			}
			return utiltestingapi.MakeWorkload(name, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				PodSets(*podSet.Obj()).
				Obj()
		}

		// The only TAS node is x1, so a replacement for x1 cannot fit anywhere.
		var wl *kueue.Workload
		ginkgo.By("admitting a workload onto x1", func() {
			wl = newWorkload("wl", tasLQ, true)
			behavioral.MustCreate(ctx, k8sClient, wl)
			admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(tasCQ.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment("main").
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(tasFlavor.Name), "1000m").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
						Obj()).
					Obj()).
				Obj()
			integration.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), admission)
			integration.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
		})

		ginkgo.By("dropping x1 so the workload is queued for a second pass", func() {
			behavioral.SetNodeCondition(ctx, k8sClient, nodeX1, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
			})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(workload.HasUnhealthyNode(wl, "x1")).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting until the second pass fires", func() {
			// While the scheduler is held back, only the fire-time re-read reads this workload, and it holds the second-pass lock until the copy is stored.
			gomega.Eventually(reads.Load, behavioral.Timeout, behavioral.Interval).Should(gomega.BeNumerically(">=", 1))
		})

		ginkgo.By("recovering x1 so the workload no longer needs the pass", func() {
			// The marker annotation is a later update of the same object, so its arrival in the cache shows the handler ran.
			const recoveredMarker = "test.kueue.x-k8s.io/recovered"

			behavioral.SetNodeCondition(ctx, k8sClient, nodeX1, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now()),
			})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(wl.Status.UnhealthyNodes).To(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			patched := wl.DeepCopy()
			if patched.Annotations == nil {
				patched.Annotations = make(map[string]string)
			}
			patched.Annotations[recoveredMarker] = "true"
			gomega.Expect(k8sClient.Patch(ctx, patched, client.MergeFrom(wl))).To(gomega.Succeed())
			// The handler updates the cache just before it drops the copy, so the marker proves the drop.
			gomega.Eventually(func(g gomega.Gomega) {
				snapshot, err := schedulerCache.Snapshot(ctx)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(tasCQ.Name))
				g.Expect(cq).NotTo(gomega.BeNil())
				info, found := cq.Workloads[workload.Key(wl)]
				g.Expect(found).To(gomega.BeTrue())
				g.Expect(info.Obj.Status.UnhealthyNodes).To(gomega.BeEmpty())
				g.Expect(info.Obj.Annotations).To(gomega.HaveKeyWithValue(recoveredMarker, "true"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("starting the scheduler", func() {
			close(schedulerStartGate)
		})

		ginkgo.By("admitting a workload outside the topology", func() {
			liveness := newWorkload("liveness", plainLQ, false)
			behavioral.MustCreate(ctx, k8sClient, liveness)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, liveness)
		})

		ginkgo.By("keeping the repaired workload admitted", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				var got kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &got)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
				g.Expect(apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeNil())
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("reporting no second pass for the repaired workload", func() {
			var events corev1.EventList
			gomega.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
			for _, event := range events.Items {
				if event.InvolvedObject.Name == wl.Name {
					gomega.Expect(event.Reason).NotTo(gomega.BeElementOf("OutdatedScheduleCycle", "SecondPassFailed"),
						"the second pass acted on a copy of the repaired workload")
				}
			}
		})
	}

	ginkgo.It("ignores the copy of a repaired workload with server-side apply", func() {
		runTakeoverScenario(false)
	})

	ginkgo.It("ignores the copy of a repaired workload with merge patch", func() {
		runTakeoverScenario(true)
	})
})

// secondPassReadClient counts the queue manager's reads of one workload.
type secondPassReadClient struct {
	client.Client
	wlKey *atomic.Pointer[types.NamespacedName]
	reads *atomic.Int32
}

func (c *secondPassReadClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if wlKey := c.wlKey.Load(); wlKey != nil && key == *wlKey {
		c.reads.Add(1)
	}
	return c.Client.Get(ctx, key, obj, opts...)
}
