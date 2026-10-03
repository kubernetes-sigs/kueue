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
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// A second-pass admission write fails with Conflict if the workload changed after the pass read it.
var _ = ginkgo.Describe("TopologyAwareScheduling: stale second-pass admission write", ginkgo.Ordered, func() {
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

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetupWithClientTransform(&config.Configuration{}, func(c client.Client) client.Client {
			return &secondPassBumpClient{Client: c, plain: k8sClient, wlKey: &wlKey, fired: &fired}
		}))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-fence-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("tas-node", "true").
			TopologyName(topology.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		nodeX1 = newNode("x1")
		nodeX2 = newNode("x2")
		util.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*nodeX1, *nodeX2})
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX1, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX2, true)
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
			util.MustCreate(ctx, k8sClient, wl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		ginkgo.By("dropping x1 so a second pass is queued", func() {
			util.SetNodeCondition(ctx, k8sClient, nodeX1, &corev1.NodeCondition{
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
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			gomega.Expect(fired.Load()).To(gomega.BeTrue(), "the extra write must have landed before the second-pass write")
		})

		// The ClusterQueue has 1 CPU, so wl2 can only be admitted if wl's reservation is lost.
		// Creating wl2 right after the conflict puts it in front of the scheduler before the second pass retries.
		var wl2 *kueue.Workload
		ginkgo.By("creating a competing workload that stays pending behind wl's reservation", func() {
			wl2 = newWorkload("wl2")
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})

		ginkgo.By("the competing workload does not get wl's quota after the conflict", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl2)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
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
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the competing workload is still pending", func() {
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
		})
	}

	ginkgo.It("rejects a stale second-pass admission write and keeps the workload's reservation with server-side apply", func() {
		runStaleWriteScenario(false)
	})

	ginkgo.It("rejects a stale second-pass admission write and keeps the workload's reservation with merge patch", func() {
		runStaleWriteScenario(true)
	})
})

// The wrapper that makes the race deterministic: one extra write, then forward the scheduler's write.
type secondPassBumpClient struct {
	client.Client
	plain client.Client
	wlKey *atomic.Pointer[types.NamespacedName]
	fired *atomic.Bool
}

func (c *secondPassBumpClient) Status() client.SubResourceWriter {
	return &bumpWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type bumpWriter struct {
	client.SubResourceWriter
	parent *secondPassBumpClient
}

// Status writes go through SSA apply by default, so the extra write lands just before the apply.
func (w *bumpWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	target, ok := obj.(interface {
		GetName() string
		GetNamespace() string
	})
	if !ok {
		return w.SubResourceWriter.Apply(ctx, obj, opts...)
	}
	if err := w.parent.bumpOnce(ctx, types.NamespacedName{Namespace: target.GetNamespace(), Name: target.GetName()}); err != nil {
		return err
	}
	return w.SubResourceWriter.Apply(ctx, obj, opts...)
}

// With WorkloadRequestUseMergePatch, status writes go through a merge patch, so the extra write lands just before the patch.
func (w *bumpWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	if _, ok := obj.(*kueue.Workload); ok {
		if err := w.parent.bumpOnce(ctx, client.ObjectKeyFromObject(obj)); err != nil {
			return err
		}
	}
	return w.SubResourceWriter.Patch(ctx, obj, patch, opts...)
}

// bumpOnce lands one extra write on the target workload the first time it is written while it lists an unhealthy node.
func (c *secondPassBumpClient) bumpOnce(ctx context.Context, key types.NamespacedName) error {
	if wlKey := c.wlKey.Load(); wlKey == nil || key != *wlKey || c.fired.Load() {
		return nil
	}
	var probe kueue.Workload
	if err := c.plain.Get(ctx, key, &probe); err != nil {
		return err
	}
	// In this spec only the second-pass write runs while wl lists an unhealthy node.
	if len(probe.Status.UnhealthyNodes) > 0 && c.fired.CompareAndSwap(false, true) {
		// A merge patch without an optimistic lock cannot conflict, so any Conflict comes from the scheduler's write.
		patch := client.MergeFrom(probe.DeepCopy())
		if probe.Annotations == nil {
			probe.Annotations = map[string]string{}
		}
		probe.Annotations["fence-test/bump"] = fmt.Sprintf("fired-%d", time.Now().UnixNano())
		return c.plain.Patch(ctx, &probe, patch)
	}
	return nil
}
