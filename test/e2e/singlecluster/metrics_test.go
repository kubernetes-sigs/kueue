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
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	serviceAccountName           = "kueue-controller-manager"
	metricsReaderClusterRoleName = "kueue-metrics-reader"
	metricsServiceName           = "kueue-controller-manager-metrics-service"
)

var _ = ginkgo.Describe("Metrics", func() {
	ginkgo.It("should ensure the metrics endpoint is serving metrics", func() {
		ginkgo.By("Creating a ClusterRoleBinding for the service account to allow access to metrics")
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

		ginkgo.By("Creating the curl-metrics pod to access the metrics endpoint")
		pod := testingjobspod.MakePod("curl-metrics", config.DefaultNamespace).
			ServiceAccountName(serviceAccountName).
			Image(util.E2eTTestCurlImage, []string{
				"/bin/sh", "-c", fmt.Sprintf(
					"curl -s -k -H \"Authorization: Bearer $(cat /var/run/secrets/kubernetes.io/serviceaccount/token)\" https://%s.%s.svc.cluster.local:8443/metrics",
					metricsServiceName, config.DefaultNamespace,
				),
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, pod)).Should(gomega.Succeed())
		ginkgo.DeferCleanup(func() {
			ginkgo.By("Deleting the pod", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
			})
		})

		ginkgo.By("Waiting for the curl-metrics pod to complete.", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPod := &corev1.Pod{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), createdPod)).To(gomega.Succeed())
				g.Expect(createdPod.Status.Phase).To(gomega.Equal(corev1.PodSucceeded))
			}, util.LongTimeout).Should(gomega.Succeed())
		})

		ginkgo.By("Getting the metrics by checking curl-metrics logs", func() {
			cmd := exec.Command("kubectl", "logs", "curl-metrics", "-n", config.DefaultNamespace)
			metricsOutput, err := cmd.CombinedOutput()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(metricsOutput).To(gomega.ContainSubstring("controller_runtime_reconcile_total"))
		})
	})
})
