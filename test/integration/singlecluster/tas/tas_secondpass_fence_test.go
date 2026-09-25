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

// The second-pass admission write checks the entry's resourceVersion as a precondition; anything
// that touched the workload between evaluation and commit makes the apiserver answer Conflict.
var _ = ginkgo.Describe("TopologyAwareScheduling: second-pass admission write fence",
	ginkgo.Ordered, func() { //nolint:revive // the ginkgo Ordered func's closure pattern trips revive's empty-lines rule on layout, not on logic
		var (
			ns           *corev1.Namespace
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			nodeX1       *corev1.Node
			nodeX2       *corev1.Node
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
			fwk.StartManager(ctx, cfg, managerSetup())
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

		ginkgo.It("the failed-node replacement still completes through the RV fence, and the workload settles consistent", func() {
			var wl *kueue.Workload

			ginkgo.By("admitting a workload onto the healthy node x1", func() {
				wl = utiltestingapi.MakeWorkload("wl", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			x1Assignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []utiltas.TopologyDomainAssignment{
					{Count: 1, Values: []string{"x1"}},
				},
			})

			ginkgo.By("x1 reads as the assignment's domain", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(x1Assignment))
			})

			ginkgo.By("taking down x1 and letting the reconciler mark the workload", func() {
				util.SetNodeCondition(ctx, k8sClient, nodeX1, &corev1.NodeCondition{
					Type:               corev1.NodeReady,
					Status:             corev1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
				})
			})

			x2Assignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []utiltas.TopologyDomainAssignment{
					{Count: 1, Values: []string{"x2"}},
				},
			})

			ginkgo.By("the replacement pass fences through: x2 assignment stays, marks clear, reservation holds", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Admission).ToNot(gomega.BeNil(), "the reservation must survive the replacement pass")
					g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(x2Assignment))
					g.Expect(wl.Status.UnhealthyNodes).To(gomega.BeEmpty(), "marks must clear at the pass's commit")
					g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadQuotaReserved)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("a competing write inside the fence's window loses to it: conflict aborts, no state lost", func() {
			// The strict precondition only exists on the merge-patch path; the suite defaults it off.
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadRequestUseMergePatch, true)
			fired := &atomic.Bool{}
			fired.Store(false)
			wlKey := types.NamespacedName{Namespace: ns.Name, Name: "wl"}

			ginkgo.By("restarting the manager with the bump-wrapped scheduler client", func() {
				fwk.StopManager(ctx)
				fwk.StartManager(ctx, cfg, managerSetupWithClientTransform(&config.Configuration{}, func(c client.Client) client.Client {
					return &secondPassBumpClient{Client: c, plain: k8sClient, wlKey: &wlKey, fired: fired}
				}))
			})

			var wl *kueue.Workload
			ginkgo.By("recreating the workload in the pre-fail shape (admitted onto x1)", func() {
				wl = utiltestingapi.MakeWorkload("wl", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet("main", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj()
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

			ginkgo.By("the bumped commit is aborted: the wl's admission stays valid and its reservation never vanished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeTrue())
					g.Expect(wl.Status.Admission).ToNot(gomega.BeNil())
					g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ToNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the abort self-reports OutdatedScheduleCycle once", func() {
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
			})
		})

	})

// The wrapper that makes the race deterministic: one injected bump, then forward the write.
type secondPassBumpClient struct {
	client.Client
	plain client.Client
	wlKey *types.NamespacedName
	fired *atomic.Bool
}

func (c *secondPassBumpClient) Status() client.SubResourceWriter {
	return &bumpWriter{SubResourceWriter: c.Client.Status(), parent: c}
}

type bumpWriter struct {
	client.SubResourceWriter
	parent *secondPassBumpClient
}

// The fence's Update lands here: bump once so the write's RV is stale when it ships.
func (w *bumpWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return w.bumpThenForward(obj, func() error { return w.SubResourceWriter.Update(ctx, obj, opts...) })
}

// The fence's merge Patch lands here too, whether loose or strict.
func (w *bumpWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return w.bumpThenForward(obj, func() error { return w.SubResourceWriter.Patch(ctx, obj, patch, opts...) })
}

func (w *bumpWriter) bumpThenForward(obj client.Object, forward func() error) error {
	p := w.parent
	wl, ok := obj.(*kueue.Workload)
	if !ok || wl.Namespace != p.wlKey.Namespace || wl.Name != p.wlKey.Name || p.fired.Load() {
		return forward()
	}
	var probe kueue.Workload
	if err := p.plain.Get(ctx, *p.wlKey, &probe); err != nil {
		return err
	}
	// Second-pass writes are the ones carrying unhealthy-node marks; bump inside their window precisely.
	if len(probe.Status.UnhealthyNodes) == 0 || !p.fired.CompareAndSwap(false, true) {
		return forward()
	}
	if probe.Annotations == nil {
		probe.Annotations = map[string]string{}
	}
	probe.Annotations["fence-test/bump"] = fmt.Sprintf("fired-%d", time.Now().UnixNano())
	if err := p.plain.Update(ctx, &probe); err != nil {
		return err
	}
	return forward()
}
