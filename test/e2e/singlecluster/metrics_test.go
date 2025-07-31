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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

const (
	serviceAccountName           = "kueue-controller-manager"
	metricsReaderClusterRoleName = "kueue-metrics-reader"
)

var _ = ginkgo.Describe("Metrics", func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *v1beta1.ResourceFlavor

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-metrics-")

		resourceFlavor = utiltesting.MakeResourceFlavor("test-flavor").Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader-rolebinding"},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      serviceAccountName,
					Namespace: kueueNS,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     metricsReaderClusterRoleName,
			},
		}
		util.MustCreate(ctx, k8sClient, metricsReaderClusterRoleBinding)

		curlPod = testingjobspod.MakePod("curl-metrics", kueueNS).
			ServiceAccountName(serviceAccountName).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, curlPod)

		ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
			util.WaitForPodRunning(ctx, k8sClient, curlPod)
		})

		curlContainerName = curlPod.Spec.Containers[0].Name
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.LongTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("workload is admitted", func() {
		var (
			clusterQueue *v1beta1.ClusterQueue
			localQueue   *v1beta1.LocalQueue
			workload     *v1beta1.Workload
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = utiltesting.MakeClusterQueue("").
				GeneratedName("test-cq-").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			localQueue = utiltesting.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, localQueue)

			workload = utiltesting.MakeWorkload("test-workload", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue.Name)).
				PodSets(
					*utiltesting.MakePodSet("ps1", 1).Obj(),
				).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, workload)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("should ensure the default metrics are available", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

			metrics := [][]string{
				{"kueue_admission_attempts_total"},
				{"kueue_admission_attempt_duration_seconds"},
				{"kueue_pending_workloads", clusterQueue.Name},
				{"kueue_reserving_active_workloads", clusterQueue.Name},
				{"kueue_admitted_active_workloads", clusterQueue.Name},
				{"kueue_quota_reserved_workloads_total", clusterQueue.Name},
				{"kueue_quota_reserved_wait_time_seconds", clusterQueue.Name},
				{"kueue_admitted_workloads_total", clusterQueue.Name},
				{"kueue_admission_wait_time_seconds", clusterQueue.Name},
				{"kueue_cluster_queue_resource_usage", clusterQueue.Name},
				{"kueue_cluster_queue_status", clusterQueue.Name},
				{"kueue_cluster_queue_resource_reservation", clusterQueue.Name},
				{"kueue_cluster_queue_nominal_quota", clusterQueue.Name},
				{"kueue_cluster_queue_borrowing_limit", clusterQueue.Name},
				{"kueue_cluster_queue_lending_limit", clusterQueue.Name},
				{"kueue_cluster_queue_weighted_share", clusterQueue.Name},

				// LocalQueueMetrics
				{"kueue_local_queue_pending_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_reserving_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_admission_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_status", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that default metrics are available", func() {
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, metrics)
			})

			ginkgo.By("deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			})

			deletedMetrics := [][]string{
				{"kueue_pending_workloads", clusterQueue.Name},
				{"kueue_reserving_active_workloads", clusterQueue.Name},
				{"kueue_admitted_active_workloads", clusterQueue.Name},
				{"kueue_quota_reserved_workloads_total", clusterQueue.Name},
				{"kueue_quota_reserved_wait_time_seconds", clusterQueue.Name},
				{"kueue_admitted_workloads_total", clusterQueue.Name},
				{"kueue_admission_wait_time_seconds", clusterQueue.Name},
				{"kueue_cluster_queue_resource_usage", clusterQueue.Name},
				{"kueue_cluster_queue_status", clusterQueue.Name},
				{"kueue_cluster_queue_resource_reservation", clusterQueue.Name},
				{"kueue_cluster_queue_nominal_quota", clusterQueue.Name},
				{"kueue_cluster_queue_borrowing_limit", clusterQueue.Name},
				{"kueue_cluster_queue_lending_limit", clusterQueue.Name},

				// LocalQueueMetrics
				{"kueue_local_queue_reserving_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_admission_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_status", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that metrics that should have been deleted are no longer available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, deletedMetrics)
			})

			notDeletedMetrics := [][]string{
				{"kueue_admission_attempts_total"},
				{"kueue_admission_attempt_duration_seconds"},
				{"kueue_cluster_queue_weighted_share", clusterQueue.Name},

				// Cleared metrics with 0 value
				{"kueue_local_queue_pending_workloads", "active", "0", ns.Name, localQueue.Name},
				{"kueue_local_queue_pending_workloads", "inadmissible", "0", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that metrics that should not have been deleted are still available", func() {
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, notDeletedMetrics)
			})
		})
	})

	ginkgo.When("workload is admitted with admission checks", func() {
		var (
			admissionCheck  *v1beta1.AdmissionCheck
			clusterQueue    *v1beta1.ClusterQueue
			localQueue      *v1beta1.LocalQueue
			createdJob      *batchv1.Job
			workloadKey     types.NamespacedName
			createdWorkload *v1beta1.Workload
		)

		ginkgo.BeforeEach(func() {
			admissionCheck = utiltesting.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, admissionCheck)

			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

			clusterQueue = utiltesting.MakeClusterQueue("").
				GeneratedName("test-admission-check-cq-").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				AdmissionChecks(v1beta1.AdmissionCheckReference(admissionCheck.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			localQueue = utiltesting.MakeLocalQueue("", ns.Name).
				GeneratedName("test-admission-checked-lq-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, localQueue)

			createdJob = testingjob.MakeJob("admission-checked-job", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, createdJob)

			admissionCheckedJobWLName := job.GetWorkloadNameForJob(createdJob.Name, createdJob.UID)
			workloadKey = types.NamespacedName{
				Name:      admissionCheckedJobWLName,
				Namespace: ns.Name,
			}

			createdWorkload = &v1beta1.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, workloadKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(v1beta1.WorkloadQuotaReserved))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, createdJob, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
		})

		ginkgo.It("should ensure the admission check metrics are available", func() {
			ginkgo.By("setting the check as successful", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, workloadKey, createdWorkload)).Should(gomega.Succeed())
					patch := util.BaseSSAWorkload(createdWorkload)
					workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, v1beta1.AdmissionCheckState{
						Name:  v1beta1.AdmissionCheckReference(admissionCheck.Name),
						State: v1beta1.CheckStateReady,
					}, realClock)
					g.Expect(k8sClient.Status().
						Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)).
						Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			metrics := [][]string{
				{"kueue_admission_checks_wait_time_seconds", clusterQueue.Name},

				{"kueue_local_queue_admission_checks_wait_time_seconds", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that admission check metrics are available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, metrics)
			})

			ginkgo.By("deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, createdJob, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			})

			ginkgo.By("checking that admission check metrics are no longer available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, metrics)
			})
		})
	})

	ginkgo.When("workload is admitted with eviction and preemption", func() {
		var (
			clusterQueue1 *v1beta1.ClusterQueue
			clusterQueue2 *v1beta1.ClusterQueue

			localQueue1 *v1beta1.LocalQueue
			localQueue2 *v1beta1.LocalQueue

			highPriorityClass *schedulingv1.PriorityClass

			lowerJob1         *batchv1.Job
			lowerWorkload1Key types.NamespacedName
			lowerWorkload1    *v1beta1.Workload

			lowerJob2         *batchv1.Job
			lowerWorkload2Key types.NamespacedName
			lowerWorkload2    *v1beta1.Workload

			blockerJob         *batchv1.Job
			blockerWorkloadKey types.NamespacedName
			blockerWorkload    *v1beta1.Workload

			higherJob1 *batchv1.Job
			higherJob2 *batchv1.Job
		)

		ginkgo.BeforeEach(func() {
			clusterQueue1 = utiltesting.MakeClusterQueue("").
				GeneratedName("test-cq-1-").
				Cohort("test-cohort").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Obj(),
				).
				Preemption(v1beta1.ClusterQueuePreemption{
					ReclaimWithinCohort: v1beta1.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  v1beta1.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &v1beta1.BorrowWithinCohort{
						Policy: v1beta1.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue1)

			clusterQueue2 = utiltesting.MakeClusterQueue("").
				GeneratedName("test-cq-2-").
				Cohort("test-cohort").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "3").
						Obj(),
				).
				Preemption(v1beta1.ClusterQueuePreemption{
					ReclaimWithinCohort: v1beta1.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  v1beta1.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &v1beta1.BorrowWithinCohort{
						Policy: v1beta1.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue2)

			localQueue1 = utiltesting.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-1-").
				ClusterQueue(clusterQueue1.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, localQueue1)

			localQueue2 = utiltesting.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-2-").
				ClusterQueue(clusterQueue2.Name).
				Obj()
			util.MustCreate(ctx, k8sClient, localQueue2)

			highPriorityClass = utiltesting.MakePriorityClass("high").PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, highPriorityClass)

			lowerJob1 = testingjob.MakeJob("lower-job-1", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue1.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowerJob1)

			lowerWLName1 := job.GetWorkloadNameForJob(lowerJob1.Name, lowerJob1.UID)
			lowerWorkload1Key = types.NamespacedName{
				Name:      lowerWLName1,
				Namespace: ns.Name,
			}
			lowerWorkload1 = &v1beta1.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowerWorkload1Key, lowerWorkload1)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowerWorkload1)

			lowerJob2 = testingjob.MakeJob("lower-job-2", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue2.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowerJob2)

			lowerWLName2 := job.GetWorkloadNameForJob(lowerJob2.Name, lowerJob2.UID)
			lowerWorkload2Key = types.NamespacedName{
				Name:      lowerWLName2,
				Namespace: ns.Name,
			}
			lowerWorkload2 = &v1beta1.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowerWorkload2Key, lowerWorkload2)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowerWorkload2)

			blockerJob = testingjob.MakeJob("blocker", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue2.Name)).
				PriorityClass(highPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "3").
				Obj()
			util.MustCreate(ctx, k8sClient, blockerJob)

			blockerWLName := job.GetWorkloadNameForJob(blockerJob.Name, blockerJob.UID)
			blockerWorkloadKey = types.NamespacedName{
				Name:      blockerWLName,
				Namespace: ns.Name,
			}
			blockerWorkload = &v1beta1.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, blockerWorkloadKey, blockerWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blockerWorkload)

			higherJob1 = testingjob.MakeJob("high-large-1", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue1.Name)).
				PriorityClass(highPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, higherJob1)

			higherJob2 = testingjob.MakeJob("high-large-2", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue2.Name)).
				PriorityClass(highPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, higherJob2)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, higherJob1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, higherJob2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, blockerJob, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowerJob1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lowerJob2, true)
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue2, true)
		})

		ginkgo.It("should ensure the eviction and preemption metrics are available", func() {
			ginkgo.By("Deactivate the blocker workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, blockerWorkloadKey, blockerWorkload)).To(gomega.Succeed())

					blockerWorkload.Spec.Active = ptr.To(false)

					g.Expect(k8sClient.Update(ctx, blockerWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, blockerWorkloadKey, blockerWorkload)).To(gomega.Succeed())
				g.Expect(blockerWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(v1beta1.WorkloadEvicted, v1beta1.WorkloadDeactivated))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Expecting at least one of the high-priority jobs to be admitted", func() {
				util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1,
					utiltesting.MakeWorkload(
						job.GetWorkloadNameForJob(higherJob1.Name, higherJob1.UID),
						higherJob1.Namespace,
					).Obj(),
					utiltesting.MakeWorkload(
						job.GetWorkloadNameForJob(higherJob2.Name, higherJob2.UID),
						higherJob2.Namespace,
					).Obj(),
				)
			})

			metrics := [][]string{
				{"kueue_admission_cycle_preemption_skips"},
				{"kueue_evicted_workloads_total"},
				{"kueue_evicted_workloads_once_total"},
				{"kueue_preempted_workloads_total"},

				{"kueue_local_queue_evicted_workloads_total"},
				{"kueue_local_queue_resource_reservation"},
				{"kueue_local_queue_resource_usage"},
			}

			ginkgo.By("checking that eviction and preemption metrics are available", func() {
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, metrics)
			})

			ginkgo.By("delete the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, higherJob1, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, higherJob2, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, blockerJob, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lowerJob1, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lowerJob2, true)
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue1, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue2, true)
			})

			ginkgo.By("checking that eviction and preemption metrics are no longer available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, metrics)
			})
		})
	})
})
