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
	"fmt"
	"sync/atomic"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

// A second-pass admission write fails with Conflict if the workload changed after the pass read it.
var _ = ginkgo.Describe("TopologyAwareScheduling: stale second-pass admission write", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		nodeX1       *corev1.Node
		nodeX2       *corev1.Node
		// The spec sets the target before creating it; the wrapper does nothing while it is nil.
		wlKey atomic.Pointer[types.NamespacedName]
		fired atomic.Bool
	)

	newNode := func(name string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithClientTransform(&config.Configuration{}, func(c client.Client) client.Client {
			return &secondPassWriteClient{Client: c, plain: k8sClient, wlKey: &wlKey, fired: &fired, onWrite: bumpUnrelatedAnnotation}
		}))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-fence-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("tas-node", "true").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		nodeX1 = newNode("x1")
		nodeX2 = newNode("x2")
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*nodeX1, *nodeX2})
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX1, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX2, true)
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	runStaleWriteScenario := func(useMergePatch bool) {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRequestUseMergePatch, useMergePatch)
		fired.Store(false)
		wlKey.Store(&types.NamespacedName{Namespace: ns.Name, Name: "wl"})

		newWorkload := func(name string) *kueue.Workload {
			return utiltestingapi.MakeWorkload(name, ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
		}

		var wl *kueue.Workload
		ginkgo.By("admitting a workload onto x1", func() {
			wl = newWorkload("wl")
			behavioral.MustCreate(ctx, k8sClient, wl)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		ginkgo.By("dropping x1 so a second pass is queued", func() {
			behavioral.SetNodeCondition(ctx, k8sClient, nodeX1, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
			})
		})

		ginkgo.By("the stale write is rejected and reported with an OutdatedScheduleCycle event", func() {
			gomega.Eventually(func(g gomega.Gomega) bool {
				var evs corev1.EventList
				g.Expect(k8sClient.List(ctx, &evs, client.InNamespace(ns.Name))).To(gomega.Succeed())
				for _, e := range evs.Items {
					if e.Reason == "OutdatedScheduleCycle" {
						return true
					}
				}
				return false
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeTrue())
			gomega.Expect(fired.Load()).To(gomega.BeTrue(), "the extra write must have landed before the second-pass write")
		})

		// The ClusterQueue has 1 CPU, so wl2 can only be admitted if wl's reservation is lost.
		// Creating wl2 right after the conflict puts it in front of the scheduler before the second pass retries.
		var wl2 *kueue.Workload
		ginkgo.By("creating a competing workload that stays pending behind wl's reservation", func() {
			wl2 = newWorkload("wl2")
			behavioral.MustCreate(ctx, k8sClient, wl2)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})

		ginkgo.By("the competing workload does not get wl's quota after the conflict", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl2)).To(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		x2Assignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
			Levels: []string{corev1.LabelHostname},
			Domains: []utiltas.TopologyDomainAssignment{
				{Count: 1, Values: []string{"x2"}},
			},
		})

		ginkgo.By("the retried second pass moves wl to x2 and clears the unhealthy node", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Admission).ToNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(x2Assignment))
				g.Expect(wl.Status.UnhealthyNodes).To(gomega.BeEmpty())
				g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadQuotaReserved)).To(gomega.BeTrue())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the competing workload is still pending", func() {
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})
	}

	ginkgo.It("rejects a stale second-pass admission write and keeps the workload's reservation with server-side apply", func() {
		runStaleWriteScenario(false)
	})

	ginkgo.It("rejects a stale second-pass admission write and keeps the workload's reservation with merge patch", func() {
		runStaleWriteScenario(true)
	})
})

