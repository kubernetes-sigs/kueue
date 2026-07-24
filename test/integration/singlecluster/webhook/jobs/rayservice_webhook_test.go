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

		ginkgo.It("should reject removing the queue name from a running RayService", func() {
			service := testingrayservice.MakeService("rayservice", ns.Name).Queue("queue-name").Obj()
			util.MustCreate(ctx, k8sClient, service)

			lookupKey := types.NamespacedName{Name: service.Name, Namespace: service.Namespace}
			createdService := &rayv1.RayService{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdService)).Should(gomega.Succeed())

			// Simulate a running workload dropping its queue name to escape Kueue.
			delete(createdService.Labels, constants.QueueLabel)
			createdService.Spec.RayClusterSpec.Suspend = new(false)
			err := k8sClient.Update(ctx, createdService)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})
	})
})
