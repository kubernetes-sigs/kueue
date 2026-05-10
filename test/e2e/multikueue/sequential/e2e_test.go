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

package sequential

import (
	"fmt"
	"os/exec"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/tools/clientcmd/api"
	apiv1 "k8s.io/client-go/tools/clientcmd/api/v1"
	"k8s.io/utils/ptr"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue Sequential", func() {
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
	)

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(ctx, k8sManagerClient, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(ctx, k8sWorker1Client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(ctx, k8sWorker2Client, managerNs.Name)

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, "multikueue1").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster1)

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, "multikueue2").Obj()
		util.MustCreate(ctx, k8sManagerClient, workerCluster2)

		multiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters("worker1", "worker2").Obj()
		util.MustCreate(ctx, k8sManagerClient, multiKueueConfig)

		multiKueueAc = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", multiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(ctx, k8sManagerClient, multiKueueAc)

		managerHighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerHighWPC)

		managerLowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sManagerClient, managerLowWPC)

		managerFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sManagerClient, managerFlavor)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "4G").
					Resource(corev1.ResourceEphemeralStorage, "100G").
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

		worker1HighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1HighWPC)

		worker1LowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1LowWPC)

		worker1Flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

		worker1Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1G").
					Resource(corev1.ResourceEphemeralStorage, "15G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq)

		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Lq)

		worker2HighWPC = utiltestingapi.MakeWorkloadPriorityClass("high-workload").PriorityValue(300).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2HighWPC)

		worker2LowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-workload").PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2LowWPC)

		worker2Flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

		worker2Cq = utiltestingapi.MakeClusterQueue("q1").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
					Resource(corev1.ResourceCPU, "1200m").
					Resource(corev1.ResourceMemory, "4G").
					Resource(corev1.ResourceEphemeralStorage, "5G").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Cq)

		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sWorker2Client, worker2Lq)
	})

	ginkgo.AfterEach(func() {
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

	ginkgo.When("Incremental mode", ginkgo.Ordered, func() {
		var defaultManagerKueueCfg *kueueconfig.Configuration

		ginkgo.BeforeAll(func() {
			ginkgo.By("setting MultiKueue Dispatcher to Incremental", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				newCfg := defaultManagerKueueCfg.DeepCopy()
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, newCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					if cfg.MultiKueue == nil {
						cfg.MultiKueue = &kueueconfig.MultiKueue{}
					}
					cfg.MultiKueue.DispatcherName = ptr.To(kueueconfig.MultiKueueDispatcherModeIncremental)
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("setting MultiKueue Dispatcher back to AllAtOnce", func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName)
			})
		})
		ginkgo.It("Should run a job on worker if admitted", func() {
			// Since it requires 2G of memory, this job can only be admitted in worker 2.
			job := testingjob.MakeJob("job", managerNs.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "2G").
				TerminationGracePeriod(1).
				// Give it the time to be observed Active in the live status update step.
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()

			ginkgo.By("Creating the job", func() {
				util.MustCreate(ctx, k8sManagerClient, job)
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.ManagedBy, "")).To(gomega.BeEquivalentTo(kueue.MultiKueueControllerName))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			createdLeaderWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}
			// the execution should be given to the worker
			util.ExpectWorkloadAdmittedWithCheck(ctx, wlLookupKey, multiKueueAc.Name, "worker2", k8sManagerClient)

			ginkgo.By("Waiting for the manager's job unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := &batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for the job to get status updates", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJob := batchv1.Job{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Status.StartTime).NotTo(gomega.BeNil())
					g.Expect(createdJob.Status.Active).To(gomega.Equal(int32(1)))
					g.Expect(createdJob.Status.CompletionTime).To(gomega.BeNil())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the job's pod", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("batch.kubernetes.io/job-name=%s", job.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker2Client, worker2RestClient, worker2Cfg, job.Namespace, 1, 0, listOpts)
			})

			ginkgo.By("Waiting for the job to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadFinished, kueue.WorkloadFinishedReasonSucceeded))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the job is completed", func() {
				util.ExpectObjectToBeDeletedOnClusters(ctx, createdLeaderWorkload, k8sWorker1Client, k8sWorker2Client)
				util.ExpectObjectToBeDeletedOnClusters(ctx, job, k8sWorker1Client, k8sWorker2Client)

				createdJob := &batchv1.Job{}
				gomega.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				gomega.Expect(createdJob.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
					batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					},
					cmpopts.IgnoreFields(batchv1.JobCondition{}, "LastTransitionTime", "LastProbeTime", "Reason", "Message"))))
			})
		})
	})

	ginkgo.When("The connection to a worker cluster is unreliable", func() {
		ginkgo.It("Should update the cluster status to reflect the connection state", func() {
			worker1Cq2 := utiltestingapi.MakeClusterQueue("q2").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1G").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sWorker1Client, worker1Cq2)

			worker1Container := fmt.Sprintf("%s-control-plane", worker1ClusterName)
			worker1ClusterKey := client.ObjectKeyFromObject(workerCluster1)
			ginkgo.By("Disconnecting worker1 node's APIServer", func() {
				sedCommand := `sed -i '/^[[:space:]]*- kube-apiserver/a\    - --bind-address=127.0.0.1' /etc/kubernetes/manifests/kube-apiserver.yaml`
				cmd := exec.Command("docker", "exec", worker1Container, "sh", "-c", sedCommand)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)

				podList := &corev1.PodList{}
				podListOptions := client.InNamespace(kueueNS)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.List(ctx, podList, podListOptions)).Should(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).ShouldNot(gomega.Succeed())
			})

			ginkgo.By("Waiting for the cluster to become inactive", func() {
				readClient := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, worker1ClusterKey, readClient)).To(gomega.Succeed())
					g.Expect(readClient.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(kueue.MultiKueueClusterActive, "ClientConnectionFailed"))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Reconnecting worker1 node's APIServer", func() {
				sedCommand := `sed -i '/^[[:space:]]*- --bind-address=127.0.0.1/d' /etc/kubernetes/manifests/kube-apiserver.yaml`
				cmd := exec.Command("docker", "exec", worker1Container, "sh", "-c", sedCommand)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Waiting for the cluster to become active", func() {
				readClient := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, worker1ClusterKey, readClient)).To(gomega.Succeed())
					g.Expect(readClient.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Active",
							Message: "Connected",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Waiting for kube-system to become available again", func() {
				util.WaitForKubeSystemControllersAvailability(ctx, k8sWorker1Client, worker1Container)
			})

			ginkgo.By("Restart Kueue and wait for availability again", func() {
				util.RestartKueueController(ctx, k8sWorker1Client, worker1ClusterName)
			})

			ginkgo.By("Checking that the Kueue is operational after reconnection", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq2, true, util.VeryLongTimeout)
			})
		})
	})

	ginkgo.When("Connection via ClusterProfile no plugins", ginkgo.Ordered, func() {
		var (
			workerCluster3         *kueue.MultiKueueCluster
			defaultManagerKueueCfg *kueueconfig.Configuration
			cp                     *inventoryv1alpha1.ClusterProfile
		)

		ginkgo.BeforeAll(func() {
			ginkgo.By("setting MultiKueueClusterProfile feature gate", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				newCfg := defaultManagerKueueCfg.DeepCopy()
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, newCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					cfg.FeatureGates[string(features.MultiKueueClusterProfile)] = true
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("reverting the configuration", func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName)
			})
		})

		ginkgo.BeforeEach(func() {
			workerCluster3 = utiltestingapi.MakeMultiKueueCluster("worker3").ClusterProfile("clusterprofile3-missing").Obj()
			util.MustCreate(ctx, k8sManagerClient, workerCluster3)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, cp, true, util.MediumTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster3, true, util.MediumTimeout)
		})

		ginkgo.It("uses ClusterProfile as way to connect worker cluster", func() {
			ginkgo.By("updating MultiKueueConfig to include worker that use ClusterProfile", func() {
				createdMkConfig := &kueue.MultiKueueConfig{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(multiKueueConfig), createdMkConfig)).To(gomega.Succeed())
					if len(createdMkConfig.Spec.Clusters) == 2 {
						createdMkConfig.Spec.Clusters = append(createdMkConfig.Spec.Clusters, "worker3")
						g.Expect(k8sManagerClient.Update(ctx, createdMkConfig)).To(gomega.Succeed())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			worker3MkClusterKey := client.ObjectKeyFromObject(workerCluster3)
			ginkgo.By("checking MultiKueueCluster status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, worker3MkClusterKey, createdCluster)).To(gomega.Succeed())
					g.Expect(createdCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionFalse,
							Reason:  "BadClusterProfile",
							Message: "load client config failed: ClusterProfile.multicluster.x-k8s.io \"clusterprofile3-missing\" not found",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("creating missing ClusterProfile", func() {
				cp = utiltestingapi.MakeClusterProfile("clusterprofile3", kueueNS).
					ClusterManager("clustermanager3").
					Obj()
				util.MustCreate(ctx, k8sManagerClient, cp)
			})
			ginkgo.By("checking ClusterProfile exists", func() {
				clusterProfileKey := client.ObjectKeyFromObject(cp)
				gomega.Eventually(func(g gomega.Gomega) {
					createdClusterProfile := &inventoryv1alpha1.ClusterProfile{}
					g.Expect(k8sManagerClient.Get(ctx, clusterProfileKey, createdClusterProfile)).To(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("triggering MultiKueueCluster reconciliation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, worker3MkClusterKey, createdCluster)).To(gomega.Succeed())
					createdCluster.Spec.ClusterSource.ClusterProfileRef = &kueue.ClusterProfileReference{Name: "clusterprofile3"}
					g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			worker3MkClusterKey = client.ObjectKeyFromObject(workerCluster3)
			ginkgo.By("checking MultiKueueCluster status again", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, worker3MkClusterKey, createdCluster)).To(gomega.Succeed())
					g.Expect(createdCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.MultiKueueClusterActive,
							Status:  metav1.ConditionFalse,
							Reason:  "BadClusterProfile",
							Message: "load client config failed: no credentials provider configured",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Connection via ClusterProfile with plugins", ginkgo.Ordered, func() {
		const (
			secretReaderPath    = "/plugins/secretreader-plugin"
			volumeName          = "plugins"
			volumeMountPath     = "/plugins"
			pluginContainerName = "secretreader-plugin-init"
		)
		var (
			defaultManagerKueueCfg  *kueueconfig.Configuration
			secretReaderRoleBinding *rbacv1.RoleBinding
			secretReaderRole        *rbacv1.Role

			defaultManagerDeployment = &appsv1.Deployment{}
			clusterProfileSecrets    = make([]*corev1.Secret, 0)
			clusterProfiles          = make([]*inventoryv1alpha1.ClusterProfile, 0)
			deploymentKey            = types.NamespacedName{Namespace: kueueNS, Name: "kueue-controller-manager"}
		)

		ginkgo.BeforeAll(func() {
			ginkgo.By("Creating Role and RoleBinding for secretreader-plugin", func() {
				secretReaderRole = utiltesting.MakeRole("secretreader", kueueNS).
					Rule([]string{""}, []string{"secrets"}, []string{"get", "list", "watch"}).
					Obj()
				util.MustCreate(ctx, k8sManagerClient, secretReaderRole)

				secretReaderRoleBinding = utiltesting.MakeRoleBinding("secretreader-binding", kueueNS).
					Subject(rbacv1.ServiceAccountKind, "kueue-controller-manager", kueueNS).
					RoleRef(rbacv1.GroupName, "ClusterRole", secretReaderRole.Name).
					Obj()
				util.MustCreate(ctx, k8sManagerClient, secretReaderRoleBinding)
			})

			ginkgo.By("Update 'kueue-controller-manager' deployment to have the secretreader-plugin binary", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, deploymentKey, defaultManagerDeployment)).To(gomega.Succeed())
					updatedDeployment := defaultManagerDeployment.DeepCopy()

					for i, container := range updatedDeployment.Spec.Template.Spec.Containers {
						if container.Name == "manager" {
							updatedDeployment.Spec.Template.Spec.Containers[i].VolumeMounts = append(
								updatedDeployment.Spec.Template.Spec.Containers[i].VolumeMounts,
								corev1.VolumeMount{
									Name:      volumeName,
									MountPath: volumeMountPath,
								},
							)
							break
						}
					}

					kubeVer := util.GetKubernetesVersion(managerCfg)
					if version.CompareKubeAwareVersionStrings(kubeVer, versionutil.MustParseGeneric("v1.35.0").String()) >= 0 {
						updatedDeployment.Spec.Template.Spec.Volumes = append(
							updatedDeployment.Spec.Template.Spec.Volumes,
							corev1.Volume{
								Name: volumeName,
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
									Image: &corev1.ImageVolumeSource{
										Reference:  util.GetClusterProfilePluginImage(),
										PullPolicy: corev1.PullIfNotPresent,
									},
								},
							},
						)
					} else {
						updatedDeployment.Spec.Template.Spec.InitContainers = []corev1.Container{
							*utiltesting.MakeContainer().
								Name(pluginContainerName).
								Image(util.GetClusterProfilePluginImage()).
								ImagePullPolicy(corev1.PullIfNotPresent).
								Command("cp", "/secretreader-plugin", secretReaderPath).
								VolumeMount(volumeName, volumeMountPath).
								Obj(),
						}
						updatedDeployment.Spec.Template.Spec.Volumes = append(
							updatedDeployment.Spec.Template.Spec.Volumes,
							corev1.Volume{
								Name: volumeName,
								VolumeSource: corev1.VolumeSource{
									EmptyDir: &corev1.EmptyDirVolumeSource{},
								},
							},
						)
					}
					g.Expect(k8sManagerClient.Update(ctx, updatedDeployment)).Should(gomega.Succeed())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				// We will wait for Kueue after setting the configuration
			})

			ginkgo.By("Updating MultiKueue configuration with CredentialsProviders", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName, func(cfg *kueueconfig.Configuration) {
					cfg.FeatureGates[string(features.MultiKueueClusterProfile)] = true
					if cfg.MultiKueue == nil {
						cfg.MultiKueue = &kueueconfig.MultiKueue{}
					}
					cfg.MultiKueue.ClusterProfile = &kueueconfig.ClusterProfile{
						CredentialsProviders: []kueueconfig.ClusterProfileCredentialsProvider{
							{
								Name: "secretreader",
								ExecConfig: api.ExecConfig{
									APIVersion:         "client.authentication.k8s.io/v1",
									Command:            secretReaderPath,
									ProvideClusterInfo: true,
									InteractiveMode:    api.NeverExecInteractiveMode,
								},
							},
						},
					}
				})
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("setting back the configuration", func() {
				// Just update Kueue configuration. We will restart Kueue later.
				util.UpdateKueueConfiguration(ctx, k8sManagerClient, defaultManagerKueueCfg)
			})

			ginkgo.By("setting back the deployment", func() {
				util.UpdateDeploymentAndWaitForProgressing(ctx, k8sManagerClient, deploymentKey, managerClusterName, func(deployment *appsv1.Deployment) {
					deployment.Spec = *defaultManagerDeployment.Spec.DeepCopy()
				})
			})

			ginkgo.By("wait for Kueue availability", func() {
				// We are using NoRestartCountCheck because we expect one fails in MultiKueue tests.
				// This happens on "The connection to a worker cluster is unreliable" test case.
				util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sManagerClient)
			})

			for _, s := range clusterProfileSecrets {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, s, true, util.Timeout)
			}

			for _, c := range clusterProfiles {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, c, true, util.Timeout)
			}

			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, secretReaderRoleBinding, true, util.MediumTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, secretReaderRole, true, util.MediumTimeout)
		})

		ginkgo.It("Should be able to use ClusterProfile as way to connect worker cluster", func() {
			ginkgo.By("creating secrets with tokens to read from", func() {
				worker1AuthInfo := util.GetAuthInfoFromKubeConfig(worker1KConfig)
				worker2AuthInfo := util.GetAuthInfoFromKubeConfig(worker2KConfig)

				secretsData := map[string]string{
					"multikueue1-cp": worker1AuthInfo.Token,
					"multikueue2-cp": worker2AuthInfo.Token,
				}
				for name, token := range secretsData {
					secret := utiltesting.MakeSecret(name, kueueNS).Data("token", []byte(token)).Obj()
					util.MustCreate(ctx, k8sManagerClient, secret)
					clusterProfileSecrets = append(clusterProfileSecrets, secret)
				}
			})

			mkc := []*kueue.MultiKueueCluster{workerCluster1, workerCluster2}
			ginkgo.By("Update existing worker clusters to use ClusterProfiles", func() {
				for _, wc := range mkc {
					gomega.Eventually(func(g gomega.Gomega) {
						createdCluster := &kueue.MultiKueueCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(wc), createdCluster)).To(gomega.Succeed())
						createdCluster.Spec.ClusterSource.KubeConfig = nil
						createdCluster.Spec.ClusterSource.ClusterProfileRef = &kueue.ClusterProfileReference{Name: wc.Name}
						g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})
			ginkgo.By("Create ClusterProfiles for existing worker clusters", func() {
				for _, wc := range mkc {
					c := utiltestingapi.MakeClusterProfile(wc.Name, kueueNS).ClusterManager("secretreader").Obj()
					util.MustCreate(ctx, k8sManagerClient, c)
					clusterProfiles = append(clusterProfiles, c)
				}
			})
			ginkgo.By("Update ClusterProfiles with AccessProviders", func() {
				secrets := []string{"multikueue1-cp", "multikueue2-cp"}
				workerClusterNames := []string{worker1ClusterName, worker2ClusterName}
				workerCAData := [][]byte{worker1Cfg.CAData, worker2Cfg.CAData}
				for i, wc := range mkc {
					gomega.Eventually(func(g gomega.Gomega) {
						createdCp := &inventoryv1alpha1.ClusterProfile{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKey{Namespace: kueueNS, Name: wc.Name}, createdCp)).To(gomega.Succeed())
						createdCp.Status.AccessProviders = []inventoryv1alpha1.AccessProvider{
							{
								Name: "secretreader",
								Cluster: apiv1.Cluster{
									Server:                   util.GetClusterServerAddress(workerClusterNames[i]),
									CertificateAuthorityData: workerCAData[i],
									Extensions: []apiv1.NamedExtension{
										{
											Name: "client.authentication.k8s.io/exec",
											Extension: runtime.RawExtension{
												Raw: fmt.Appendf(nil, `{"clusterName":"%s"}`, secrets[i]),
											},
										},
									},
								},
							},
						}
						g.Expect(k8sManagerClient.Status().Update(ctx, createdCp)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Check MultiKueueCluster status", func() {
				for _, wc := range mkc {
					gomega.Eventually(func(g gomega.Gomega) {
						createdCluster := &kueue.MultiKueueCluster{}
						g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(wc), createdCluster)).To(gomega.Succeed())
						g.Expect(createdCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
							metav1.Condition{
								Type:    kueue.MultiKueueClusterActive,
								Status:  metav1.ConditionTrue,
								Reason:  "Active",
								Message: "Connected",
							},
							util.IgnoreConditionTimestampsAndObservedGeneration)))
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Check AdmissionChecks are active after switching to ClusterProfiles", func() {
				util.ExpectAdmissionChecksToBeActive(ctx, k8sManagerClient, multiKueueAc)
			})
		})
	})

	ginkgo.When("MultiKueueOrchestratedPreemption is enabled", ginkgo.Ordered, func() {
		var defaultManagerKueueCfg, defaultWorker1KueueCfg, defaultWorker2KueueCfg *kueueconfig.Configuration

		ginkgo.BeforeAll(func() {
			ginkgo.By("setting MultiKueueClusterProfile feature gate", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				defaultWorker1KueueCfg = util.GetKueueConfiguration(ctx, k8sWorker1Client)
				defaultWorker2KueueCfg = util.GetKueueConfiguration(ctx, k8sWorker2Client)

				updateCfg := func(cfg *kueueconfig.Configuration) {
					cfg.FeatureGates[string(features.MultiKueueOrchestratedPreemption)] = true
				}

				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg.DeepCopy(), managerClusterName, updateCfg)
				util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker1Client, defaultWorker1KueueCfg.DeepCopy(), worker1ClusterName, updateCfg)
				util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker2Client, defaultWorker2KueueCfg.DeepCopy(), worker2ClusterName, updateCfg)
			})
		})
		ginkgo.AfterAll(func() {
			ginkgo.By("reverting the configuration", func() {
				util.UpdateKueueConfigurationAndRestart(ctx, k8sManagerClient, defaultManagerKueueCfg, managerClusterName)
				util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker1Client, defaultWorker1KueueCfg, worker1ClusterName)
				util.UpdateKueueConfigurationAndRestart(ctx, k8sWorker2Client, defaultWorker2KueueCfg, worker2ClusterName)
			})
		})

		ginkgo.It("should not trigger concurrent preemptions", func() {
			// Fits only in worker1
			lowJob1 := testingjob.MakeJob("low-job1", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(corev1.ResourceEphemeralStorage, "15G").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, lowJob1)

			lowWlKey1 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob1.Name, lowJob1.UID), Namespace: managerNs.Name}

			managerLowWl1 := &kueue.Workload{}
			workerLowW1 := &kueue.Workload{}

			ginkgo.By("Checking that the first low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey1, managerLowWl1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl1)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the first low-priority workload is created in worker1 and not in worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey1, workerLowW1)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowW1)).To(gomega.BeTrue())

					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey1, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// Fits only in worker2
			lowJob2 := testingjob.MakeJob("low-job2", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerLowWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "1.5G").
				RequestAndLimit(corev1.ResourceEphemeralStorage, "5G").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, lowJob2)

			lowWlKey2 := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowJob2.Name, lowJob2.UID), Namespace: managerNs.Name}

			managerLowWl2 := &kueue.Workload{}
			workerLowW2 := &kueue.Workload{}

			ginkgo.By("Checking that the second low-priority workload is created and admitted in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, lowWlKey2, managerLowWl2)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(managerLowWl2)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the second low-priority workload is created in worker2 and not in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker2Client.Get(ctx, lowWlKey2, workerLowW2)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(workerLowW2)).To(gomega.BeTrue())

					g.Expect(k8sWorker1Client.Get(ctx, lowWlKey2, &kueue.Workload{})).To(utiltesting.BeNotFoundError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// Can fit in both workers after preemptions
			highJob := testingjob.MakeJob("high-job", managerNs.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				WorkloadPriorityClass(managerHighWPC.Name).
				Queue(kueue.LocalQueueName(managerLq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "0.1").
				RequestAndLimit(corev1.ResourceMemory, "0.1G").
				RequestAndLimit(corev1.ResourceEphemeralStorage, "5G").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, highJob)

			highWlKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: managerNs.Name}

			managerHighWl := &kueue.Workload{}

			ginkgo.By("Checking that the high-priority workload is created in the manager cluster", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, highWlKey, managerHighWl)).To(gomega.Succeed())
					g.Expect(managerHighWl.Spec.QueueName).To(gomega.BeEquivalentTo(managerLq.Name))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			var evictedWlKey types.NamespacedName
			var unaffectedWlKey types.NamespacedName
			var unaffectedWorkerClient client.Client

			ginkgo.By("Checking that the high-priority workload is admitted in one of the workers", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					worker1HighWorkload := &kueue.Workload{}
					worker2HighWorkload := &kueue.Workload{}

					worker1Error := k8sWorker1Client.Get(ctx, highWlKey, worker1HighWorkload)
					worker2Error := k8sWorker2Client.Get(ctx, highWlKey, worker2HighWorkload)

					g.Expect(worker1Error == nil).NotTo(gomega.Equal(worker2Error == nil))

					if worker1Error == nil {
						g.Expect(worker1HighWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
						evictedWlKey = lowWlKey1
						unaffectedWlKey = lowWlKey2
						unaffectedWorkerClient = k8sWorker2Client
					} else {
						g.Expect(worker2HighWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
						evictedWlKey = lowWlKey2
						unaffectedWlKey = lowWlKey1
						unaffectedWorkerClient = k8sWorker1Client
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the non-evicted workload remains Admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					unaffectedWl := &kueue.Workload{}

					g.Expect(k8sManagerClient.Get(ctx, unaffectedWlKey, unaffectedWl)).To(gomega.Succeed())
					g.Expect(unaffectedWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(workload.IsEvicted(unaffectedWl)).To(gomega.BeFalse())

					g.Expect(unaffectedWorkerClient.Get(ctx, unaffectedWlKey, unaffectedWl)).To(gomega.Succeed())
					g.Expect(unaffectedWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					g.Expect(workload.IsEvicted(unaffectedWl)).To(gomega.BeFalse())
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the evicted workload was requeued successfully", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					evictedWl := &kueue.Workload{}

					// Evicted workload was requeued on the manager
					g.Expect(k8sManagerClient.Get(ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
					g.Expect(evictedWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
					g.Expect(evictedWl.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadAdmitted))

					// Evicted workload is requeued and pending on worker 1
					g.Expect(k8sWorker1Client.Get(ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
					g.Expect(evictedWl.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))

					// Evicted workload is requeued and pending on worker 2
					g.Expect(k8sWorker2Client.Get(ctx, evictedWlKey, evictedWl)).To(gomega.Succeed())
					g.Expect(evictedWl.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
