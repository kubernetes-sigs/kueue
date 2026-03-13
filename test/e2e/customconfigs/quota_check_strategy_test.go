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

package customconfigse2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("QuotaCheckStrategy", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
		originalKueueCfg  *configapi.Configuration
		createdWorkload   *kueue.Workload
	)

	ginkgo.BeforeEach(func() {
		originalKueueCfg = util.GetKueueConfiguration(ctx, k8sClient)
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, originalKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{string(features.QuotaCheckStrategy): true}
			cfg.Resources = &configapi.Resources{
				QuotaCheckStrategy: ptr.To(configapi.QuotaCheckIgnoreUndeclared),
			}
			cfg.ControllerManager.Metrics = configapi.ControllerMetrics{
				EnableClusterQueueResources: true,
			}
		})
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-quota-check-strategy-")
	})

	ginkgo.AfterEach(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, originalKueueCfg, kindClusterName)

	})

	ginkgo.When("quotaCheckStrategy is set to IgnoreUndeclared", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			createdJob   *batchv1.Job
		)

		ginkgo.BeforeEach(func() {
			resourceFlavor = utiltestingapi.MakeResourceFlavor("test-flavor-" + ns.Name).Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader-rolebinding-" + ns.Name},
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

			clusterQueue = utiltestingapi.MakeClusterQueue("").
				GeneratedName("test-cq-ignoreundeclared-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-ignoreundeclared-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

			createdJob = testingjob.MakeJob("ignoreundeclared-job", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "1Gi").
				Obj()
			util.MustCreate(ctx, k8sClient, createdJob)

		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, createdJob, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, createdWorkload, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.MediumTimeout)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)

		})

		ginkgo.It("should not report resource usage for undeclared resources", func() {
			createdJobWLName := job.GetWorkloadNameForJob(createdJob.Name, createdJob.UID)
			workloadKey := types.NamespacedName{
				Name:      createdJobWLName,
				Namespace: ns.Name,
			}
			createdWorkload = &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, workloadKey, createdWorkload)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)

			availableMetrics := [][]string{
				{"kueue_cluster_queue_resource_usage", clusterQueue.Name, "cpu"},
			}

			ginkgo.By("checking that resource usage metrics for declared resources are available", func() {
				util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, availableMetrics)
			})

			unavailableMetrics := [][]string{
				{"kueue_cluster_queue_resource_usage", clusterQueue.Name, "memory"},
			}

			ginkgo.By("checking that resource usage metrics for undeclared resources are not available", func() {
				util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, unavailableMetrics)
			})
		})
	})
})
