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
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	gomegaformat "github.com/onsi/gomega/format"
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
)

var _ = ginkgo.Describe("Metrics", func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-metrics-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should ensure the metrics endpoint is serving metrics", func() {
		ginkgo.By("Creating a ClusterRoleBinding for the service account to allow access to metrics")
		var (
			resourceFlavor *v1beta1.ResourceFlavor
			clusterQueue   *v1beta1.ClusterQueue
			localQueue     *v1beta1.LocalQueue
			workload       *v1beta1.Workload
		)

		metricsReaderClusterRoleBinding := &rbacv1.ClusterRoleBinding{
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
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the ClusterRoleBinding", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
			})
		})

		ginkgo.By("creating resource flavor", func() {
			resourceFlavor = utiltesting.MakeResourceFlavor("test-flavor").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the resource flavor", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, utiltesting.MakeResourceFlavor(resourceFlavor.GetName()).Obj(), true)
			})
		})

		ginkgo.By("Creating a cluster queue", func() {
			clusterQueue = utiltesting.MakeClusterQueue("test-cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas(resourceFlavor.GetName()).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the cluster queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, utiltesting.MakeClusterQueue(clusterQueue.GetName()).Obj(), true)
			})
		})

		ginkgo.By("Creating a local queue", func() {
			localQueue = utiltesting.MakeLocalQueue("test-lq", ns.GetName()).
				ClusterQueue(clusterQueue.GetName()).
				Obj()

			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the local queue", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, utiltesting.MakeLocalQueue(localQueue.GetName(), ns.GetName()).Obj(), true)
			})
		})

		ginkgo.By("Creating a workload", func() {
			workload = utiltesting.MakeWorkload("test-workload", ns.GetName()).
				Queue(localQueue.GetName()).
				PodSets(
					*utiltesting.MakePodSet("ps1", 1).Obj(),
				).
				Request(corev1.ResourceCPU, "1").
				Obj()

			gomega.Expect(k8sClient.Create(ctx, workload)).To(gomega.Succeed())
		})

		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the workload", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, utiltesting.MakeWorkload(workload.GetName(), ns.GetName()).Obj(), true)
			})
		})

		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

		ginkgo.By("Creating the curl-metrics pod to access the metrics endpoint")
		pod := testingjobspod.MakePod("curl-metrics", config.DefaultNamespace).
			ServiceAccountName(serviceAccountName).
			Image(util.E2eTTestCurlImage, []string{
				"sleep", "5m",
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the pod", func() {
				util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, pod, true, util.LongTimeout)
			})
		})

		ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
			}, util.LongTimeout).Should(gomega.Succeed())
		})

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

		defaultGomegaMaxLength := gomegaformat.MaxLength
		gomegaformat.MaxLength = 0
		ginkgo.DeferCleanup(func() {
			gomegaformat.MaxLength = defaultGomegaMaxLength
		})

		ginkgo.By("Getting the metrics by checking curl-metrics logs", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cmd := exec.Command("kubectl", "exec", "-n", config.DefaultNamespace, "curl-metrics", "--", "/bin/sh", "-c",
					fmt.Sprintf(
						"curl -s -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics ",
						metricsServiceName, config.DefaultNamespace,
					),
				)
				metricsOutput, err := cmd.CombinedOutput()
				g.Expect(err).NotTo(gomega.HaveOccurred())
				for _, metric := range metrics {
					g.Expect(string(metricsOutput)).To(gomega.ContainSubstring(metric))
				}
			}, util.Timeout).Should(gomega.Succeed())
		})
	})
})
