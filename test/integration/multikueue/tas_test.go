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
	apitypes "k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.FDescribe("Topology Aware Scheduling", ginkgo.Ordered, func() {
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
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue

		managerTopology *kueuealpha.Topology
		worker1Topology *kueuealpha.Topology
		worker2Topology *kueuealpha.Topology

		managerTasFlavor *kueue.ResourceFlavor
		worker1TasFlavor *kueue.ResourceFlavor
		worker2TasFlavor *kueue.ResourceFlavor
	)

	ginkgo.BeforeAll(func() {
		_ = features.SetEnable(features.MultiKueueBatchJobWithManagedBy, true)
		_ = features.SetEnable(features.TopologyAwareScheduling, true)
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations)
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

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltesting.MakeAdmissionCheck("ac1").
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

		// managerTopology = utiltesting.MakeDefaultOneLevelTopology("default")
		// util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTopology)
		// managerTasFlavor = testing.MakeResourceFlavor("tas-flavor").
		// 	NodeLabel("node-group", "tas").
		// 	TopologyName(managerTopology.Name).Obj()
		// util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTasFlavor)

		// worker1Topology = utiltesting.MakeDefaultOneLevelTopology("default")
		// util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Topology)
		// worker1TasFlavor = testing.MakeResourceFlavor("tas-flavor").
		// 	NodeLabel("node-group", "tas").
		// 	TopologyName(worker1Topology.Name).Obj()
		// util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1TasFlavor)

		// worker2Topology = utiltesting.MakeDefaultOneLevelTopology("default")
		// util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Topology)
		// worker2TasFlavor = testing.MakeResourceFlavor("tas-flavor").
		// 	NodeLabel("node-group", "tas").
		// 	TopologyName(worker2Topology.Name).Obj()
		// util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2TasFlavor)

		// managerCq = utiltesting.MakeClusterQueue("cluster-queue").
		// 	ResourceGroup(
		// 		*testing.MakeFlavorQuotas(managerTasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
		// 	).
		// 	AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
		// 	Obj()
		// gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())
		// util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		// managerLq = utiltesting.MakeLocalQueue("local-queue", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		// gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

		// worker1Cq = utiltesting.MakeClusterQueue("cluster-queue").
		// 	ResourceGroup(
		// 		*testing.MakeFlavorQuotas(worker1TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
		// 	).
		// 	Obj()
		// gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
		// util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

		// worker1Lq = utiltesting.MakeLocalQueue("local-queue", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		// gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

		// worker2Cq = utiltesting.MakeClusterQueue("cluster-queue").
		// 	ResourceGroup(
		// 		*testing.MakeFlavorQuotas(worker2TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
		// 	).
		// 	Obj()
		// gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
		// util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

		// worker2Lq = utiltesting.MakeLocalQueue("local-queue", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		// gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
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

	ginkgo.When("Topology has a single level (kubernetes.io/hostname)", func() {
		var (
			nodes []corev1.Node
		)

		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
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
			util.CreateNodesWithStatus(managerTestCluster.ctx, managerTestCluster.client, nodes)
			util.CreateNodesWithStatus(worker1TestCluster.ctx, worker1TestCluster.client, nodes)
			util.CreateNodesWithStatus(worker2TestCluster.ctx, worker2TestCluster.client, nodes)
		})

		ginkgo.AfterEach(func() {
			for _, node := range nodes {
				util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, &node, true)
				util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &node, true)
				util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, &node, true)
			}
		})

		ginkgo.It("should admit workload which fits in a required topology domain", func() {
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("creating a job which requires block", func() {
				util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("verify the workload is created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, wl)).Should(gomega.Succeed())
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

			ginkgo.By("verify the workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(worker1TestCluster.ctx, worker1TestCluster.client, wl)
				util.ExpectReservingActiveWorkloadsMetric(worker1Cq, 1)
			})

			ginkgo.By("verify admission for the workload", func() {
				wl := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, wl)).Should(gomega.Succeed())
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
		})

	})

	ginkgo.FWhen("ProvisioningRequest is used", func() {
		var (
			nodes          []corev1.Node
			managerAc      *kueue.AdmissionCheck
			worker1Ac      *kueue.AdmissionCheck
			worker2Ac      *kueue.AdmissionCheck
			managerPrc     *kueue.ProvisioningRequestConfig
			worker1Prc     *kueue.ProvisioningRequestConfig
			worker2Prc     *kueue.ProvisioningRequestConfig
			createdRequest autoscaling.ProvisioningRequest
		)

		ginkgo.BeforeEach(func() {

			managerTopology = utiltesting.MakeDefaultOneLevelTopology("default")
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTopology)
			managerTasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(managerTopology.Name).Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerTasFlavor)

			worker1Topology = utiltesting.MakeDefaultOneLevelTopology("default")
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Topology)
			worker1TasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(worker1Topology.Name).Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1TasFlavor)

			worker2Topology = utiltesting.MakeDefaultOneLevelTopology("default")
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Topology)
			worker2TasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName(worker2Topology.Name).Obj()
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2TasFlavor)

			managerPrc = testing.MakeProvisioningRequestConfig("prov-config").
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
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerPrc)
			worker1Prc = testing.MakeProvisioningRequestConfig("prov-config").
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
			worker2Prc = testing.MakeProvisioningRequestConfig("prov-config").
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

			managerAc = testing.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker1Prc.Name).
				Obj()
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managerAc)

			worker1Ac = testing.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker1Prc.Name).
				Obj()
			util.MustCreate(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac)

			worker2Ac = testing.MakeAdmissionCheck("provisioning").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", worker1Prc.Name).
				Obj()
			util.MustCreate(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac)

			util.SetAdmissionCheckActive(managerTestCluster.ctx, managerTestCluster.client, managerAc, metav1.ConditionTrue)
			util.SetAdmissionCheckActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac, metav1.ConditionTrue)
			util.SetAdmissionCheckActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac, metav1.ConditionTrue)

			managerCq = utiltesting.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(managerTasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
				AdmissionChecks(kueue.AdmissionCheckReference(managerAc.Name)).
				Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

			managerLq = utiltesting.MakeLocalQueue("local-queue", managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

			worker1Cq = utiltesting.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(worker1TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(worker1Ac.Name)).
				Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)

			worker1Lq = utiltesting.MakeLocalQueue("local-queue", worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

			worker2Cq = utiltesting.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(worker2TasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(worker2Ac.Name)).
				Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)

			worker2Lq = utiltesting.MakeLocalQueue("local-queue", worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
			gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
		})

		// ginkgo.AfterEach(func() {
		// 	//util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerAc, true)
		// 	util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ac, true)
		// 	util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ac, true)
		// 	util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerPrc, true)
		// 	util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Prc, true)
		// 	util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Prc, true)
		// 	util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &createdRequest, true)
		// 	for _, node := range nodes {
		// 		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, &node, true)
		// 		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, &node, true)
		// 		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, &node, true)
		// 	}
		// })

		ginkgo.It("should admit workload when nodes are provisioned; manager restart", func() {
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("creating a job which requires block", func() {
				util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			})

			wlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("verify the workload reserves the quota", func() {
				util.ExpectQuotaReservedWorkloadsTotalMetric(managerCq, 1)
				util.ExpectReservingActiveWorkloadsMetric(managerCq, 1)
			})

			provReqKey := apitypes.NamespacedName{
				Namespace: wlKey.Namespace,
				Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(worker1Ac.Name), 1),
			}

			ginkgo.By("await for the ProvisioningRequest to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("provision the nodes", func() {
				nodes = []corev1.Node{
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
				util.CreateNodesWithStatus(managerTestCluster.ctx, managerTestCluster.client, nodes)
				util.CreateNodesWithStatus(worker1TestCluster.ctx, worker1TestCluster.client, nodes)
				util.CreateNodesWithStatus(worker2TestCluster.ctx, worker2TestCluster.client, nodes)
			})

			ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
						Type:   autoscaling.Provisioned,
						Status: metav1.ConditionTrue,
						Reason: autoscaling.Provisioned,
					})
					g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, &createdRequest)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			wl := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("await for the check to be ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, wl)).To(gomega.Succeed())
					state := workload.FindAdmissionCheck(wl.Status.AdmissionChecks, kueue.AdmissionCheckReference(managerAc.Name))
					g.Expect(state).NotTo(gomega.BeNil())
					g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
				}, util.LongTimeout, time.Millisecond).Should(gomega.Succeed())
			})

			// ginkgo.By("restart Kueue manager", func() {
			// 	managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
			// 	managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, managerSetup)
			// })

			ginkgo.By("verify admission for the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
					g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
					g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
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

			ginkgo.By("verify the workload is admitted", func() {
				util.ExpectReservingActiveWorkloadsMetric(managerCq, 1)
				util.ExpectPendingWorkloadsMetric(managerCq, 0, 0)
				util.ExpectWorkloadsToBeAdmitted(managerTestCluster.ctx, managerTestCluster.client, wl)
			})
		})

	})
})
