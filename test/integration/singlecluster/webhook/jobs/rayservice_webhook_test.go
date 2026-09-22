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

package jobs

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayService Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName disabled", func() {
		ginkgo.BeforeEach(func() {
			fwk.StartManager(ctx, cfg, managerSetup(rayservice.SetupRayServiceWebhook))
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayservice-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.StopManager(ctx)
		})

		ginkgo.When("ValidateRayAndSparkJobUpdates is enabled", func() {
			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ValidateRayAndSparkJobUpdates, true)
			})

			ginkgo.It("should reject removing the queue name from an unsuspended RayService", func() {
				service := testingrayservice.MakeService("rayservice", ns.Name).Queue("queue-name").Obj()
				util.MustCreate(ctx, k8sClient, service)

				lookupKey := types.NamespacedName{Name: service.Name, Namespace: service.Namespace}
				createdService := &rayv1.RayService{}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdService)).Should(gomega.Succeed())

				// Simulate an unsuspended service updating other fields while retaining the queue name.
				createdService.Spec.RayClusterSpec.Suspend = new(false)
				gomega.Expect(k8sClient.Update(ctx, createdService)).Should(gomega.Succeed())

				// Simulate an unsuspended service dropping its queue name to become unmanaged.
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdService)).Should(gomega.Succeed())
				delete(createdService.Labels, constants.QueueLabel)
				err := k8sClient.Update(ctx, createdService)
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})
		})

		ginkgo.When("ValidateRayAndSparkJobUpdates is disabled", func() {
			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ValidateRayAndSparkJobUpdates, false)
			})

			ginkgo.It("should allow removing the queue name from an unsuspended RayService", func() {
				service := testingrayservice.MakeService("rayservice", ns.Name).Queue("queue-name").Obj()
				util.MustCreate(ctx, k8sClient, service)

				lookupKey := types.NamespacedName{Name: service.Name, Namespace: service.Namespace}
				createdService := &rayv1.RayService{}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdService)).Should(gomega.Succeed())

				// Simulate an unsuspended service updating other fields while retaining the queue name.
				createdService.Spec.RayClusterSpec.Suspend = new(false)
				gomega.Expect(k8sClient.Update(ctx, createdService)).Should(gomega.Succeed())

				// Simulate an unsuspended service dropping its queue name to become unmanaged.
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdService)).Should(gomega.Succeed())
				delete(createdService.Labels, constants.QueueLabel)
				gomega.Expect(k8sClient.Update(ctx, createdService)).Should(gomega.Succeed())
			})
		})
	})
})
