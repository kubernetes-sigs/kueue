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
	"k8s.io/apimachinery/pkg/types"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("QuotaCheckStrategy", ginkgo.Label("feature:quotacheckstrategy", behavioral.Shard0), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		behavioral.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			if cfg.FeatureGates == nil {
				cfg.FeatureGates = make(map[string]bool)
			}
			cfg.Resources = &configapi.Resources{
				QuotaCheckStrategy: new(configapi.QuotaCheckIgnoreUndeclared),
			}
			cfg.Metrics = configapi.ControllerMetrics{
				EnableClusterQueueResources: true,
			}
		})

		resourceFlavor = utiltestingapi.MakeResourceFlavor("test-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("").
			GeneratedName("test-cq-ignoreundeclared-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
					Resource(corev1.ResourceCPU, "100").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
	})

	ginkgo.AfterAll(func() {
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, resourceFlavor, true, behavioral.MediumTimeout)
	})

	ginkgo.When("Metrics is available", func() {
		var (
			localQueue                      *kueue.LocalQueue
			createdJob                      *batchv1.Job
			curlContainerName               string
			curlPod                         *corev1.Pod
			metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding
		)

		ginkgo.BeforeEach(func() {
			ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-quota-check-strategy-")
			metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
				Name: "metrics-reader-rolebinding-" + ns.Name,
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
			behavioral.MustCreate(ctx, k8sClient, metricsReaderClusterRoleBinding)

			curlPod = testingjobspod.MakePod("curl-metrics-"+ns.Name, kueueNS).
				ServiceAccountName(serviceAccountName).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, curlPod)

			ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
				behavioral.WaitForPodRunning(ctx, k8sClient, curlPod)
			})

			localQueue = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-ignoreundeclared-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, behavioral.LongTimeout)
			behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("should not report metrics for resource usage for undeclared resources", func() {
			ginkgo.By("Create a job with undeclared resource", func() {
				createdJob = testingjob.MakeJob("ignoreundeclared-job", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
					TerminationGracePeriod(1).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "1Gi").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, createdJob)
				createdJobWLName := job.GetWorkloadNameForJob(createdJob.Name, createdJob.UID)
				workloadKey := types.NamespacedName{
					Name:      createdJobWLName,
					Namespace: ns.Name,
				}
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, workloadKey)
			})

			availableMetrics := [][]string{
				{"kueue_cluster_queue_resource_usage", clusterQueue.Name, "cpu"},
			}
			curlContainerName = curlPod.Spec.Containers[0].Name
			ginkgo.By("checking that resource usage metrics for declared resources are available", func() {
				behavioral.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, availableMetrics)
			})

			unavailableMetrics := [][]string{
				{"kueue_cluster_queue_resource_usage", clusterQueue.Name, "memory"},
			}
			ginkgo.By("checking that resource usage metrics for undeclared resources are not available", func() {
				behavioral.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, unavailableMetrics)
			})
		})
	})

	ginkgo.When("Preemption is enabled", func() {
		var (
			localQueue *kueue.LocalQueue
			highWPC    *kueue.WorkloadPriorityClass
			lowWPC     *kueue.WorkloadPriorityClass
			lowJobKey  types.NamespacedName
		)

		ginkgo.BeforeEach(func() {
			ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-preemption-")

			localQueue = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-preemption-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, highWPC, true, behavioral.MediumTimeout)
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, lowWPC, true, behavioral.MediumTimeout)
		})

		ginkgo.It("should preempt workloads with undeclared resource that have lower priority", func() {
			lowWPC = utiltestingapi.MakeWorkloadPriorityClass("low").PriorityValue(10).Obj()
			highWPC = utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj()
			behavioral.MustCreate(ctx, k8sClient, lowWPC)
			behavioral.MustCreate(ctx, k8sClient, highWPC)

			ginkgo.By("Create a low priority workload with undeclared resource", func() {
				lowJob := testingjob.MakeJob("low-job", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					WorkloadPriorityClass("low").
					RequestAndLimit(corev1.ResourceCPU, "100").
					RequestAndLimit(corev1.ResourceMemory, "1Gi").
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
					TerminationGracePeriod(1).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, lowJob)
				lowJobKey = types.NamespacedName{Name: job.GetWorkloadNameForJob(lowJob.Name, lowJob.UID), Namespace: ns.Name}
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, lowJobKey)
			})

			ginkgo.By("Create a high priority workload with undeclared resource", func() {
				highJob := testingjob.MakeJob("high-job", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					WorkloadPriorityClass("high").
					RequestAndLimit(corev1.ResourceCPU, "100").
					RequestAndLimit(corev1.ResourceMemory, "1Gi").
					Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
					TerminationGracePeriod(1).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, highJob)
				highJobKey := types.NamespacedName{Name: job.GetWorkloadNameForJob(highJob.Name, highJob.UID), Namespace: ns.Name}
				behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, highJobKey)
			})

			ginkgo.By("Check the low priority workload is preempted", func() {
				getworkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, lowJobKey, getworkload)).To(gomega.Succeed())
					g.Expect(getworkload.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadAdmitted))
					g.Expect(getworkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPreempted))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})
