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

package multikueue

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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util"
)

var defaultEnabledIntegrations sets.Set[string] = sets.New(
	"batch/job", "kubeflow.org/mpijob", "ray.io/rayjob", "ray.io/raycluster",
	"jobset.x-k8s.io/jobset", "kubeflow.org/paddlejob",
	"kubeflow.org/pytorchjob", "kubeflow.org/tfjob", "kubeflow.org/xgboostjob", "kubeflow.org/jaxjob",
	"pod", "workload.codeflare.dev/appwrapper")

var _ = ginkgo.Describe("Topology Aware Scheduling", ginkgo.Ordered, func() {
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
		managerNs = util.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultiKueueSecret1 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue1",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multiKueueAC)).Should(gomega.Succeed())

		ginkgo.By("wait for check active", func() {
			updatedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multiKueueAC)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		managerTopology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTopology)

		managerTasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(managerTopology.Name).Obj()
		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTasFlavor)

		worker1Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Topology)

		worker1TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(worker1Topology.Name).Obj()
		util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1TasFlavor)

		worker2Topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Topology)

		worker2TasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName(worker2Topology.Name).Obj()
		util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2TasFlavor)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerLq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq, true)

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)

		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac, true)

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerTasFlavor, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1TasFlavor, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2TasFlavor, true)

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerTopology, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Topology, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Topology, true)

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})

	ginkgo.When("Topology has a single level", func() {
		ginkgo.BeforeEach(func() {
			managerCq = utiltestingapi.MakeClusterQueue("mgr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(managerTasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

			managerLq = utiltestingapi.MakeLocalQueue("local-queue", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

			worker1Cq = utiltestingapi.MakeClusterQueue("wr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

			worker1Lq = utiltestingapi.MakeLocalQueue("local-queue", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

			worker2Cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker2TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

			worker2Lq = utiltestingapi.MakeLocalQueue("local-queue", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())

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
			util.CreateNodesWithStatus(worker1TestCluster.ctx, worker1TestCluster.client, worker1Nodes)
			util.CreateNodesWithStatus(worker2TestCluster.ctx, worker2TestCluster.client, worker2Nodes)
		})

		ginkgo.AfterEach(func() {
			for _, node := range worker1Nodes {
				util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &node, true)
			}
			for _, node := range worker2Nodes {
				util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, &node, true)
			}
		})

		ginkgo.It("should admit workload which fits in a required topology domain", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)

			job := testingjob.MakeJob("job", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("creating a job which requires block", func() {
				util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("verify the workload is created in manager cluster and has QuotaReserved", func() {
				managerWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).Should(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(managerWl.Status.Conditions, kueue.WorkloadQuotaReserved)).Should(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			var assignedWorkerCluster client.Client
			var assignedWorkerCtx context.Context
			var assignedClusterName string
			ginkgo.By("checking which worker cluster was assigned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())

					assignedClusterName = *managerWl.Status.ClusterName
					g.Expect(assignedClusterName).To(gomega.Or(gomega.Equal(workerCluster1.Name), gomega.Equal(workerCluster2.Name)))
					if assignedClusterName == workerCluster1.Name {
						assignedWorkerCluster = worker1TestCluster.client
						assignedWorkerCtx = worker1TestCluster.ctx
					} else {
						assignedWorkerCluster = worker2TestCluster.client
						assignedWorkerCtx = worker2TestCluster.ctx
					}

					g.Expect(assignedWorkerCluster.Get(assignedWorkerCtx, wlLookupKey, wl)).To(gomega.Succeed())
					g.Expect(wl.Spec.PodSets).Should(gomega.BeComparableTo([]kueue.PodSet{{
						Name:  kueue.DefaultPodSetName,
						Count: 1,
						TopologyRequest: &kueue.PodSetTopologyRequest{
							Required:      ptr.To(string(corev1.LabelHostname)),
							PodIndexLabel: ptr.To(batchv1.JobCompletionIndexAnnotation),
						},
					}}, cmpopts.IgnoreFields(kueue.PodSet{}, "Template")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the workload is admitted in the assigned worker cluster", func() {
				util.ExpectWorkloadsToBeAdmitted(assignedWorkerCtx, assignedWorkerCluster, wl)
			})

			ginkgo.By("verify TopologyAssignment for the workload in the assigned worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(assignedWorkerCluster.Get(assignedWorkerCtx, wlLookupKey, wl)).Should(gomega.Succeed())
					g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels:  []string{corev1.LabelHostname},
							Domains: []kueue.TopologyDomainAssignment{{Count: 1, Values: []string{"host-1"}}},
						},
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify DelayedTopologyRequest is marked Ready on manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(managerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
					g.Expect(managerWl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(ptr.To(kueue.DelayedTopologyRequestStateReady)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Prc)

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
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Prc)

			worker1Ac = utiltestingapi.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker1Prc.Name).
				Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac)

			worker2Ac = utiltestingapi.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker2Prc.Name).
				Obj()
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac)

			util.SetAdmissionCheckActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac, metav1.ConditionTrue)
			util.SetAdmissionCheckActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac, metav1.ConditionTrue)

			managerCq = utiltestingapi.MakeClusterQueue("mgr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(managerTasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

			managerLq = utiltestingapi.MakeLocalQueue("local-queue", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

			worker1Cq = utiltestingapi.MakeClusterQueue("wr-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(worker1Ac.Name)).
				Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

			worker1Lq = utiltestingapi.MakeLocalQueue("local-queue", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

			worker2Cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker2TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(worker2Ac.Name)).
				Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

			worker2Lq = utiltestingapi.MakeLocalQueue("local-queue", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &createdRequest, true)

			util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Prc, true)
			util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Prc, true)

			for _, node := range worker1Nodes {
				util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &node, true)
			}
			for _, node := range worker2Nodes {
				util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, &node, true)
			}
		})

		ginkgo.It("should admit workload when nodes are provisioned", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)

			job := testingjob.MakeJob("job", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				PodAnnotation(kueue.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("creating a job which requires block", func() {
				util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("verify the workload reserves the quota", func() {
				util.ExpectQuotaReservedWorkloadsTotalMetric(managerCq, "", 1)
				util.ExpectReservingActiveWorkloadsMetric(managerCq, 1)
			})

			ginkgo.By("provision the nodes on both workers", func() {
				worker1Nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				worker2Nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(worker1TestCluster.ctx, worker1TestCluster.client, worker1Nodes)
				util.CreateNodesWithStatus(worker2TestCluster.ctx, worker2TestCluster.client, worker2Nodes)
			})

			var assignedWorkerCluster client.Client
			var assignedWorkerCtx context.Context
			var assignedClusterName string
			var assignedAcName string
			ginkgo.By("checking which worker cluster was assigned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.ClusterName).NotTo(gomega.BeNil())

					assignedClusterName = *managerWl.Status.ClusterName
					g.Expect(assignedClusterName).To(gomega.Or(gomega.Equal(workerCluster1.Name), gomega.Equal(workerCluster2.Name)))
					if assignedClusterName == workerCluster1.Name {
						assignedWorkerCluster = worker1TestCluster.client
						assignedWorkerCtx = worker1TestCluster.ctx
						assignedAcName = worker1Ac.Name
					} else {
						assignedWorkerCluster = worker2TestCluster.client
						assignedWorkerCtx = worker2TestCluster.ctx
						assignedAcName = worker2Ac.Name
					}

					g.Expect(assignedWorkerCluster.Get(assignedWorkerCtx, wlLookupKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			provReqKey := types.NamespacedName{
				Namespace: wlLookupKey.Namespace,
				Name:      provisioning.ProvisioningRequestName(wlLookupKey.Name, kueue.AdmissionCheckReference(assignedAcName), 1),
			}

			ginkgo.By("set the ProvisioningRequest as Provisioned in the assigned worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(assignedWorkerCluster.Get(assignedWorkerCtx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(assignedWorkerCluster.Status().Update(assignedWorkerCtx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify the workload is admitted in the assigned worker cluster", func() {
				util.ExpectWorkloadsToBeAdmitted(assignedWorkerCtx, assignedWorkerCluster, wl)
			})

			ginkgo.By("verify TopologyAssignment for the workload in the assigned worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(assignedWorkerCluster.Get(assignedWorkerCtx, wlLookupKey, wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								corev1.LabelHostname,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"x1",
									},
								},
							},
						},
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify DelayedTopologyRequest is marked Ready on manager", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					managerWl := &kueue.Workload{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(managerWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
					g.Expect(managerWl.Status.Admission.PodSetAssignments[0].DelayedTopologyRequest).To(gomega.Equal(ptr.To(kueue.DelayedTopologyRequestStateReady)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