// A second-pass fail-fast eviction is rejected if the workload changed after the pass read it.
var _ = ginkgo.Describe("TopologyAwareScheduling: stale second-pass fail-fast eviction", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		nodeX1       *corev1.Node
		wl           *kueue.Workload
		// The wrapper sets the target before the workload exists; it does nothing while it is nil.
		wlKey atomic.Pointer[types.NamespacedName]
		fired atomic.Bool
	)

	newNode := func(name string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-fence-")
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
	})

	// createFixtures creates the queue and the only node after the manager is up, whose webhooks serve the creates.
	createFixtures := func() {
		ginkgo.GinkgoHelper()
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX1, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("tas-node", "true").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		// The only node is the one the workload occupies, so a replacement for it never fits.
		nodeX1 = newNode("x1")
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*nodeX1})
	}

	// expectEvent waits for an event with the given reason and message on wl.
	expectEvent := func(reason, message string) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) bool {
			var events corev1.EventList
			g.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
			for _, e := range events.Items {
				if e.Reason == reason && e.InvolvedObject.Name == wl.Name && e.Message == message {
					return true
				}
			}
			return false
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeTrue(), "expected a %s event for %s", reason, wl.Name)
	}

	// admitOnTheOnlyNode admits wl and fails its node, so the failed replacement has no room.
	admitOnTheOnlyNode := func() {
		ginkgo.GinkgoHelper()
		wl = utiltestingapi.MakeWorkload("wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		// The workload goes first: its reservation keeps the ClusterQueue from being deleted.
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		})
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		ginkgo.By("failing the only node so no replacement fits", func() {
			behavioral.SetNodeCondition(ctx, k8sClient, nodeX1, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
			})
			behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, nodeX1.Name)
		})
	}

	// runScenario starts the manager with the extra write and brings up the workload on a failed node.
	runScenario := func(useMergePatch bool, onWrite func(ctx context.Context, c *secondPassWriteClient, key types.NamespacedName) error) {
		ginkgo.GinkgoHelper()
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRequestUseMergePatch, useMergePatch)
		fired.Store(false)
		wlKey.Store(&types.NamespacedName{Namespace: ns.Name, Name: "wl"})
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithClientTransform(&config.Configuration{}, func(c client.Client) client.Client {
			return &secondPassWriteClient{Client: c, plain: k8sClient, wlKey: &wlKey, fired: &fired, onWrite: onWrite}
		}))
		createFixtures()
		admitOnTheOnlyNode()
	}

	runHealedScenario := func(useMergePatch bool) {
		runScenario(useMergePatch, healFailedNode)

		ginkgo.By("the stale eviction is rejected and reported with an OutdatedScheduleCycle event", func() {
			expectEvent("OutdatedScheduleCycle", "Skipped eviction because the workload changed while it was being scheduled")
			gomega.Expect(fired.Load()).To(gomega.BeTrue(), "the node must have been healed before the eviction write")
		})

		ginkgo.By("the workload keeps its admission and its unhealthy node is cleared", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var got kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &got)).To(gomega.Succeed())
				g.Expect(got.Status.UnhealthyNodes).To(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				var got kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &got)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(&got)).To(gomega.BeTrue())
				g.Expect(apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeNil())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	}

	runBumpedScenario := func(useMergePatch bool) {
		runScenario(useMergePatch, bumpUnrelatedAnnotation)

		ginkgo.By("the skipped eviction is retried from a fresh read and evicts the workload", func() {
			expectEvent("OutdatedScheduleCycle", "Skipped eviction because the workload changed while it was being scheduled")
			expectEvent("EvictedDueToNodeFailures", "Workload was evicted as there was no replacement for unhealthy node(s): x1")
			gomega.Eventually(func(g gomega.Gomega) {
				var got kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &got)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadEvictedDueToNodeFailures))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(fired.Load()).To(gomega.BeTrue(), "the extra write must have landed before the eviction")
		})
	}

	ginkgo.It("keeps the workload admitted when its node heals just before the eviction with server-side apply", func() {
		runHealedScenario(false)
	})

	ginkgo.It("keeps the workload admitted when its node heals just before the eviction with merge patch", func() {
		runHealedScenario(true)
	})

	ginkgo.It("evicts the workload from the retried pass after an unrelated write with server-side apply", func() {
		runBumpedScenario(false)
	})

	ginkgo.It("evicts the workload from the retried pass after an unrelated write with merge patch", func() {
		runBumpedScenario(true)
	})
})

