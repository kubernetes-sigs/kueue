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
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var defaultEnabledIntegrations sets.Set[string] = sets.New(
	"batch/job", "kubeflow.org/mpijob", "ray.io/rayjob", "ray.io/raycluster",
	"jobset.x-k8s.io/jobset", "kubeflow.org/paddlejob",
	"kubeflow.org/pytorchjob", "kubeflow.org/tfjob", "kubeflow.org/xgboostjob", "kubeflow.org/jaxjob",
	"pod", "workload.codeflare.dev/appwrapper")

var _ = ginkgo.Describe("Topology Aware Scheduling", ginkgo.Label("area:multikueue", "feature:multikueue", "feature:tas"), ginkgo.Ordered, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultiKueueSecret1 *corev1.Secret
		managerMultiKueueSecret2 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		workerCluster2           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig
		multiKueueAC             *kueue.AdmissionCheck

		managerTopology *kueue.Topology
		worker1Topology *kueue.Topology
		worker2Topology *kueue.Topology

		managerTasFlavor *kueue.ResourceFlavor
		worker1TasFlavor *kueue.ResourceFlavor
		worker2TasFlavor *kueue.ResourceFlavor

		managerCq *kueue.ClusterQueue
		worker1Cq *kueue.ClusterQueue
		worker2Cq *kueue.ClusterQueue

		managerLq *kueue.LocalQueue
		worker1Lq *kueue.LocalQueue
		worker2Lq *kueue.LocalQueue

		worker1Ac *kueue.AdmissionCheck
		worker2Ac *kueue.AdmissionCheck

		worker1Nodes []corev1.Node
		worker2Nodes []corev1.Node
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		managerNs = behavioral.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-")
		worker1Ns = behavioral.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = behavioral.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue1", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue2", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		behavioral.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)

		managerTopology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTopology)

		managerTasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(managerTopology.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTasFlavor)

		worker1Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		behavioral.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Topology)

		worker1TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(worker1Topology.Name).Obj()
		behavioral.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1TasFlavor)

		worker2Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Topology)

		worker2TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(worker2Topology.Name).Obj()
		behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2TasFlavor)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())

		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerLq, true)
		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq, true)

		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)

		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac, true)

		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerTasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1TasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2TasFlavor, true)

		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerTopology, true)
		behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Topology, true)
		behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Topology, true)

		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.When("Topology has a single level", func() {
		ginkgo.BeforeEach(func() {
			managerCq = utiltestingapi.MakeClusterQueue("mgr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(managerTasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

			managerLq = utiltestingapi.MakeLocalQueue("local-queue", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

			worker1Cq = utiltestingapi.MakeClusterQueue("wr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

			worker1Lq = utiltestingapi.MakeLocalQueue("local-queue", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

			worker2Cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker2TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

			worker2Lq = utiltestingapi.MakeLocalQueue("local-queue", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)

			worker1Nodes = []corev1.Node{
				*testingnode.MakeNode("single-node").
					Label(corev1.LabelHostname, "host-1").
					Label("node-group", "tas").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			}
			worker2Nodes = []corev1.Node{
				*testingnode.MakeNode("single-node").
					Label(corev1.LabelHostname, "host-1").
					Label("node-group", "tas").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			}
			behavioral.CreateNodesWithStatus(worker1TestCluster.ctx, worker1TestCluster.client, worker1Nodes)
			behavioral.CreateNodesWithStatus(worker2TestCluster.ctx, worker2TestCluster.client, worker2Nodes)
		})

		ginkgo.AfterEach(func() {
			for _, node := range worker1Nodes {
				behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &node, true)
			}
			for _, node := range worker2Nodes {
				behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, &node, true)
			}
		})

		ginkgo.It("should admit workload which fits in a required topology domain", func() {
			job := testingjob.MakeJob("job", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("creating a job which requires block", func() {
				behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("verify the workload is created in manager cluster and has QuotaReserved", func() {
				managerWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).Should(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(managerWl.Status.Conditions, kueue.WorkloadQuotaReserved)).Should(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			var selectedWorker behavioral.ClusterInfo
			ginkgo.By("checking which worker cluster was assigned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					selectedWorker = behavioral.GetClientForSelectedWorkerCluster(
						g,
						managerWl,
						behavioral.DefaultClusterInfosForTests(
							worker1TestCluster.ctx,
							worker1TestCluster.client,
							worker2TestCluster.ctx,
							worker2TestCluster.client,
						)...,
					)

					g.Expect(selectedWorker.Client.Get(selectedWorker.Ctx, wlLookupKey, wl)).To(gomega.Succeed())
					g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:      new(string(corev1.LabelHostname)),
							PodIndexLabel: new(batchv1.JobCompletionIndexAnnotation),
						},
					}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the workload is admitted in the assigned worker cluster", func() {
				behavioral.ExpectWorkloadsToBeAdmitted(selectedWorker.Ctx, selectedWorker.Client, wl)
			})

			ginkgo.By("verify TopologyAssignment for the workload in the assigned worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(selectedWorker.Client.Get(selectedWorker.Ctx, wlLookupKey, wl)).Should(gomega.Succeed())
					g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						tas.V1Beta2From(&tas.TopologyAssignment{
							Levels:  []string{corev1.LabelHostname},
							Domains: []tas.TopologyDomainAssignment{{Count: 1, Values: []string{"host-1"}}},
						}),
					))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify DelayedTopologyRequest is marked Ready on manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(managerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
					g.Expect(managerWl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(new(kueue.DelayedTopologyRequestStateReady)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("ProvisioningRequest is used", func() {
		var (
			worker1Prc *kueue.ProvisioningRequestConfig
			worker2Prc *kueue.ProvisioningRequestConfig

			createdRequest autoscaling.ProvisioningRequest
		)

		ginkgo.BeforeEach(func() {
			worker1Prc = utiltestingapi.MakeProvisioningRequestConfig("prov-config").
				ProvisioningClass("provisioning-class").
				RetryLimit(1).
				BaseBackoff(1).
				PodSetUpdate(kueue.ProvisioningRequestPodSetUpdates{
					NodeSelector: []kueue.ProvisioningRequestPodSetUpdatesNodeSelector{{
						Key:                              "dedicated-selector-key",
						ValueFromProvisioningClassDetail: "dedicated-selector-detail",
					}},
				}).
				Obj()
			behavioral.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Prc)

			worker2Prc = utiltestingapi.MakeProvisioningRequestConfig("prov-config").
				ProvisioningClass("provisioning-class").
				RetryLimit(1).
				BaseBackoff(1).
				PodSetUpdate(kueue.ProvisioningRequestPodSetUpdates{
					NodeSelector: []kueue.ProvisioningRequestPodSetUpdatesNodeSelector{{
						Key:                              "dedicated-selector-key",
						ValueFromProvisioningClassDetail: "dedicated-selector-detail",
					}},
				}).
				Obj()
			behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Prc)

			worker1Ac = utiltestingapi.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", worker1Prc.Name).
				Obj()
			behavioral.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac)

			worker2Ac = utiltestingapi.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.SchemeGroupVersion.Group, "ProvisioningRequestConfig", worker2Prc.Name).
				Obj()
			behavioral.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac)

			behavioral.SetAdmissionCheckActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac, metav1.ConditionTrue)
			behavioral.SetAdmissionCheckActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac, metav1.ConditionTrue)

			managerCq = utiltestingapi.MakeClusterQueue("mgr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(managerTasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

			managerLq = utiltestingapi.MakeLocalQueue("local-queue", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

			worker1Cq = utiltestingapi.MakeClusterQueue("wr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(worker1Ac.Name)).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

			worker1Lq = utiltestingapi.MakeLocalQueue("local-queue", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

			worker2Cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker2TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(worker2Ac.Name)).
				Obj()
			behavioral.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

			worker2Lq = utiltestingapi.MakeLocalQueue("local-queue", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &createdRequest, true)

			behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Prc, true)
			behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Prc, true)

			for _, node := range worker1Nodes {
				behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &node, true)
			}
			for _, node := range worker2Nodes {
				behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, &node, true)
			}
		})
	})
})
