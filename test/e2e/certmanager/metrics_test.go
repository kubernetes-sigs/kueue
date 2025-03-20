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

package certmanager

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	serviceAccountName           = "kueue-controller-manager"
	metricsReaderClusterRoleName = "kueue-metrics-reader"
	metricsServiceName           = "kueue-controller-manager-metrics-service"
	certSecretName               = "metrics-server-cert"
	certMountPath                = "/etc/kueue/metrics/certs"
)

var _ = ginkgo.Describe("Metrics", ginkgo.Ordered, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *v1beta1.ResourceFlavor

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	ginkgo.BeforeEach(func() {
		ns = utiltesting.MakeNamespaceWithGenerateName("e2e-metrics-")
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		resourceFlavor = utiltesting.MakeResourceFlavor("test-flavor").Obj()
		gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

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
			Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		curlPod.Spec.Volumes = []corev1.Volume{
			corev1.Volume{
				Name: "metrics-certs",
				VolumeSource: corev1.VolumeSource{
					Secret: &corev1.SecretVolumeSource{
						SecretName: certSecretName,
						Items: []corev1.KeyToPath{
							{Key: "ca.crt", Path: "ca.crt"},
						},
					},
				},
			},
		}
		curlPod.Spec.Containers[0].VolumeMounts = []corev1.VolumeMount{
			{
				Name:      "metrics-certs",
				MountPath: certMountPath,
				ReadOnly:  true,
			},
		}
		gomega.Expect(k8sClient.Create(ctx, curlPod)).To(gomega.Succeed())

		ginkgo.By("Waiting for metrics-server-cert secret", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				secret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{
					Namespace: config.DefaultNamespace,
					Name:      certSecretName,
				}, secret)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for the curl-metrics pod to run", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(curlPod), createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				curlContainerName = createdPod.Spec.Containers[0].Name
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
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
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			localQueue = utiltesting.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())

			workload = utiltesting.MakeWorkload("test-workload", ns.Name).
				Queue(localQueue.Name).
				PodSets(
					*utiltesting.MakePodSet("ps1", 1).Obj(),
				).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).To(gomega.Succeed())
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
				expectMetricsToBeAvailable(curlPod.Name, curlContainerName, metrics)
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
				expectMetricsNotToBeAvailable(curlPod.Name, curlContainerName, deletedMetrics)
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
				expectMetricsToBeAvailable(curlPod.Name, curlContainerName, notDeletedMetrics)
			})
		})
	})
})

func getKueueMetricsSecure(curlPodName, curlContainerName string) ([]byte, error) {
	metricsOutput, _, err := util.KExecute(ctx, cfg, restClient, config.DefaultNamespace, curlPodName, curlContainerName,
		[]string{
			"/bin/sh", "-c",
			fmt.Sprintf(
				"curl -s --cacert %s/ca.crt -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics",
				certMountPath,
				metricsServiceName,
				config.DefaultNamespace,
			),
		})

	return metricsOutput, err
}

func expectMetricsToBeAvailable(curlPodName, curlContainerName string, metrics [][]string) {
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, err := getKueueMetricsSecure(curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		g.Expect(string(metricsOutput)).Should(utiltesting.ContainMetrics(metrics))
	}, util.Timeout).Should(gomega.Succeed())
}

func expectMetricsNotToBeAvailable(curlPodName, curlContainerName string, metrics [][]string) {
	gomega.Eventually(func(g gomega.Gomega) {
		metricsOutput, err := getKueueMetricsSecure(curlPodName, curlContainerName)
		g.Expect(err).NotTo(gomega.HaveOccurred())

		g.Expect(string(metricsOutput)).Should(utiltesting.ExcludeMetrics(metrics))
	}, util.Timeout).Should(gomega.Succeed())
}