// The wrapper that makes the race deterministic: one extra write, then forward the scheduler's write.
type secondPassWriteClient struct {
	client.Client
	plain client.Client
	wlKey *atomic.Pointer[types.NamespacedName]
	fired *atomic.Bool
	// onWrite makes the extra write; it must be a no-op once it has landed.
	onWrite func(ctx context.Context, c *secondPassWriteClient, key types.NamespacedName) error
}

// shouldFire reports whether key is the target workload and the extra write has not landed yet.
func (c *secondPassWriteClient) shouldFire(key types.NamespacedName) bool {
	wlKey := c.wlKey.Load()
	return wlKey != nil && key == *wlKey && !c.fired.Load()
}

func (c *secondPassWriteClient) Status() client.SubResourceWriter {
	return &extraWriteWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type extraWriteWriter struct {
	client.SubResourceWriter
	parent *secondPassWriteClient
}

// Status writes go through SSA apply by default, so the extra write lands just before the apply.
func (w *extraWriteWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	target, ok := obj.(interface {
		GetName() string
		GetNamespace() string
	})
	if !ok {
		return w.SubResourceWriter.Apply(ctx, obj, opts...)
	}
	if err := w.parent.onWrite(ctx, w.parent, types.NamespacedName{Namespace: target.GetNamespace(), Name: target.GetName()}); err != nil {
		return err
	}
	return w.SubResourceWriter.Apply(ctx, obj, opts...)
}

// With WorkloadRequestUseMergePatch, status writes go through a merge patch, so the extra write lands just before the patch.
func (w *extraWriteWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if _, ok := obj.(*kueue.Workload); ok {
		if err := w.parent.onWrite(ctx, w.parent, client.ObjectKeyFromObject(obj)); err != nil {
			return err
		}
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

// bumpUnrelatedAnnotation lands one unrelated write on the target workload the first time it is written while it lists an unhealthy node.
func bumpUnrelatedAnnotation(ctx context.Context, c *secondPassWriteClient, key types.NamespacedName) error {
	if !c.shouldFire(key) {
		return nil
	}
	var probe kueue.Workload
	if err := c.plain.Get(ctx, key, &probe); err != nil {
		return err
	}
	// In these specs only the write under test runs while wl lists an unhealthy node.
	if len(probe.Status.UnhealthyNodes) == 0 || !c.fired.CompareAndSwap(false, true) {
		return nil
	}
	// A merge patch without an optimistic lock cannot conflict, so any Conflict comes from the scheduler's write.
	patch := client.MergeFrom(probe.DeepCopy())
	if probe.Annotations == nil {
		probe.Annotations = map[string]string{}
	}
	probe.Annotations["fence-test/bump"] = fmt.Sprintf("fired-%d", time.Now().UnixNano())
	return c.plain.Patch(ctx, &probe, patch)
}

// healFailedNode makes the failed node Ready again and clears the Workload's unhealthy node, as a node recovery does.
func healFailedNode(ctx context.Context, c *secondPassWriteClient, key types.NamespacedName) error {
	if !c.shouldFire(key) {
		return nil
	}
	var probe kueue.Workload
	if err := c.plain.Get(ctx, key, &probe); err != nil {
		return err
	}
	if len(probe.Status.UnhealthyNodes) == 0 || !c.fired.CompareAndSwap(false, true) {
		return nil
	}
	// The scheduler's goroutine runs this write, so it returns errors instead of calling Eventually.
	if err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var node corev1.Node
		if err := c.plain.Get(ctx, types.NamespacedName{Name: probe.Status.UnhealthyNodes[0].Name}, &node); err != nil {
			return err
		}
		ready := utiltas.GetNodeCondition(&node, corev1.NodeReady)
		if ready == nil {
			node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeReady})
			ready = &node.Status.Conditions[len(node.Status.Conditions)-1]
		}
		ready.Status = corev1.ConditionTrue
		ready.LastTransitionTime = metav1.NewTime(time.Now())
		return c.plain.Status().Update(ctx, &node)
	}); err != nil {
		return err
	}
	// The write also leaves the scheduler's eviction carrying an outdated resourceVersion.
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		var updated kueue.Workload
		if err := c.plain.Get(ctx, key, &updated); err != nil {
			return err
		}
		updated.Status.UnhealthyNodes = nil
		return c.plain.Status().Update(ctx, &updated)
	})
}
