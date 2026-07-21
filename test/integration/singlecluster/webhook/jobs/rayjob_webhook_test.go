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
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayJob Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName disabled", func() {
		ginkgo.BeforeEach(func() {
			fwk.StartManager(ctx, cfg, managerSetup(rayjob.SetupRayJobWebhook))
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "rayjob-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.StopManager(ctx)
		})

		ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
			job := testingjob.MakeJob("rayjob", ns.Name).Queue("indexed_job").Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})

		ginkgo.It("invalid configuration shutdown after job finishes", func() {
			job := testingjob.MakeJob("rayjob", ns.Name).
				Queue("queue-name").
				ShutdownAfterJobFinishes(false).
				Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})

		ginkgo.It("should reject RayJob with clusterSelector and RayClusterSpec", func() {
			job := testingjob.MakeJob("rayjob-with-both", ns.Name).
				Queue("queue-name").
				ClusterSelector(map[string]string{"ray.io/cluster": "existing-cluster"}).
				Obj()
			// clusterSelector + RayClusterSpec -> validation error
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})

		ginkgo.It("should allow RayJob with clusterSelector but no RayClusterSpec", func() {
			job := testingjob.MakeJob("rayjob-selector-only", ns.Name).
				Queue("queue-name").
				ClusterSelector(map[string]string{"ray.io/cluster": "existing-cluster"}).
				RayClusterSpec(nil).
				Obj()
			// clusterSelector + nil RayClusterSpec -> valid (not managed by Kueue)
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		})

		ginkgo.It("should reject RayJob with queue label but no clusterSelector and no RayClusterSpec", func() {
			job := testingjob.MakeJob("rayjob-nil-clusterspec", ns.Name).
				Queue("queue-name").
				RayClusterSpec(nil).
				Obj()
			// no clusterSelector + nil RayClusterSpec -> validation error
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})

		ginkgo.It("should allow RayJob with clusterSelector and no queue label", func() {
			job := testingjob.MakeJob("rayjob-with-selector-no-queue", ns.Name).
				ClusterSelector(map[string]string{"ray.io/cluster": "existing-cluster"}).
				Obj()
			// No queue label -> not managed by Kueue, validation skipped
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		})

		ginkgo.It("should allow valid RayJob with RayClusterSpec and no clusterSelector", func() {
			job := testingjob.MakeJob("rayjob-valid", ns.Name).
				Queue("queue-name").
				Obj()
			// no clusterSelector + RayClusterSpec present -> valid, full validation runs
			// MakeJob() creates a valid RayClusterSpec by default
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
		})

		ginkgo.It("should reject removing the queue name from a running RayJob", func() {
			job := testingjob.MakeJob("rayjob-queue-removal", ns.Name).Queue("queue-name").Obj()
			util.MustCreate(ctx, k8sClient, job)

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &rayv1.RayJob{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			// Simulate a running workload dropping its queue name to escape Kueue.
			delete(createdJob.Labels, constants.QueueLabel)
			createdJob.Spec.Suspend = false
			err := k8sClient.Update(ctx, createdJob)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})
	})
})
