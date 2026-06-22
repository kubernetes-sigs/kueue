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

package baseline

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LocalQueue metrics", ginkgo.Label("feature:localqueuemetrics", util.Shard1), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.Metrics.LocalQueueMetrics = &configapi.LocalQueueMetrics{
				Enable: true,
				LocalQueueSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"metrics-test": "true",
					},
				},
			}
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-customconfig-lq-metrics-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("test-flavor-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "lq-metrics-reader-rolebinding-" + ns.Name},
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

		curlPod = testingjobspod.MakePod("curl-metrics-"+ns.Name, kueueNS).
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

	ginkgo.When("workload is admitted to a lq with matching labels", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			workload     *kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = utiltestingapi.MakeClusterQueue("").
				GeneratedName("test-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-").
				ClusterQueue(clusterQueue.Name).
				Label("metrics-test", "true").
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

			workload = utiltestingapi.MakeWorkload("test-workload", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 1).Obj(),
				).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, workload)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("should ensure the localqueue metrics are available", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

			metrics := [][]string{
				{"kueue_local_queue_pending_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_reserving_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_workloads_total", ns.Name, localQueue.Name, ""},
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
				{"kueue_local_queue_reserving_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_active_workloads", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that metrics that should have been deleted are no longer available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, deletedMetrics)
			})

			notDeletedMetrics := [][]string{
				// Cleared metrics with 0 value
				{"kueue_local_queue_pending_workloads", "active", "0", ns.Name, localQueue.Name},
				{"kueue_local_queue_pending_workloads", "inadmissible", "0", ns.Name, localQueue.Name},

				// LocalQueue counters persist until the LocalQueue itself is
				// deleted, and the status metric is re-reported with reason
				// ClusterQueueDoesNotExist.
				{"kueue_local_queue_quota_reserved_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_workloads_total", ns.Name, localQueue.Name, ""},
				{"kueue_local_queue_admission_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_status", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that metrics that should not have been deleted are still available", func() {
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, notDeletedMetrics)
			})

			ginkgo.By("deleting the local queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			})

			// kueue_local_queue_status is not asserted here: its cleanup is
			// skipped when the ClusterQueue was already removed from the cache.
			deletedLocalQueueMetrics := [][]string{
				{"kueue_local_queue_pending_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_workloads_total", ns.Name, localQueue.Name, ""},
				{"kueue_local_queue_admission_wait_time_seconds", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that LocalQueue metrics are no longer available after deleting the local queue", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, deletedLocalQueueMetrics)
			})
		})
	})

	ginkgo.When("workload is admitted to a lq without labels", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			workload     *kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = utiltestingapi.MakeClusterQueue("").
				GeneratedName("test-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

			workload = utiltestingapi.MakeWorkload("test-workload", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(
					*utiltestingapi.MakePodSet("ps1", 1).Obj(),
				).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, workload)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("should ensure the localqueue metrics are not available", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

			metrics := [][]string{
				{"kueue_local_queue_pending_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_reserving_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_active_workloads", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_workloads_total", ns.Name, localQueue.Name},
				{"kueue_local_queue_quota_reserved_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_admitted_workloads_total", ns.Name, localQueue.Name, ""},
				{"kueue_local_queue_admission_wait_time_seconds", ns.Name, localQueue.Name},
				{"kueue_local_queue_status", ns.Name, localQueue.Name},
			}

			ginkgo.By("checking that default metrics are not available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, metrics)
			})
		})
	})
})

