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

//
package core
//
//import (
//	"context"
//
//	"github.com/onsi/ginkgo/v2"
//	"github.com/onsi/gomega"
//	corev1 "k8s.io/api/core/v1"
//	// "k8s.io/apimachinery/pkg/api/resource"
//	"sigs.k8s.io/controller-runtime/pkg/client"
//	"sigs.k8s.io/controller-runtime/pkg/manager"
//
//	config "sigs.k8s.io/kueue/apis/config/v1beta2"
//	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
//	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
//	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
//	"sigs.k8s.io/kueue/pkg/constants"
//	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
//	// ctrlconstants "sigs.k8s.io/kueue/pkg/controller/constants"
//	"sigs.k8s.io/kueue/pkg/controller/core"
//	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
//	"sigs.k8s.io/kueue/pkg/controller/tas"
//	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
//	"sigs.k8s.io/kueue/pkg/features"
//	"sigs.k8s.io/kueue/pkg/scheduler"
//	// utiltas "sigs.k8s.io/kueue/pkg/util/tas"
//	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
//	// testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
//	"sigs.k8s.io/kueue/pkg/webhooks"
//	// "sigs.k8s.io/kueue/pkg/workload"
//	"sigs.k8s.io/kueue/test/util"
//)
//
//func managerSetupWithNodeAvoidance(ctx context.Context, mgr manager.Manager) {
//	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
//
//	failedWebhook, err := webhooks.Setup(mgr)
//	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)
//
//	controllersCfg := &config.Configuration{}
//	mgr.GetScheme().Default(controllersCfg)
//
//	cacheOptions := []schdcache.Option{
//		schdcache.WithUnhealthyNodeLabel("unhealthy"),
//	}
//	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
//	queues := qcache.NewManager(mgr.GetClient(), cCache)
//
//	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg)
//	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "Core controller", failedCtrl)
//
//	failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, controllersCfg)
//	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)
//
//	err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
//
//	err = provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer())
//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
//
//	reconciler, err := provisioning.NewController(
//		mgr.GetClient(),
//		mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
//
//	err = reconciler.SetupWithManager(mgr)
//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
//
//	sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
//	err = sched.Start(ctx)
//	gomega.Expect(err).NotTo(gomega.HaveOccurred())
//}
//
//var _ = ginkgo.Describe("TAS Node Avoidance", ginkgo.Ordered, func() {
//	var (
//		ns *corev1.Namespace
//	)
//
//	ginkgo.BeforeAll(func() {
//		err := features.SetEnable(features.FailureAwareScheduling, true)
//		gomega.Expect(err).NotTo(gomega.HaveOccurred())
//		fwk.StartManager(ctx, cfg, managerSetupWithNodeAvoidance)
//	})
//
//	ginkgo.AfterAll(func() {
//		fwk.StopManager(ctx)
//	})
//
//	ginkgo.BeforeEach(func() {
//		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-node-avoidance-")
//	})
//
//	ginkgo.AfterEach(func() {
//		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
//	})
//
//	ginkgo.When("Using Node Avoidance Policies", func() {
//		var (
//			tasFlavor    *kueue.ResourceFlavor
//			topology     *kueue.Topology
//			localQueue   *kueue.LocalQueue
//			clusterQueue *kueue.ClusterQueue
//			nodes        []corev1.Node
//		)
//
//		ginkgo.BeforeEach(func() {
//			nodes = []corev1.Node{
//				*testingnode.MakeNode("node-healthy").
//					Label("node-group", "tas").
//					Label("kubernetes.io/hostname", "node-healthy").
//					StatusAllocatable(corev1.ResourceList{
//						corev1.ResourceCPU: resource.MustParse("1"),
//					}).
//	/*
//		ginkgo.When("Using Node Avoidance Policies", func() {
//			var (
//				tasFlavor    *kueue.ResourceFlavor
//				topology     *kueue.Topology
//				localQueue   *kueue.LocalQueue
//				clusterQueue *kueue.ClusterQueue
//				nodes        []corev1.Node
//			)
//
//			ginkgo.BeforeEach(func() {
//				nodes = []corev1.Node{
//					*testingnode.MakeNode("node-healthy").
//						Label("node-group", "tas").
//						Label("kubernetes.io/hostname", "node-healthy").
//						StatusAllocatable(corev1.ResourceList{
//							corev1.ResourceCPU: resource.MustParse("1"),
//						}).
//						Ready().
//						Obj(),
//					*testingnode.MakeNode("node-unhealthy").
//						Label("node-group", "tas").
//						Label("kubernetes.io/hostname", "node-unhealthy").
//						Label("unhealthy", "true").
//						StatusAllocatable(corev1.ResourceList{
//							corev1.ResourceCPU: resource.MustParse("1"),
//						}).
//						Ready().
//						Obj(),
//				}
//				util.CreateNodesWithStatus(ctx, k8sClient, nodes)
//
//				topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
//				util.MustCreate(ctx, k8sClient, topology)
//
//				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
//					NodeLabel("node-group", "tas").
//					TopologyName("default").Obj()
//				util.MustCreate(ctx, k8sClient, tasFlavor)
//
//				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
//					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
//					Obj()
//				util.MustCreate(ctx, k8sClient, clusterQueue)
//				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
//
//				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
//				util.MustCreate(ctx, k8sClient, localQueue)
//			})
//
//			ginkgo.AfterEach(func() {
//				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
//				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
//				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
//				for _, node := range nodes {
//					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
//				}
//			})
//
//			ginkgo.It("should prefer healthy nodes when policy is PreferHealthy", func() {
//				ginkgo.By("creating a workload with PreferHealthy policy", func() {
//					wl := utiltestingapi.MakeWorkload("wl-prefer-healthy", ns.Name).
//						Queue(kueue.LocalQueueName(localQueue.Name)).
//						Request(corev1.ResourceCPU, "1").
//						Annotations(map[string]string{
//							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyPreferHealthy,
//						}).
//						Obj()
//					util.MustCreate(ctx, k8sClient, wl)
//
//					gomega.Eventually(func(g gomega.Gomega) {
//						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
//						if !workload.IsAdmitted(wl) {
//							ginkgo.GinkgoWriter.Printf("Workload %s not admitted. Conditions: %v\n", wl.Name, wl.Status.Conditions)
//						}
//						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
//					}, util.Timeout, util.Interval).Should(gomega.Succeed())
//					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
//					// Should be assigned to node-healthy
//					gomega.Expect(utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values).Should(gomega.ContainElement("node-healthy"))
//				})
//
//				ginkgo.By("creating another workload with PreferHealthy policy, should fallback to unhealthy node", func() {
//					wl := utiltestingapi.MakeWorkload("wl-prefer-healthy-fallback", ns.Name).
//						Queue(kueue.LocalQueueName(localQueue.Name)).
//						Request(corev1.ResourceCPU, "1").
//						Annotations(map[string]string{
//							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyPreferHealthy,
//						}).
//						Obj()
//					util.MustCreate(ctx, k8sClient, wl)
//
//					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
//
//					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
//					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
//					// Should be assigned to node-unhealthy because node-healthy is full
//					gomega.Expect(utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values).Should(gomega.ContainElement("node-unhealthy"))
//				})
//			})
//
//			ginkgo.It("should disallow unhealthy nodes when policy is DisallowUnhealthy", func() {
//				ginkgo.By("creating a workload with DisallowUnhealthy policy", func() {
//					wl := utiltestingapi.MakeWorkload("wl-disallow-unhealthy", ns.Name).
//						Queue(kueue.LocalQueueName(localQueue.Name)).
//						Request(corev1.ResourceCPU, "1").
//						Annotations(map[string]string{
//							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyDisallowUnhealthy,
//						}).
//						Obj()
//					util.MustCreate(ctx, k8sClient, wl)
//
//					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
//
//					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
//					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
//					// Should be assigned to node-healthy
//					gomega.Expect(utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values).Should(gomega.ContainElement("node-healthy"))
//				})
//
//				ginkgo.By("creating another workload with DisallowUnhealthy policy, should remain pending", func() {
//					wl := utiltestingapi.MakeWorkload("wl-disallow-unhealthy-pending", ns.Name).
//						Queue(kueue.LocalQueueName(localQueue.Name)).
//						Request(corev1.ResourceCPU, "1").
//						Annotations(map[string]string{
//							ctrlconstants.NodeAvoidancePolicyAnnotation: ctrlconstants.NodeAvoidancePolicyDisallowUnhealthy,
//						}).
//						Obj()
//					util.MustCreate(ctx, k8sClient, wl)
//
//					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
//				})
//			})
//		})
//	*/
//})