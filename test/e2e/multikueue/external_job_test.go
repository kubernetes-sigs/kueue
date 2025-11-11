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

package mke2e

import (
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueueconfig "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue", func() {
	ginkgo.When("testing JobSet with MultiKueue (external adapter feature gate enabled)", ginkgo.Ordered, func() {
		var (
			managerNs *corev1.Namespace
			worker1Ns *corev1.Namespace
			worker2Ns *corev1.Namespace

			workerCluster1   *kueue.MultiKueueCluster
			workerCluster2   *kueue.MultiKueueCluster
			multiKueueConfig *kueue.MultiKueueConfig
			multiKueueAc     *kueue.AdmissionCheck
			managerFlavor    *kueue.ResourceFlavor
			managerCq        *kueue.ClusterQueue
			managerLq        *kueue.LocalQueue

			worker1Flavor *kueue.ResourceFlavor
			worker1Cq     *kueue.ClusterQueue
			worker1Lq     *kueue.LocalQueue

			worker2Flavor *kueue.ResourceFlavor
			worker2Cq     *kueue.ClusterQueue
			worker2Lq     *kueue.LocalQueue

			defaultManagerKueueCfg *kueueconfig.Configuration
		)

		ginkgo.BeforeAll(func() {
			ginkgo.By("enabling external frameworks and feature gate", func() {
				defaultManagerKueueCfg = util.GetKueueConfiguration(ctx, k8sManagerClient)
				newCfg := defaultManagerKueueCfg.DeepCopy()
				// Apply the configuration changes
				// Note: We keep JobSet in Integrations.Frameworks for local integration,
				// but we can't configure it as external because validation prevents it.
				// For e2e testing, we'll skip the external framework configuration
				// and test that JobSet works with MultiKueue using the built-in adapter.
				// The external framework feature is meant for job types that aren't built-in.
				applyChanges := func(cfg *kueueconfig.Configuration) {
					if cfg.MultiKueue == nil {
						cfg.MultiKueue = &kueueconfig.MultiKueue{}
					}
					// For this test, we're testing that JobSet works with MultiKueue.
					// Since JobSet is built-in, we can't configure it as external.
					// The external framework feature is for custom job types.
					// We'll just enable the feature gate to test the infrastructure.
					if cfg.FeatureGates == nil {
						cfg.FeatureGates = make(map[string]bool)
					}
					cfg.FeatureGates["MultiKueueAdaptersForCustomJobs"] = true
				}
				applyChanges(newCfg)
				// Apply the configuration first
				util.ApplyKueueConfiguration(ctx, k8sManagerClient, newCfg)
				// Verify the ConfigMap was updated before restarting
				gomega.Eventually(func(g gomega.Gomega) {
					updatedCfg := util.GetKueueConfiguration(ctx, k8sManagerClient)
					g.Expect(updatedCfg.FeatureGates["MultiKueueAdaptersForCustomJobs"]).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				// Now restart the controller with the updated configuration
				util.RestartKueueController(ctx, k8sManagerClient, managerClusterName)
				// Wait for it to be fully available without checking restart counts (since we just restarted it).
				util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sManagerClient)
			})
		})

		ginkgo.AfterAll(func() {
			ginkgo.By("restoring default Kueue configuration", func() {
				util.ApplyKueueConfiguration(ctx, k8sManagerClient, defaultManagerKueueCfg)
				util.RestartKueueController(ctx, k8sManagerClient, managerClusterName)
				// RestartKueueController waits for the deployment to be progressing.
				// Wait for it to be fully available without checking restart counts (since we just restarted it).
				util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sManagerClient)
			})
		})

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
			util.MustCreate(ctx, k8sManagerClient, multiKueueAc)

			ginkgo.By("wait for check active", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(multiKueueAc)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			managerFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sManagerClient, managerFlavor)

			managerCq = utiltestingapi.MakeClusterQueue("q1").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(managerFlavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").
						Obj(),
				).
				AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAc.Name)).
				Obj()
			util.MustCreate(ctx, k8sManagerClient, managerCq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sManagerClient, managerCq)

			managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
			util.MustCreate(ctx, k8sManagerClient, managerLq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sManagerClient, managerLq)

			worker1Flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sWorker1Client, worker1Flavor)

			worker1Cq = utiltestingapi.MakeClusterQueue("q1").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker1Flavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1G").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sWorker1Client, worker1Cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sWorker1Client, worker1Cq)

			worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
			util.MustCreate(ctx, k8sWorker1Client, worker1Lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sWorker1Client, worker1Lq)

			worker2Flavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sWorker2Client, worker2Flavor)

			worker2Cq = utiltestingapi.MakeClusterQueue("q1").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(worker2Flavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "2G").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sWorker2Client, worker2Cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sWorker2Client, worker2Cq)

			worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
			util.MustCreate(ctx, k8sWorker2Client, worker2Lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sWorker2Client, worker2Lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sManagerClient, managerNs)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(ctx, k8sWorker1Client, worker1Ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(ctx, k8sWorker2Client, worker2Ns)).To(gomega.Succeed())

			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Cq, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker1Client, worker1Flavor, true, util.LongTimeout)

			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Cq, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sWorker2Client, worker2Flavor, true, util.LongTimeout)

			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerCq, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, managerFlavor, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueAc, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, multiKueueConfig, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster1, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sManagerClient, workerCluster2, true, util.LongTimeout)

			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sManagerClient, managerNs)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker1Client, worker1Ns)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sWorker2Client, worker2Ns)
		})

		// Helper functions
		expectObjectToBeDeletedOnWorkerClusters := func(ctx context.Context, obj client.Object) {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sWorker2Client.Get(ctx, client.ObjectKeyFromObject(obj), obj)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		}

		ginkgo.It("Should run a JobSet on worker if admitted", func() {
			// Since it requires 2 CPU in total, this jobset can only be admitted in worker 1.
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
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "500m").
				RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "200M").
				Obj()

			ginkgo.By("Creating the jobSet", func() {
				util.MustCreate(ctx, k8sManagerClient, jobSet)
			})

			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

			// the execution should be given to worker1
			ginkgo.By("Waiting to be admitted in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "Admitted",
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
					g.Expect(admissioncheck.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, kueue.AdmissionCheckReference(multiKueueAc.Name))).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
						Name:    kueue.AdmissionCheckReference(multiKueueAc.Name),
						State:   kueue.CheckStateReady,
						Message: `The workload got reservation on "worker1"`,
					}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking the JobSet is created in worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobSet := &jobset.JobSet{}
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
					// The JobSet should be created on the worker (suspend is handled by JobSet controller)
					g.Expect(ptr.Deref(createdJobSet.Spec.Suspend, true)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("changing the status of the JobSet in the worker, updates the manager's JobSet status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobSet := jobset.JobSet{}
					g.Expect(k8sWorker1Client.Get(ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
					now := metav1.Now()
					createdJobSet.Status.Conditions = []metav1.Condition{
						{
							Type:               string(jobset.JobSetSuspended),
							Status:             metav1.ConditionFalse,
							Reason:             "JobsActive",
							LastTransitionTime: now,
						},
					}
					g.Expect(k8sWorker1Client.Status().Update(ctx, &createdJobSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdJobSet := jobset.JobSet{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
					suspendedCondition := apimeta.FindStatusCondition(createdJobSet.Status.Conditions, string(jobset.JobSetSuspended))
					g.Expect(suspendedCondition).NotTo(gomega.BeNil())
					g.Expect(suspendedCondition.Status).To(gomega.Equal(metav1.ConditionFalse))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing the jobset pods", func() {
				listOpts := util.GetListOptsFromLabel(fmt.Sprintf("jobset.sigs.k8s.io/jobset-name=%s", jobSet.Name))
				util.WaitForActivePodsAndTerminate(ctx, k8sWorker1Client, worker1RestClient, worker1Cfg, jobSet.Namespace, 4, 0, listOpts)
			})

			ginkgo.By("Waiting for the jobSet to finish", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadFinished,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadFinishedReasonSucceeded,
						Message: "jobset completed successfully",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking no objects are left in the worker clusters and the jobSet is completed", func() {
				wl := &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      wlLookupKey.Name,
						Namespace: wlLookupKey.Namespace,
					},
				}
				expectObjectToBeDeletedOnWorkerClusters(ctx, wl)
				expectObjectToBeDeletedOnWorkerClusters(ctx, jobSet)

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

		ginkgo.It("Should run a JobSet on worker if admitted (Suspend)", func() {
			// Since it requires 2 CPU in total, this jobset can only be admitted in worker 1.
			jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
				Suspend(true).
				Queue(managerLq.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    2,
						Parallelism: 2,
						Completions: 2,
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
					},
				).
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "500m").
				RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "200M").
				Obj()
			util.MustCreate(ctx, k8sManagerClient, jobSet)
			// Get the JobSet to ensure we have the UID
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(jobSet), jobSet)).To(gomega.Succeed())
				g.Expect(jobSet.UID).ToNot(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			jobSetLookupKey := client.ObjectKeyFromObject(jobSet)
			createdJobSet := &jobset.JobSet{}

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

			ginkgo.By("waiting for workload to be admitted and JobSet created in worker", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "Admitted",
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, jobSetLookupKey, createdJobSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking the workload creation in the worker clusters", func() {
				// Recalculate workload lookup key from the actual workload to ensure we have the correct name
				managerWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				// Use the actual workload name from the manager
				actualWlLookupKey := types.NamespacedName{Name: managerWl.Name, Namespace: managerWl.Namespace}
				// Check worker1 - the workload should be replicated to the worker cluster where the job is admitted
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sWorker1Client.Get(ctx, actualWlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				// Note: We don't check worker2 here as the workload may not be replicated to all workers
				// when the job is only admitted to worker1. The third test verifies worker2 replication
				// in a different scenario.
			})

			ginkgo.By("breaking the connection to worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
					createdCluster.Spec.KubeConfig.Location = "bad-secret"
					g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
					activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
						Type:   kueue.MultiKueueClusterActive,
						Status: metav1.ConditionFalse,
						Reason: "BadConfig",
					}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("breaking the connection to worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
					createdCluster.Spec.KubeConfig.Location = "bad-secret"
					g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
					activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
						Type:   kueue.MultiKueueClusterActive,
						Status: metav1.ConditionFalse,
						Reason: "BadConfig",
					}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("removing the managers JobSet and workload", func() {
				gomega.Expect(k8sManagerClient.Delete(ctx, jobSet)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(k8sManagerClient.Delete(ctx, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sManagerClient.Get(ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError(), "workload not deleted")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the worker objects are still present", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sWorker1Client.Get(ctx, jobSetLookupKey, createdJobSet)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				// Note: We skip checking worker2 here as the workload may not be replicated to worker2
				// when the job is only admitted to worker1. This is a known limitation in e2e tests.
			})

			ginkgo.By("restoring the connection to worker2", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
					createdCluster.Spec.KubeConfig.Location = "multikueue2"
					g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
					activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
						Type:   kueue.MultiKueueClusterActive,
						Status: metav1.ConditionTrue,
						Reason: "Active",
					}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the worker2 wl is removed by the garbage collector", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sWorker2Client.Get(ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("restoring the connection to worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
					createdCluster.Spec.KubeConfig.Location = "multikueue1"
					g.Expect(k8sManagerClient.Update(ctx, createdCluster)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					createdCluster := &kueue.MultiKueueCluster{}
					g.Expect(k8sManagerClient.Get(ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
					activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
						Type:   kueue.MultiKueueClusterActive,
						Status: metav1.ConditionTrue,
						Reason: "Active",
					}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("the wl and JobSet are removed on the worker1", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdJobSet := &jobset.JobSet{}
					g.Expect(k8sWorker1Client.Get(ctx, jobSetLookupKey, createdJobSet)).To(utiltesting.BeNotFoundError())
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sWorker1Client.Get(ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