var _ = ginkgo.Describe("WorkloadMetrics", ginkgo.Label("feature:workloadmetrics"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding
		curlContainerName               string
		curlPod                         *corev1.Pod
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			if cfg.FeatureGates == nil {
				cfg.FeatureGates = make(map[string]bool)
			}
			cfg.FeatureGates[string(features.WorkloadMetrics)] = true
		})
	})

	ginkgo.AfterAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-workload-metrics-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("test-flavor-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)

		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "wm-metrics-reader-rolebinding-" + ns.Name},
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

		curlPod = testingjobspod.MakePod("curl-metrics-"+ns.Name, kueueNS).
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

	ginkgo.When("workload is preempted", func() {
		var (
			clusterQueue1 *kueue.ClusterQueue
			clusterQueue2 *kueue.ClusterQueue
			localQueue1   *kueue.LocalQueue
			localQueue2   *kueue.LocalQueue

			highPriorityClass *schedulingv1.PriorityClass

			lowerJob1          *batchv1.Job
			lowerJob2          *batchv1.Job
			blockerJob         *batchv1.Job
			blockerWorkload    *kueue.Workload
			blockerWorkloadKey types.NamespacedName
			higherJob1         *batchv1.Job
			higherJob2         *batchv1.Job
		)

		ginkgo.BeforeEach(func() {
			clusterQueue1 = utiltestingapi.MakeClusterQueue("").
				GeneratedName("test-cq-1-").
				Cohort("test-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "2").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			clusterQueue2 = utiltestingapi.MakeClusterQueue("").
				GeneratedName("test-cq-2-").
				Cohort("test-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "3").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue1, clusterQueue2)

			localQueue1 = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-1-").
				ClusterQueue(clusterQueue1.Name).
				Obj()
			localQueue2 = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-2-").
				ClusterQueue(clusterQueue2.Name).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue1, localQueue2)

			highPriorityClass = utiltesting.MakePriorityClass("high-" + ns.Name).PriorityValue(100).Obj()
			util.MustCreate(ctx, k8sClient, highPriorityClass)

			lowerJob1 = testingjob.MakeJob("lower-job-1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue1.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, lowerJob1)

			lowerJob2 = testingjob.MakeJob("lower-job-2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue2.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, lowerJob2)

			blockerJob = testingjob.MakeJob("blocker", ns.Name).
				Queue(kueue.LocalQueueName(localQueue2.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				PriorityClass(highPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "3").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, blockerJob)

			blockerWLName := job.GetWorkloadNameForJob(blockerJob.Name, blockerJob.UID)
			blockerWorkloadKey = types.NamespacedName{Name: blockerWLName, Namespace: ns.Name}
			blockerWorkload = &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, blockerWorkloadKey, blockerWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blockerWorkload)

			higherJob1 = testingjob.MakeJob("high-large-1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue1.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				PriorityClass(highPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "4").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, higherJob1)

			higherJob2 = testingjob.MakeJob("high-large-2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue2.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				PriorityClass(highPriorityClass.Name).
				RequestAndLimit(corev1.ResourceCPU, "4").
				TerminationGracePeriod(1).
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

		ginkgo.It("should emit kueue_workload_preemptions_total", func() {
			higherWorkload1 := &kueue.Workload{}
			higherWorkload2 := &kueue.Workload{}

			ginkgo.By("Wait for higher-priority workloads to be pending (inadmissible)", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{
						Name:      job.GetWorkloadNameForJob(higherJob1.Name, higherJob1.UID),
						Namespace: ns.Name,
					}, higherWorkload1)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{
						Name:      job.GetWorkloadNameForJob(higherJob2.Name, higherJob2.UID),
						Namespace: ns.Name,
					}, higherWorkload2)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectWorkloadsToBePending(ctx, k8sClient, higherWorkload1, higherWorkload2)
			})

			ginkgo.By("Deactivate the blocker workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, blockerWorkloadKey, blockerWorkload)).To(gomega.Succeed())
					blockerWorkload.Spec.Active = new(false)
					g.Expect(k8sClient.Update(ctx, blockerWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Expecting at least one of the high-priority jobs to be admitted", func() {
				util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1,
					utiltestingapi.MakeWorkload(
						job.GetWorkloadNameForJob(higherJob1.Name, higherJob1.UID),
						higherJob1.Namespace,
					).Obj(),
					utiltestingapi.MakeWorkload(
						job.GetWorkloadNameForJob(higherJob2.Name, higherJob2.UID),
						higherJob2.Namespace,
					).Obj(),
				)
			})

			ginkgo.By("checking that kueue_workload_preemptions_total is available", func() {
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, [][]string{
					{"kueue_workload_preemptions_total", ns.Name},
				})
			})
		})
	})
})
