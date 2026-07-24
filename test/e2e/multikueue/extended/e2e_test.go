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

package extended

import (
	"fmt"
	"strconv"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftrainer "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadaw "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadleaderworkerset "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	workloadrayservice "sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	workloadtrainjob "sigs.k8s.io/kueue/pkg/controller/jobs/trainjob"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	testingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	// extra virtual resource that allows to directly to a specific worker cluster, without relying on real resources.
	extraResourceGPUHighCost = "example.com/gpu-h100"
	extraResourceGPULowCost  = "example.com/gpu-v100s"
)

type kubernetesClientsMap map[string]struct {
	client     client.Client
	restClient *rest.RESTClient
	cfg        *rest.Config
}

var _ = ginkgo.Describe("MultiKueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		workerCluster1   *kueue.MultiKueueCluster
		workerCluster2   *kueue.MultiKueueCluster
		multiKueueConfig *kueue.MultiKueueConfig
		multiKueueAc     *kueue.AdmissionCheck
		managerHighWPC   *kueue.WorkloadPriorityClass
		managerLowWPC    *kueue.WorkloadPriorityClass
		managerFlavor    *kueue.ResourceFlavor
		managerCq        *kueue.ClusterQueue
		managerLq        *kueue.LocalQueue

		worker1HighWPC *kueue.WorkloadPriorityClass
		worker1LowWPC  *kueue.WorkloadPriorityClass
		worker1Flavor  *kueue.ResourceFlavor
		worker1Cq      *kueue.ClusterQueue
		worker1Lq      *kueue.LocalQueue

		worker2HighWPC *kueue.WorkloadPriorityClass
		worker2LowWPC  *kueue.WorkloadPriorityClass
		worker2Flavor  *kueue.ResourceFlavor
		worker2Cq      *kueue.ClusterQueue
		worker2Lq      *kueue.LocalQueue

		kubernetesClients kubernetesClientsMap
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker1-").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueClusterWithGeneratedName("worker2-").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfigWithGeneratedName("multikueueconfig-").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("").
			GeneratedName("ac1-").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerHighWPC = utiltestingapi.MakeWorkloadPriorityClass("").
			GeneratedName("high-workload-").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerHighWPC)

		managerLowWPC = utiltestingapi.MakeWorkloadPriorityClass("").
			GeneratedName("low-workload-").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLowWPC)

		managerFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("default-").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("").
			GeneratedName("q1-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "4G").
					Resource(corev1.ResourceEphemeralStorage, "100G").
					Resource(extraResourceGPUHighCost, "2").
					Resource(extraResourceGPULowCost, "2").
					Obj(),
			).
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sManagerClient, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sManagerClient, managerLq)

		worker1HighWPC = utiltestingapi.MakeWorkloadPriorityClass(managerHighWPC.Name).PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1HighWPC)

		worker1LowWPC = utiltestingapi.MakeWorkloadPriorityClass(managerLowWPC.Name).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1LowWPC)

		worker1Flavor = utiltestingapi.MakeResourceFlavor(managerFlavor.Name).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue(managerCq.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Resource(corev1.ResourceEphemeralStorage, "15G").
					Resource(extraResourceGPUHighCost, "2").
					Resource(extraResourceGPULowCost, "1").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2HighWPC = utiltestingapi.MakeWorkloadPriorityClass(managerHighWPC.Name).PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2HighWPC)

		worker2LowWPC = utiltestingapi.MakeWorkloadPriorityClass(managerLowWPC.Name).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2LowWPC)

		worker2Flavor = utiltestingapi.MakeResourceFlavor(managerFlavor.Name).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue(managerCq.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1200m").
					Resource(corev1.ResourceMemory, "4G").
					Resource(corev1.ResourceEphemeralStorage, "5G").
					Resource(extraResourceGPUHighCost, "1").
					Resource(extraResourceGPULowCost, "2").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)

		kubernetesClients = kubernetesClientsMap{
			workerCluster1.Name: {
				client:     k8sWorker1Client,
				restClient: worker1RestClient,
				cfg:        worker1Cfg,
			},
			workerCluster2.Name: {
				client:     k8sWorker2Client,
				restClient: worker2RestClient,
				cfg:        worker2Cfg,
			},
		}
	})

	ginkgo.AfterEach(func() {
		// Clean up resources created by the RayService test on all clusters.
		rayServiceConfigMap := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "rayservice-hello", Namespace: managerNs.Name}}
		gomega.Expect(client.IgnoreNotFound(k8sManagerClient.Delete(ctx, rayServiceConfigMap))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker1Client.Delete(ctx, rayServiceConfigMap.DeepCopy()))).To(gomega.Succeed())
		gomega.Expect(client.IgnoreNotFound(k8sWorker2Client.Delete(ctx, rayServiceConfigMap.DeepCopy()))).To(gomega.Succeed())

		// Use the CRD-tolerant helper: shards without KubeRay have no RayService CRD installed.
		gomega.Expect(util.DeleteAllRayServicesInNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteAllRayServicesInNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteAllRayServicesInNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1HighWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker1Client, worker1LowWPC, true)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2HighWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sWorker2Client, worker2LowWPC, true)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerHighWPC, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerLowWPC, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.MediumTimeout)

		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
	})

	ginkgo.When("Creating a multikueue integration workload", func() {
		ginkgo.It("Should sync a LeaderWorkerSet and run replicas on worker cluster", ginkgo.Label("feature:leaderworkerset"), func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(2).
				Size(2).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Queue(managerLq.Name).
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the leaderworkerset", func() {
				util.MustCreate(ctx, k8sManagerClient, lws)
			})

			createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())

			wlLookupKey0 := types.NamespacedName{
				Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "0"),
				Namespace: managerNs.Name,
			}
			wlLookupKey1 := types.NamespacedName{
				Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, "1"),
				Namespace: managerNs.Name,
			}

			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey0, multiKueueAc.Name)
			workerClient := kubernetesClients[admittedWorkerName].client

			ginkgo.By("Verifying both workloads are admitted on the same worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl0 := &kueue.Workload{}
					g.Expect(workerClient.Get(ctx, wlLookupKey0, wl0)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl0)).To(gomega.BeTrue())
					wl1 := &kueue.Workload{}
					g.Expect(workerClient.Get(ctx, wlLookupKey1, wl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl1)).To(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for LWS to be synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for all replicas to be ready on worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
					g.Expect(workerLWS.Status.ReadyReplicas).To(gomega.Equal(int32(2)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying pods on management cluster remain gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sManagerClient.List(ctx, pods, client.InNamespace(managerNs.Name), client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					})).To(gomega.Succeed())
					g.Expect(pods.Items).ToNot(gomega.BeEmpty())
					for _, pod := range pods.Items {
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).To(gomega.BeTrue())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the leaderworkerset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, lws, false, util.MediumTimeout)
			})

			ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(workerClient.Get(ctx, wlLookupKey0, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					g.Expect(workerClient.Get(ctx, wlLookupKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should dispatch all LeaderWorkerSet workloads to the same worker", ginkgo.Label("feature:leaderworkerset"), func() {
			const lwsReplicas = 3
			lws := testingleaderworkerset.MakeLeaderWorkerSet("leaderworkerset", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(lwsReplicas).
				Size(2).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M").
				Queue(managerLq.Name).
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the leaderworkerset", func() {
				util.MustCreate(ctx, k8sManagerClient, lws)
			})

			createdLWS := &leaderworkersetv1.LeaderWorkerSet{}
			gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLWS)).To(gomega.Succeed())

			wlKeys := make([]types.NamespacedName, lwsReplicas)
			for i := range lwsReplicas {
				wlKeys[i] = types.NamespacedName{
					Name:      workloadleaderworkerset.GetWorkloadName(createdLWS.UID, createdLWS.Name, strconv.Itoa(i)),
					Namespace: managerNs.Name,
				}
			}

			ginkgo.By("Waiting for workloads to be created on manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, key := range wlKeys {
						wl := &kueue.Workload{}
						g.Expect(k8sManagerClient.Get(ctx, key, wl)).To(gomega.Succeed())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlKeys[0], multiKueueAc.Name)
			workerClient := kubernetesClients[admittedWorkerName].client

			ginkgo.By("Verifying primary workload is admitted on worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					wl := &kueue.Workload{}
					g.Expect(workerClient.Get(ctx, wlKeys[0], wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying LWS is synced to worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					workerLWS := &leaderworkersetv1.LeaderWorkerSet{}
					g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(lws), workerLWS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying follower workloads are dispatched to the same worker cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					primaryWl := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlKeys[0], primaryWl)).To(gomega.Succeed())
					g.Expect(primaryWl.Status.ClusterName).ToNot(gomega.BeNil())
					for _, key := range wlKeys[1:] {
						wl := &kueue.Workload{}
						g.Expect(k8sManagerClient.Get(ctx, key, wl)).To(gomega.Succeed())
						g.Expect(wl.Status.ClusterName).ToNot(gomega.BeNil())
						g.Expect(*wl.Status.ClusterName).To(gomega.Equal(*primaryWl.Status.ClusterName))
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deleting the leaderworkerset", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sManagerClient, lws, true)
				util.ExpectObjectToBeDeletedWithTimeout(ctx, workerClient, lws, false, util.MediumTimeout)
			})

			ginkgo.By("Checking that all workloads are deleted from manager and worker clusters", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					for _, key := range wlKeys {
						g.Expect(k8sManagerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
						g.Expect(workerClient.Get(ctx, key, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should run a jobSet on worker if admitted", ginkgo.Label("feature:jobset"), func() {
			jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
				Queue(managerLq.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    2,
						Parallelism: 2,
						Completions: 2,
						Image:       util.GetAgnHostImage(),
						// Give it the time to be observed Active in the live status update step.
						Args: util.BehaviorWaitForDeletion,
					},
				).
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "100m").
				RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "100M").
				TerminationGracePeriod(1).
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				util.MustCreate(ctx, k8sManagerClient, jobSet)
			})

			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			admittedWorker := kubernetesClients[admittedWorkerName]

			ginkgo.By("Waiting for the jobSet to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobset := &jobset.JobSet{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobset)).To(gomega.Succeed())

					g.Expect(createdJobset.Status.ReplicatedJobsStatus).To(gomega.BeComparableTo([]jobset.ReplicatedJobStatus{
						{
							Name:   "replicated-job-1",
							Ready:  2,
							Active: 2,
						},
					}, cmpopts.IgnoreFields(jobset.ReplicatedJobStatus{}, "Succeeded", "Failed")))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the jobset pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("jobset.sigs.k8s.io/jobset-name=%s", jobSet.Name))
				util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, jobSet.Namespace, 4, 0, listOpts)
			})

			ginkgo.By("Waiting for the jobSet to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())

					g.Expect(apimeta.FindStatusCondition(createdLeaderWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "jobset completed successfully",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the jobSet is completed", func() {
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdLeaderWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, jobSet, k8sWorker1Client, k8sWorker2Client)

				createdJobSet := &jobset.JobSet{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
				gomega.Expect(ptr.Deref(createdJobSet.Spec.Suspend, true)).To(gomega.BeFalse())
				gomega.Expect(createdJobSet.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					metav1.Condition{
						Type:    string(jobset.JobSetCompleted),
						Status:  metav1.ConditionTrue,
						Reason:  "AllJobsCompleted",
						Message: "jobset completed successfully",
					},
					util.IgnoreConditionTimestampsAndObservedGeneration)))
			})
		})

		ginkgo.It("Should run an appwrapper containing a job on worker if admitted", ginkgo.Label("feature:appwrapper"), func() {
			jobName := "job-1"
			aw := testingaw.MakeAppWrapper("aw", managerNs.Name).
				Queue(managerLq.Name).
				Component(testingaw.Component{
					Template: testingjob.MakeJob(jobName, managerNs.Name).
						SetTypeMeta().
						Suspend(false).
						Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion). // Give it the time to be observed Active in the live status update step.
						Parallelism(2).
						RequestAndLimit(corev1.ResourceCPU, "100m").
						RequestAndLimit(corev1.ResourceMemory, "100M").
						TerminationGracePeriod(1).
						SetTypeMeta().Obj(),
				}).
				Obj()

			ginkgo.By("Creating the appwrapper", func() {
				util.MustCreate(ctx, k8sManagerClient, aw)
			})

			wlLookupKey := types.NamespacedName{Name: workloadaw.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: managerNs.Name}

			admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			admittedWorker := kubernetesClients[admittedWorkerName]

			ginkgo.By("Waiting for the appwrapper to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdAppWrapper := &awv1beta2.AppWrapper{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperRunning))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the wrapped job's pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", jobName))
				util.WaitForActivePodsAndTerminate(ctx, admittedWorker.client, admittedWorker.restClient, admittedWorker.cfg, aw.Namespace, 2, 0, listOpts)
			})

			ginkgo.By("Waiting for the appwrapper to finish", func() {
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the appwrapper is completed", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, aw, k8sWorker1Client, k8sWorker2Client)

				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				gomega.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
				gomega.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
			})
		})

		ginkgo.It("Should run a kubeflow PyTorchJob on worker if admitted", ginkgo.Label("feature:pytorchjob"), func() {
			pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", managerNs.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				Queue(managerLq.Name).
				PyTorchReplicaSpecs(
					testingpytorchjob.PyTorchReplicaSpecRequirement{
						ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
						ReplicaCount:  1,
						RestartPolicy: "Never",
						Image:         util.GetAgnHostImage(),
						Args:          util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceCPU, "100m").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeMaster, corev1.ResourceMemory, "100M").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceCPU, "100m").
				RequestAndLimit(kftraining.PyTorchJobReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Obj()

			ginkgo.By("Creating the PyTorchJob", func() {
				util.MustCreate(ctx, k8sManagerClient, pyTorchJob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}

			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info("PyTorchJob %s is admitted in worker cluster %s", pyTorchJob.Name, admittedWorker)

			ginkgo.By("Waiting for the PyTorchJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdPyTorchJob := &kftraining.PyTorchJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(pyTorchJob), createdPyTorchJob)).To(gomega.Succeed())
					g.Expect(createdPyTorchJob.Status.ReplicaStatuses[kftraining.PyTorchJobReplicaTypeMaster]).To(gomega.BeComparableTo(
						&kftraining.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
							Selector: fmt.Sprintf(
								"training.kubeflow.org/job-name=%s,training.kubeflow.org/operator-name=pytorchjob-controller,training.kubeflow.org/replica-type=master",
								createdPyTorchJob.Name,
							),
						},
					))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the PyTorchJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, pyTorchJob, k8sWorker1Client, k8sWorker2Client)
			})
		})

		ginkgo.It("Should run a MPIJob on worker if admitted", ginkgo.Label("feature:mpijob"), func() {
			mpijob := testingmpijob.MakeMPIJob("mpijob1", managerNs.Name).
				Queue(managerLq.Name).
				ManagedBy(kueue.MultiKueueControllerName).
				MPIJobReplicaSpecs(
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
						Image:         util.GetAgnHostImage(),
						Args:          util.BehaviorExitFast,
					},
					testingmpijob.MPIJobReplicaSpecRequirement{
						ReplicaType:   kfmpi.MPIReplicaTypeWorker,
						ReplicaCount:  1,
						RestartPolicy: "OnFailure",
						Image:         util.GetAgnHostImage(),
						Args:          util.BehaviorExitFast,
					},
				).
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceCPU, "100m").
				RequestAndLimit(kfmpi.MPIReplicaTypeLauncher, corev1.ResourceMemory, "100M").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceCPU, "100m").
				RequestAndLimit(kfmpi.MPIReplicaTypeWorker, corev1.ResourceMemory, "100M").
				Obj()

			ginkgo.By("Creating the MPIJob", func() {
				util.MustCreate(ctx, k8sManagerClient, mpijob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}

			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info("MPIJob %s is admitted in worker cluster %s", mpijob.Name, admittedWorker)

			ginkgo.By("Waiting for the MPIJob to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdMPIJob := &kfmpi.MPIJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(mpijob), createdMPIJob)).To(gomega.Succeed())
					g.Expect(createdMPIJob.Status.ReplicaStatuses[kfmpi.MPIReplicaTypeLauncher]).To(gomega.BeComparableTo(
						&kfmpi.ReplicaStatus{
							Active:    0,
							Succeeded: 1,
						},
					))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the MPIJob is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, mpijob, k8sWorker1Client, k8sWorker2Client)
			})
		})

		ginkgo.It("Should run a TrainJob on worker if admitted", ginkgo.Label("feature:trainjob"), func() {
			trainjob := testingtrainjob.MakeTrainJob("trainjob-test", managerNs.Name).
				RuntimeRefName("torch-distributed").
				Queue(managerLq.Name).
				RequestAndLimit(corev1.ResourceCPU, "100m", "100m").
				RequestAndLimit(corev1.ResourceMemory, "100M", "100M").
				// Even if we override the image coming from the TrainingRuntime, we still need to set the command and args
				TrainerImage(util.GetAgnHostImage(), []string{"/agnhost"}, util.BehaviorExitFast).
				Obj()

			ginkgo.By("Creating the trainjob", func() {
				util.MustCreate(ctx, k8sManagerClient, trainjob)
			})

			wlLookupKey := types.NamespacedName{Name: workloadtrainjob.GetWorkloadNameForTrainJob(trainjob.Name, trainjob.UID), Namespace: managerNs.Name}

			admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
			ginkgo.GinkgoLogr.Info("TrainJob %s is admitted in worker cluster %s", trainjob.Name, admittedWorker)

			ginkgo.By("Checking the TrainJob is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdTrainJob := &kftrainer.TrainJob{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(trainjob), createdTrainJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdTrainJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.When("Ray integration tests", ginkgo.Ordered, ginkgo.Label("feature:kuberay"), func() {
			ginkgo.It("Should run a RayJob on worker if admitted", func() {
				kuberayTestImage := util.GetKuberayTestImage()
				rayjob := testingrayjob.MakeJob("rayjob1", managerNs.Name).
					Suspend(true).
					Queue(managerLq.Name).
					WithSubmissionMode(rayv1.K8sJobMode).
					Entrypoint("python -c \"import ray; ray.init(); print(ray.cluster_resources())\"").
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
					Image(rayv1.HeadNode, kuberayTestImage).
					Image(rayv1.WorkerNode, kuberayTestImage).
					TerminationGracePeriod(1).
					Obj()

				ginkgo.By("Creating the RayJob", func() {
					util.MustCreate(ctx, k8sManagerClient, rayjob)
				})

				wlLookupKey := types.NamespacedName{Name: workloadrayjob.GetWorkloadNameForRayJob(rayjob.Name, rayjob.UID), Namespace: managerNs.Name}

				admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayJob %s/%s is admitted in worker cluster %s", rayjob.Name, rayjob.Namespace, admittedWorker))

				ginkgo.By("Waiting for the RayJob to finish", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayJob := &rayv1.RayJob{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayjob), createdRayJob)).To(gomega.Succeed())
						g.Expect(createdRayJob.Status.JobDeploymentStatus).To(gomega.Equal(rayv1.JobDeploymentStatusComplete))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
					util.ExpectWorkloadToFinish(ctx, k8sManagerClient, wlLookupKey)
				})

				ginkgo.By("Checking no objects are left in the worker clusters and the RayJob is completed", func() {
					wl := &kueue.Workload{
						ObjectMeta: metav1.ObjectMeta{
							Name:      wlLookupKey.Name,
							Namespace: wlLookupKey.Namespace,
						},
					}
					util.ExpectObjectToBeDeletedOnClusters(ctx, wl, k8sWorker1Client, k8sWorker2Client)
					util.ExpectObjectToBeDeletedOnClusters(ctx, rayjob, k8sWorker1Client, k8sWorker2Client)
				})
			})

			ginkgo.It("Should run a RayCluster on worker if admitted", func() {
				kuberayTestImage := util.GetKuberayTestImage()
				raycluster := testingraycluster.MakeCluster("raycluster1", managerNs.Name).
					Suspend(true).
					Queue(managerLq.Name).
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
					Image(rayv1.HeadNode, kuberayTestImage, []string{}).
					Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
					Obj()

				ginkgo.By("Creating the RayCluster", func() {
					util.MustCreate(ctx, k8sManagerClient, raycluster)
				})

				wlLookupKey := types.NamespacedName{Name: workloadraycluster.GetWorkloadNameForRayCluster(raycluster.Name, raycluster.UID), Namespace: managerNs.Name}
				// the execution should be given to the worker1
				admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayCluster %s/%s is admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorker))

				ginkgo.By("Checking the RayCluster is ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdRayCluster.Status.ReadyWorkerReplicas).To(gomega.Equal(int32(1)))
						g.Expect(createdRayCluster.Status.AvailableWorkerReplicas).To(gomega.Equal(int32(1)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should scale an elastic RayCluster on worker if admitted", func() {
				kuberayTestImage := util.GetKuberayTestImage()
				raycluster := testingraycluster.MakeCluster("raycluster-elastic", managerNs.Name).
					Suspend(true).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Queue(managerLq.Name).
					ScaleFirstWorkerGroup(1).
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "200m").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "200m").
					Image(rayv1.HeadNode, kuberayTestImage, []string{}).
					Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
					Obj()

				ginkgo.By("Creating the elastic RayCluster", func() {
					util.MustCreate(ctx, k8sManagerClient, raycluster)
				})

				// Elastic (workload-slicing) RayCluster workloads are named with the
				// object's generation, so fetch the created object to derive the name
				// of its current slice.
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), raycluster)).To(gomega.Succeed())
				wlLookupKey := types.NamespacedName{
					Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(raycluster.Name, raycluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), raycluster.GetGeneration()),
					Namespace: managerNs.Name,
				}
				admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("elastic RayCluster %s/%s is admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorker))

				// The assertions below check DesiredWorkerReplicas, which KubeRay derives
				// directly from the worker cluster's RayCluster spec. This is exactly what
				// the manager-driven elastic sync propagates, and it does not depend on the
				// Ray runtime becoming healthy (Ray pod readiness is KubeRay's own concern).
				ginkgo.By("Checking the RayCluster starts with one worker on the worker cluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Scaling the first worker group up to three on the manager", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](3)
						g.Expect(k8sManagerClient.Update(ctx, createdRayCluster)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the scaled-up worker replicas propagate to the worker cluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(3)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Scaling the first worker group back down to one on the manager", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](1)
						g.Expect(k8sManagerClient.Update(ctx, createdRayCluster)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the reduced worker replicas propagate to the worker cluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should reflect worker-side autoscaler resizes (up and down) of an elastic RayCluster back on the manager", func() {
				kuberayTestImage := util.GetKuberayTestImage()
				// enableInTreeAutoscaling makes the worker the source of truth for
				// worker-group replicas: KubeRay's autoscaler resizes the RayCluster on
				// the worker cluster directly. A replica range (min<max) is allowed
				// because autoscaler-driven resizes are reflected back to the manager.
				raycluster := testingraycluster.MakeCluster("raycluster-autoscale", managerNs.Name).
					Suspend(true).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Queue(managerLq.Name).
					WithEnableAutoscaling(new(true)).
					FirstWorkerGroupReplicas(1, 1, 3).
					ElasticSchedulingGates().
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "200m").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "200m").
					Image(rayv1.HeadNode, kuberayTestImage, []string{}).
					Image(rayv1.WorkerNode, kuberayTestImage, []string{}).
					Obj()

				ginkgo.By("Creating the elastic autoscaling RayCluster", func() {
					util.MustCreate(ctx, k8sManagerClient, raycluster)
				})

				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), raycluster)).To(gomega.Succeed())
				wlLookupKey := types.NamespacedName{
					Name:      jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(raycluster.Name, raycluster.UID, rayv1.GroupVersion.WithKind("RayCluster"), raycluster.GetGeneration()),
					Namespace: managerNs.Name,
				}
				admittedWorkerName := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				workerClient := kubernetesClients[admittedWorkerName].client
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("elastic autoscaling RayCluster %s/%s admitted in worker cluster %s", raycluster.Name, raycluster.Namespace, admittedWorkerName))

				ginkgo.By("Checking the RayCluster starts with one worker on the worker cluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Simulating the Ray autoscaler: scale the worker group up to two on the worker cluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						workerRayCluster := &rayv1.RayCluster{}
						g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
						workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](2)
						g.Expect(workerClient.Update(ctx, workerRayCluster)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the autoscaled size is reflected back onto the manager's RayCluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(2)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the worker RayCluster keeps the autoscaled size (not torn down mid-handover)", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						workerRayCluster := &rayv1.RayCluster{}
						g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
						g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(2)))
						g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("Simulating the Ray autoscaler: scale the worker group back down to one on the worker cluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						workerRayCluster := &rayv1.RayCluster{}
						g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
						workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas = ptr.To[int32](1)
						g.Expect(workerClient.Update(ctx, workerRayCluster)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the scaled-down size is reflected back onto the manager's RayCluster", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayCluster := &rayv1.RayCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), createdRayCluster)).To(gomega.Succeed())
						g.Expect(ptr.Deref(createdRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(1)))
						g.Expect(createdRayCluster.Status.DesiredWorkerReplicas).To(gomega.Equal(int32(1)))
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Checking the worker RayCluster keeps running at the scaled-down size", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						workerRayCluster := &rayv1.RayCluster{}
						g.Expect(workerClient.Get(ctx, client.ObjectKeyFromObject(raycluster), workerRayCluster)).To(gomega.Succeed())
						g.Expect(ptr.Deref(workerRayCluster.Spec.WorkerGroupSpecs[0].Replicas, -1)).To(gomega.BeEquivalentTo(int32(1)))
						g.Expect(ptr.Deref(workerRayCluster.Spec.Suspend, false)).To(gomega.BeFalse())
					}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("Should run a RayService on worker if admitted", ginkgo.Label("requires:fullray"), func() {
				kuberayTestImage := util.GetKuberayTestImage()

				// Create ConfigMap with a simple Ray Serve application
				configMap := &corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "rayservice-hello",
						Namespace: managerNs.Name,
					},
					Data: map[string]string{
						"hello_serve.py": `from ray import serve

@serve.deployment
class HelloWorld:
    def __call__(self, request):
        return "Hello, World!"

app = HelloWorld.bind()`,
					},
				}

				serveConfigV2 := `applications:
  - name: hello_app
    import_path: hello_serve:app
    route_prefix: /
    deployments:
      - name: HelloWorld
        num_replicas: 1
        max_replicas_per_node: 1
        ray_actor_options:
          num_cpus: 0.2`

				volumes := []corev1.Volume{
					{
						Name: "code-sample",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{
									Name: "rayservice-hello",
								},
								Items: []corev1.KeyToPath{
									{
										Key:  "hello_serve.py",
										Path: "hello_serve.py",
									},
								},
							},
						},
					},
				}
				volumeMounts := []corev1.VolumeMount{
					{
						Name:      "code-sample",
						MountPath: "/home/ray/samples",
					},
				}
				env := []corev1.EnvVar{
					{
						Name:  "PYTHONPATH",
						Value: "/home/ray/samples:$PYTHONPATH",
					},
				}

				rayService := testingrayservice.MakeService("rayservice1", managerNs.Name).
					Suspend(true).
					Queue(managerLq.Name).
					RequestAndLimit(rayv1.HeadNode, corev1.ResourceCPU, "1").
					RequestAndLimit(rayv1.WorkerNode, corev1.ResourceCPU, "0.5").
					Image(rayv1.HeadNode, kuberayTestImage).
					Image(rayv1.WorkerNode, kuberayTestImage).
					RayStartParam(rayv1.HeadNode, "object-store-memory", "100000000").
					WithServeConfigV2(serveConfigV2).
					Env(rayv1.HeadNode, env).
					Env(rayv1.WorkerNode, env).
					Volumes(rayv1.HeadNode, volumes).
					Volumes(rayv1.WorkerNode, volumes).
					VolumeMounts(rayv1.HeadNode, volumeMounts).
					VolumeMounts(rayv1.WorkerNode, volumeMounts).
					TerminationGracePeriod(1).
					Obj()

				rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].GroupName = "small-group"
				rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MinReplicas = ptr.To[int32](1)
				rayService.Spec.RayClusterSpec.WorkerGroupSpecs[0].MaxReplicas = ptr.To[int32](2)

				ginkgo.By("Creating the ConfigMap on all clusters", func() {
					worker1ConfigMap := configMap.DeepCopy()
					worker2ConfigMap := configMap.DeepCopy()
					util.MustCreate(ctx, k8sManagerClient, configMap)
					util.MustCreate(ctx, k8sWorker1Client, worker1ConfigMap)
					util.MustCreate(ctx, k8sWorker2Client, worker2ConfigMap)
				})

				ginkgo.By("Creating the RayService", func() {
					util.MustCreate(ctx, k8sManagerClient, rayService)
				})

				wlLookupKey := types.NamespacedName{Name: workloadrayservice.GetWorkloadNameForRayService(rayService.Name, rayService.UID), Namespace: managerNs.Name}

				admittedWorker := util.ExpectWorkloadsToBeAdmittedAndGetWorkerName(ctx, k8sManagerClient, wlLookupKey, multiKueueAc.Name)
				ginkgo.GinkgoLogr.Info(fmt.Sprintf("RayService %s/%s is admitted in worker cluster %s", rayService.Name, rayService.Namespace, admittedWorker))

				ginkgo.By("Checking the RayService is running", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						createdRayService := &rayv1.RayService{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(rayService), createdRayService)).To(gomega.Succeed())
						g.Expect(createdRayService.Spec.RayClusterSpec.Suspend).To(gomega.Equal(new(false)))
						g.Expect(apimeta.IsStatusConditionTrue(createdRayService.Status.Conditions, string(rayv1.RayServiceReady))).To(gomega.BeTrue())
					}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})
})
