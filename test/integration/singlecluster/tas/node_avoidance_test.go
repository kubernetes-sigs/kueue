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

package core

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

func managerSetupWithNodeAvoidance(ctx context.Context, mgr manager.Manager) {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	failedWebhook, err := webhooks.Setup(mgr)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	controllersCfg := &config.Configuration{}
	mgr.GetScheme().Default(controllersCfg)

	ginkgo.GinkgoWriter.Printf("Feature FailureAwareScheduling enabled: %v\n", features.Enabled(features.NodeAvoidanceScheduling))

	cacheOptions := []schdcache.Option{
		schdcache.WithAvoidNodeLabel("unhealthy"),
	}
	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	queues := qcache.NewManager(mgr.GetClient(), cCache)

	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "Core controller", failedCtrl)

	failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, controllersCfg)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

	err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	reconciler, err := provisioning.NewController(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = reconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
	err = sched.Start(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.Describe("TAS Node Avoidance", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		err := features.SetEnable(features.NodeAvoidanceScheduling, true)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fwk.StartManager(ctx, cfg, managerSetupWithNodeAvoidance)
		ginkgo.DeferCleanup(fwk.StopManager, ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-node-avoidance-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

		ginkgo.When("Using Node Avoidance Policies", func() {
			var (
				tasFlavor    *kueue.ResourceFlavor
				topology     *kueue.Topology
				localQueue   *kueue.LocalQueue
				clusterQueue *kueue.ClusterQueue
				nodes        []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("node-healthy").
						Label("node-group", "tas").
						Label("kubernetes.io/hostname", "node-healthy").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("node-unhealthy").
						Label("node-group", "tas").
						Label("kubernetes.io/hostname", "node-unhealthy").
						Label("unhealthy", "true").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				for _, node := range nodes {
					ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true) })
				}

				topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true) })

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true) })

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true) })
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true) })
			})

			ginkgo.It("should prefer healthy nodes when policy is PreferHealthy", func() {
				ginkgo.By("creating a workload with PreferHealthy policy", func() {
					wl := utiltestingapi.MakeWorkload("wl-prefer-healthy", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Request(corev1.ResourceCPU, "1").
						Annotations(map[string]string{
							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyPreferNoSchedule,
						}).
						Obj()
					ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true) })
					util.MustCreate(ctx, k8sClient, wl)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
						g.Expect(utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values).To(gomega.ContainElement("node-healthy"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should disallow unhealthy nodes when policy is DisallowUnhealthy", func() {
				ginkgo.By("tainting the healthy node to make it unavailable", func() {
					node := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "node-healthy"}, node)).To(gomega.Succeed())
					node.Spec.Unschedulable = true
					gomega.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "node-healthy"}, node)).To(gomega.Succeed())
						g.Expect(node.Spec.Unschedulable).To(gomega.BeTrue())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("creating another workload with DisallowUnhealthy policy, should remain pending", func() {
					wl := utiltestingapi.MakeWorkload("wl-disallow-unhealthy", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Request(corev1.ResourceCPU, "1").
						Annotations(map[string]string{
							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyRequired,
						}).
						Obj()
					ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true) })
					util.MustCreate(ctx, k8sClient, wl)

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						if workload.IsAdmitted(wl) {
							ginkgo.GinkgoWriter.Printf("Workload %s unexpectedly admitted. Assignments: %v\n", wl.Name, wl.Status.Admission.PodSetAssignments)
						}
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("creating another workload with DisallowUnhealthy policy, should remain pending", func() {
					wl := utiltestingapi.MakeWorkload("wl-disallow-unhealthy-pending", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Request(corev1.ResourceCPU, "1").
						Annotations(map[string]string{
							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyRequired,
						}).
						Obj()
					util.MustCreate(ctx, k8sClient, wl)

					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
				})
			})
		})
})