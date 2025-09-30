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

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	serviceAccountName           = "kueue-controller-manager"
	metricsReaderClusterRoleName = "kueue-metrics-reader"
	metricsServiceName           = "kueue-controller-manager-metrics-service"
	certSecretName               = "kueue-metrics-server-cert"
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
		curlPod.Spec.Volumes = []corev1.Volume{
			{
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
		util.MustCreate(ctx, k8sClient, curlPod)

		ginkgo.By("Waiting for kueue-metrics-server-cert secret", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				secret := &corev1.Secret{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{
					Namespace: kueueNS,
					Name:      certSecretName,
				}, secret)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

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
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			localQueue = utiltesting.MakeLocalQueue("", ns.Name).
				GeneratedName("test-lq-").
				ClusterQueue(clusterQueue.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())

			workload = utiltesting.MakeWorkload("test-workload", ns.Name).
				Queue(v1beta1.LocalQueueName(localQueue.Name)).
				PodSets(
					*utiltesting.MakePodSet("ps1", 1).Obj(),
				).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		ginkgo.It("should expose quota reserved workload metric", func() {
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

			ginkgo.By("checking that the quota reserved workload metric is present", func() {
				expectedMetric := []string{
					"kueue_quota_reserved_workloads_total",
					clusterQueue.Name,
				}
				expectMetricsToBeAvailable(curlPod.Name, curlContainerName, [][]string{expectedMetric})
			})
		})
	})
})

func getKueueMetricsSecure(curlPodName, curlContainerName string) ([]byte, error) {
	metricsOutput, _, err := util.KExecute(ctx, cfg, restClient, kueueNS, curlPodName, curlContainerName,
		[]string{
			"/bin/sh", "-c",
			fmt.Sprintf(
				"curl -s --cacert %s/ca.crt -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics",
				certMountPath,
				metricsServiceName,
				kueueNS,
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
