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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	jobcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	jobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
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

	ginkgo.GinkgoWriter.Printf("Feature NodeAvoidanceScheduling enabled: %v\n", features.Enabled(features.NodeAvoidanceScheduling))

	cacheOptions := []schdcache.Option{}
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

	err = jobcontroller.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	jobReconciler, err := jobcontroller.NewReconciler(
		ctx,
		mgr.GetClient(),
		mgr.GetFieldIndexer(),
		mgr.GetEventRecorderFor("kueue-job-controller"),
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = jobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = jobcontroller.SetupWebhook(mgr, jobframework.WithQueues(queues), jobframework.WithCache(cCache))
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

	ginkgo.When("NodeAvoidanceScheduling feature is enabled", func() {
		var (
			nodes        []corev1.Node
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
				*testingnode.MakeNode("node-safe").
					Label("node-group", "tas").
					Label("kubernetes.io/hostname", "node-safe").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("node-avoid").
					Label("node-group", "tas").
					Label("avoid", "true").
					Label("kubernetes.io/hostname", "node-avoid").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			}
			ginkgo.GinkgoWriter.Printf("DEBUG: Creating nodes with labels: %v\n", nodes[0].Labels)
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)
			for _, node := range nodes {
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true) })
			}

			topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
			if topology.Annotations == nil {
				topology.Annotations = make(map[string]string)
			}
			topology.Annotations[kueue.NodeAvoidanceLabelAnnotation] = "avoid"
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

			localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
			ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true) })
		})

		ginkgo.It("should prefer safe nodes when policy is PreferNoSchedule", func() {
			job := jobtesting.MakeJob("job-prefer-safe", ns.Name).
				Queue("local-queue").
				Request(corev1.ResourceCPU, "1").
				SetAnnotation(kueue.NodeAvoidancePolicyAnnotation, kueue.NodeAvoidancePolicyPreferNoSchedule).
				Obj()
			ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true) })
			util.MustCreate(ctx, k8sClient, job)

			wl := &kueue.Workload{}
			wlName := jobcontroller.GetWorkloadNameForJob(job.Name, job.UID)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verify the workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			gomega.Expect(utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values).To(gomega.ContainElement("node-safe"))
		})

		ginkgo.It("should disallow avoided nodes when policy is NoSchedule", func() {
			ginkgo.By("tainting the safe node to make it unavailable", func() {
				node := &corev1.Node{}
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "node-safe"}, node)).To(gomega.Succeed())
				node.Spec.Unschedulable = true
				gomega.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "node-safe"}, node)).To(gomega.Succeed())
					g.Expect(node.Spec.Unschedulable).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("creating a job with NoSchedule policy, should remain pending", func() {
				job := jobtesting.MakeJob("job-disallow-avoid", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").
					SetAnnotation(kueue.NodeAvoidancePolicyAnnotation, kueue.NodeAvoidancePolicyNoSchedule).
					Obj()
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true) })
				util.MustCreate(ctx, k8sClient, job)

				wl := &kueue.Workload{}
				wlName := jobcontroller.GetWorkloadNameForJob(job.Name, job.UID)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					if workload.IsAdmitted(wl) {
						ginkgo.GinkgoWriter.Printf("Workload %s unexpectedly admitted. Assignments: %v\n", wl.Name, wl.Status.Admission.PodSetAssignments)
					}
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("creating another job with NoSchedule policy, should remain pending", func() {
				job := jobtesting.MakeJob("job-disallow-avoid-pending", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").
					SetAnnotation(kueue.NodeAvoidancePolicyAnnotation, kueue.NodeAvoidancePolicyNoSchedule).
					Obj()
				util.MustCreate(ctx, k8sClient, job)

				wl := &kueue.Workload{}
				wlName := jobcontroller.GetWorkloadNameForJob(job.Name, job.UID)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			})

			ginkgo.By("creating a job with WorkloadPriorityClass having NoSchedule policy, should remain pending", func() {
				wpc := &kueue.WorkloadPriorityClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "wpc-avoid",
						Annotations: map[string]string{
							kueue.NodeAvoidancePolicyAnnotation: kueue.NodeAvoidancePolicyNoSchedule,
						},
					},
					Value: 100,
				}
				gomega.Expect(k8sClient.Create(ctx, wpc)).To(gomega.Succeed())
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, wpc, true) })

				job := jobtesting.MakeJob("job-wpc-avoid", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").
					WorkloadPriorityClass("wpc-avoid").
					Obj()
				util.MustCreate(ctx, k8sClient, job)
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true) })

				wl := &kueue.Workload{}
				wlName := jobcontroller.GetWorkloadNameForJob(job.Name, job.UID)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(kueue.NodeAvoidancePolicyAnnotation, kueue.NodeAvoidancePolicyNoSchedule))
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("creating a job with WorkloadPriorityClass having NoSchedule policy and Job having PreferNoSchedule, should admit", func() {
				wpc := &kueue.WorkloadPriorityClass{
					ObjectMeta: metav1.ObjectMeta{
						Name: "wpc-avoid-2",
						Annotations: map[string]string{
							kueue.NodeAvoidancePolicyAnnotation: kueue.NodeAvoidancePolicyNoSchedule,
						},
					},
					Value: 100,
				}
				gomega.Expect(k8sClient.Create(ctx, wpc)).To(gomega.Succeed())
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, wpc, true) })

				job := jobtesting.MakeJob("job-wpc-override", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").
					WorkloadPriorityClass("wpc-avoid-2").
					SetAnnotation(kueue.NodeAvoidancePolicyAnnotation, kueue.NodeAvoidancePolicyPreferNoSchedule).
					Obj()
				util.MustCreate(ctx, k8sClient, job)
				ginkgo.DeferCleanup(func() { util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true) })

				wl := &kueue.Workload{}
				wlName := jobcontroller.GetWorkloadNameForJob(job.Name, job.UID)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wlName, Namespace: ns.Name}, wl)).To(gomega.Succeed())
					g.Expect(wl.Annotations).To(gomega.HaveKeyWithValue(kueue.NodeAvoidancePolicyAnnotation, kueue.NodeAvoidancePolicyPreferNoSchedule))
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
