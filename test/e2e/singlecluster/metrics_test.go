/*
Copyright 2024 The Kubernetes Authors.

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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegaformat "github.com/onsi/gomega/format"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
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
	metricsServiceName           = "kueue-controller-manager-metrics-service"
)

var _ = ginkgo.Describe("Metrics", func() {
	var (
		defaultGomegaMaxLength int

		ns *corev1.Namespace

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	testClock := clock.RealClock{}

	ginkgo.BeforeEach(func() {
		defaultGomegaMaxLength = gomegaformat.MaxLength
		gomegaformat.MaxLength = 0

		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-metrics-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader-rolebinding"},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      serviceAccountName,
					Namespace: config.DefaultNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     metricsReaderClusterRoleName,
			},
		}
		gomega.Expect(k8sClient.Create(ctx, metricsReaderClusterRoleBinding)).Should(gomega.Succeed())

		curlPod = testingjobspod.MakePod("curl-metrics", config.DefaultNamespace).
			ServiceAccountName(serviceAccountName).
			Image(util.E2eTTestCurlImage, []string{
				"sleep", "5m",
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, curlPod)).Should(gomega.Succeed())

		ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(curlPod), createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))

				curlContainerName = createdPod.Spec.Containers[0].Name
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.AfterEach(func() {
		gomegaformat.MaxLength = defaultGomegaMaxLength

		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.LongTimeout)
	})

	ginkgo.It("should ensure the default metrics are available upon workload creation", func() {
		var (
			resourceFlavor *v1beta1.ResourceFlavor
			clusterQueue   *v1beta1.ClusterQueue
			localQueue     *v1beta1.LocalQueue
			workload       *v1beta1.Workload
		)

		ginkgo.By("creating resource flavor", func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("test-flavor").Obj()

			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the resource flavor", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			})
		})

		ginkgo.By("Creating a cluster queue", func() {
			clusterQueue = utiltesting.MakeClusterQueue("test-cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			})
		})

		ginkgo.By("Creating a local queue", func() {
			localQueue = utiltesting.MakeLocalQueue("test-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the local queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			})
		})

		ginkgo.By("Creating a workload", func() {
			workload = utiltesting.MakeWorkload("test-workload", ns.Name).
				Queue(localQueue.Name).
				PodSets(
					*utiltesting.MakePodSet("ps1", 1).Obj(),
				).
				Request(corev1.ResourceCPU, "1").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, workload)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the workload", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
			})
		})

		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

		metrics := []string{
			"controller_runtime_reconcile_total",

			"kueue_admission_attempts_total",
			"kueue_admission_attempt_duration_seconds",
			"kueue_pending_workloads",
			"kueue_reserving_active_workloads",
			"kueue_admitted_active_workloads",
			"kueue_quota_reserved_workloads_total",
			"kueue_quota_reserved_wait_time_seconds",
			"kueue_admitted_workloads_total",
			"kueue_admission_wait_time_seconds",
			"kueue_cluster_queue_resource_usage",
			"kueue_cluster_queue_status",
			"kueue_cluster_queue_resource_reservation",
			"kueue_cluster_queue_nominal_quota",
			"kueue_cluster_queue_borrowing_limit",
			"kueue_cluster_queue_lending_limit",
			"kueue_cluster_queue_weighted_share",

			// LocalQueueMetrics
			"kueue_local_queue_pending_workloads",
			"kueue_local_queue_reserving_active_workloads",
			"kueue_local_queue_admitted_active_workloads",
			"kueue_local_queue_quota_reserved_workloads_total",
			"kueue_local_queue_quota_reserved_wait_time_seconds",
			"kueue_local_queue_admitted_workloads_total",
			"kueue_local_queue_admission_wait_time_seconds",
			"kueue_local_queue_status",
		}

		ginkgo.By("Getting the metrics by checking curl-metrics logs", func() {
			checkMetricsAvailability(curlPod.Name, curlContainerName, metrics)
		})
	})

	ginkgo.It("should ensure the admission check metrics are available", func() {
		var (
			resourceFlavor  *v1beta1.ResourceFlavor
			admissionCheck  *v1beta1.AdmissionCheck
			clusterQueue    *v1beta1.ClusterQueue
			localQueue      *v1beta1.LocalQueue
			createdJob      *batchv1.Job
			workloadKey     types.NamespacedName
			createdWorkload *v1beta1.Workload
		)

		ginkgo.By("creating an admission check", func() {
			admissionCheck = utiltesting.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()

			gomega.Expect(k8sClient.Create(ctx, admissionCheck)).Should(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the admission check", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
			})
		})

		util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

		ginkgo.By("creating resource flavor", func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("test-flavor").Obj()

			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the resource flavor", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			})
		})

		ginkgo.By("Creating an admission checked cluster queue", func() {
			clusterQueue = utiltesting.MakeClusterQueue("test-admission-check-cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				AdmissionChecks(admissionCheck.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			})
		})

		ginkgo.By("Creating an admission checked local queue", func() {
			localQueue = utiltesting.MakeLocalQueue("test-admission-checked-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the local queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			})
		})

		ginkgo.By("Creating an admission checked job", func() {
			createdJob = testingjob.MakeJob("admission-checked-job", ns.Name).
				Queue(localQueue.Name).
				Request("cpu", "1").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, createdJob)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the admission checked job", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, createdJob, true)
			})
		})

		admissionCheckedJobWLName := job.GetWorkloadNameForJob(createdJob.Name, createdJob.UID)
		workloadKey = types.NamespacedName{
			Name:      admissionCheckedJobWLName,
			Namespace: ns.Name,
		}

		createdWorkload = &v1beta1.Workload{}

		ginkgo.By("waiting for the workload to be assigned", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, workloadKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).
					Should(utiltesting.HaveConditionStatusTrue(v1beta1.WorkloadQuotaReserved))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting the check as successful", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, workloadKey, createdWorkload)).Should(gomega.Succeed())
				patch := workload.BaseSSAWorkload(createdWorkload)
				workload.SetAdmissionCheckState(&patch.Status.AdmissionChecks, v1beta1.AdmissionCheckState{
					Name:  admissionCheck.Name,
					State: v1beta1.CheckStateReady,
				}, testClock)
				g.Expect(k8sClient.Status().
					Patch(ctx, patch, client.Apply, client.FieldOwner("test-admission-check-controller"), client.ForceOwnership)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		metrics := []string{
			"admission_checks_wait_time_seconds",

			"local_queue_admission_checks_wait_time_seconds",
		}

		ginkgo.By("Getting the metrics by checking curl-metrics logs", func() {
			checkMetricsAvailability(curlPod.Name, curlContainerName, metrics)
		})
	})

	ginkgo.It("should ensure the eviction and preemption metrics are available", func() {
		var (
			resourceFlavor    *v1beta1.ResourceFlavor
			clusterQueue      *v1beta1.ClusterQueue
			localQueue        *v1beta1.LocalQueue
			lowPriorityClass  *schedulingv1.PriorityClass
			highPriorityClass *schedulingv1.PriorityClass
			lowerJob          *batchv1.Job
			higherJob         *batchv1.Job
			lowerWorkloadKey  types.NamespacedName
			higherWorkloadKey types.NamespacedName
			lowerWorkload     *v1beta1.Workload
			higherWorkload    *v1beta1.Workload
		)

		ginkgo.By("creating resource flavor", func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("test-flavor").Obj()

			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the resource flavor", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			})
		})

		ginkgo.By("Creating a cluster queue", func() {
			clusterQueue = utiltesting.MakeClusterQueue("test-cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(v1beta1.ClusterQueuePreemption{
					WithinClusterQueue: v1beta1.PreemptionPolicyLowerPriority,
				}).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			})
		})

		ginkgo.By("Creating a local queue", func() {
			localQueue = utiltesting.MakeLocalQueue("test-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the local queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			})
		})

		ginkgo.By("Creating a low priority class", func() {
			lowPriorityClass = utiltesting.MakePriorityClass("low").PriorityValue(1).Obj()

			gomega.Expect(k8sClient.Create(ctx, lowPriorityClass)).Should(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, lowPriorityClass)).To(gomega.Succeed())
		})

		ginkgo.By("Creating a low priority job", func() {
			lowerJob = testingjob.MakeJob("lower-job", ns.Name).
				Queue(localQueue.Name).
				Request("cpu", "1").
				PriorityClass(lowPriorityClass.Name).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, lowerJob)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the low priority job", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lowerJob, true)
			})
		})

		lowerWLName := job.GetWorkloadNameForJob(lowerJob.Name, lowerJob.UID)
		lowerWorkloadKey = types.NamespacedName{
			Name:      lowerWLName,
			Namespace: ns.Name,
		}

		lowerWorkload = &v1beta1.Workload{}

		ginkgo.By("Workload is admitted using the first flavor", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowerWorkloadKey, lowerWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowerWorkload)
		})

		ginkgo.By("Creating a high priority class", func() {
			highPriorityClass = utiltesting.MakePriorityClass("high").PriorityValue(100).Obj()

			gomega.Expect(k8sClient.Create(ctx, highPriorityClass)).Should(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
		})

		ginkgo.By("Workload is preempted by higher priority job", func() {
			higherJob = testingjob.MakeJob("high", ns.Name).
				Queue(localQueue.Name).
				PriorityClass("high").
				Request(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, higherJob)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, lowerWorkload)

			higherWLName := job.GetWorkloadNameForJob(higherJob.Name, higherJob.UID)

			higherWorkloadKey = types.NamespacedName{
				Name:      higherWLName,
				Namespace: ns.Name,
			}

			higherWorkload = &v1beta1.Workload{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, higherWorkloadKey, higherWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, higherWorkload)
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the higher priority workload", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, higherWorkload, true)
			})
		})

		ginkgo.By("Deactivate the higher priority workload", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, higherWorkloadKey, higherWorkload)).To(gomega.Succeed())

				higherWorkload.Spec.Active = ptr.To(false)

				g.Expect(k8sClient.Update(ctx, higherWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, higherWorkloadKey, higherWorkload)).To(gomega.Succeed())

			g.Expect(higherWorkload.Status.Conditions).To(
				gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    v1beta1.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "Deactivated",
						Message: "The workload is deactivated",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				),
			)
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		metrics := []string{
			"kueue_evicted_workloads_total",
			"kueue_preempted_workloads_total",

			"kueue_local_queue_evicted_workloads_total",
			"kueue_local_queue_resource_reservation",
			"kueue_local_queue_resource_usage",
		}

		ginkgo.By("Getting the metrics by checking curl-metrics logs", func() {
			checkMetricsAvailability(curlPod.Name, curlContainerName, metrics)
		})
	})
})

func checkMetricsAvailability(curlPodName, curlContainerName string, metrics []string) {
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, _, err := util.KExecute(ctx, cfg, restClient, config.DefaultNamespace, curlPodName, curlContainerName,
			[]string{
				"/bin/sh", "-c",
				fmt.Sprintf(
					"curl -s -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics ",
					metricsServiceName, config.DefaultNamespace,
				),
			})
		g.Expect(err).NotTo(gomega.HaveOccurred())
		for _, metric := range metrics {
			g.Expect(string(metricsOutput)).To(gomega.ContainSubstring(metric))
		}
	}, util.Timeout).Should(gomega.Succeed())
}
