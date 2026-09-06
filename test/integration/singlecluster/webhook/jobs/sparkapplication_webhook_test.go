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
	sparkappv1beta2 "github.com/kubeflow/spark-operator/v2/api/v1beta2"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingspark "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("SparkApplication Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName disabled", func() {
		ginkgo.BeforeEach(func() {
			fwk.StartManager(ctx, cfg, managerSetup(sparkapplication.SetupWebhook))
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "sparkapplication-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.StopManager(ctx)
		})

		ginkgo.When("ValidateRayAndSparkJobUpdates is enabled", func() {
			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ValidateRayAndSparkJobUpdates, true)
			})

			ginkgo.It("should reject removing the queue name from an unsuspended SparkApplication", func() {
				app := testingspark.MakeSparkApplication("sparkapp", ns.Name).Queue("queue-name").Obj()
				util.MustCreate(ctx, k8sClient, app)

				lookupKey := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
				createdApp := &sparkappv1beta2.SparkApplication{}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdApp)).Should(gomega.Succeed())

				// Simulate an unsuspended application updating other fields while retaining the queue name.
				createdApp.Spec.Suspend = new(false)
				gomega.Expect(k8sClient.Update(ctx, createdApp)).Should(gomega.Succeed())

				// Simulate an unsuspended application dropping its queue name to become unmanaged.
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdApp)).Should(gomega.Succeed())
				delete(createdApp.Labels, controllerconstants.QueueLabel)
				err := k8sClient.Update(ctx, createdApp)
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(err).Should(utiltesting.BeForbiddenError())
			})
		})

		ginkgo.When("ValidateRayAndSparkJobUpdates is disabled", func() {
			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ValidateRayAndSparkJobUpdates, false)
			})

			ginkgo.It("should allow removing the queue name from an unsuspended SparkApplication", func() {
				app := testingspark.MakeSparkApplication("sparkapp", ns.Name).Queue("queue-name").Obj()
				util.MustCreate(ctx, k8sClient, app)

				lookupKey := types.NamespacedName{Name: app.Name, Namespace: app.Namespace}
				createdApp := &sparkappv1beta2.SparkApplication{}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdApp)).Should(gomega.Succeed())

				// Simulate an unsuspended application updating other fields while retaining the queue name.
				createdApp.Spec.Suspend = new(false)
				gomega.Expect(k8sClient.Update(ctx, createdApp)).Should(gomega.Succeed())

				// Simulate an unsuspended application dropping its queue name to become unmanaged.
				gomega.Expect(k8sClient.Get(ctx, lookupKey, createdApp)).Should(gomega.Succeed())
				delete(createdApp.Labels, controllerconstants.QueueLabel)
				gomega.Expect(k8sClient.Update(ctx, createdApp)).Should(gomega.Succeed())
			})
		})
	})
})
