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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	kubeSystemNamespace       = "kube-system"
	kueueManagerName          = "kueue-controller-manager"
	kueueVisibilityServerName = "kueue-visibility-server"
	cqName                    = "test-kubeconfig-cq"
	customVisibilityPort      = 9444
)

var _ = ginkgo.Describe("Visibility Server", ginkgo.Label("feature:visibility", util.Shard0), ginkgo.Ordered, func() {
	var originalDeployment appsv1.Deployment
	var originalService corev1.Service
	var cq *kueue.ClusterQueue

	ginkgo.BeforeAll(func() {
		ginkgo.By("Updating the visibilityServer configuration and restarting Kueue")
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.VisibilityServer = &configapi.VisibilityServerConfiguration{
				BindPort: ptr.To[int32](customVisibilityPort),
			}
		})
	})

	ginkgo.BeforeEach(func() {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNS}, &originalDeployment)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = k8sClient.Get(ctx, types.NamespacedName{Name: kueueVisibilityServerName, Namespace: kueueNS}, &originalService)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("Creating a ClusterQueue")
		cq = &kueue.ClusterQueue{
			ObjectMeta: metav1.ObjectMeta{Name: cqName},
		}
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		ginkgo.By("Restoring the original deployment")
		gomega.Eventually(func(g gomega.Gomega) {
			latestDeployment := &appsv1.Deployment{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kueueManagerName, Namespace: kueueNS}, latestDeployment)).To(gomega.Succeed())
			latestDeployment.Spec = originalDeployment.Spec
			g.Expect(k8sClient.Update(ctx, latestDeployment)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Restoring the original service")
		gomega.Eventually(func(g gomega.Gomega) {
			latestService := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kueueVisibilityServerName, Namespace: kueueNS}, latestService)).To(gomega.Succeed())
			latestService.Spec.Ports = originalService.Spec.Ports
			g.Expect(k8sClient.Update(ctx, latestService)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.WaitForKueueAvailabilityNoRestartCountCheck(ctx, k8sClient)

		ginkgo.By("Cleaning up cluster queue")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.It("Should use the custom port from the visibilityServer configuration API", func() {
		ginkgo.By("Updating the visibilityServer configuration and restarting Kueue")

		ginkgo.By("Updating the visibility-server service's targetPort")
		gomega.Eventually(func(g gomega.Gomega) {
			patchedService := &corev1.Service{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: kueueVisibilityServerName, Namespace: kueueNS}, patchedService)).To(gomega.Succeed())
			for i, p := range patchedService.Spec.Ports {
				if p.Name == "https" {
					patchedService.Spec.Ports[i].TargetPort = intstr.FromInt32(customVisibilityPort)
				}
			}
			g.Expect(k8sClient.Update(ctx, patchedService)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying requests succeed on the custom port")
		gomega.Eventually(func(g gomega.Gomega) {
			visClient := util.CreateVisibilityClient("")
			pw, err := visClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, cqName, metav1.GetOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(pw).NotTo(gomega.BeNil())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
