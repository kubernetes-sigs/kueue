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
	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("JobSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("with manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(jobset.SetupJobSetWebhook))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "jobset-")
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
			job := testingjob.MakeJobSet("jobset", ns.Name).Queue("indexed_job").
				ReplicatedJobs(
					testingjob.ReplicatedJobRequirements{
						Name: "leader",
					},
				).
				Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeForbiddenError())
		})

		ginkgo.It("shouldn't create JobSet if both kueue.x-k8s.io/podset-required-topology and kueue.x-k8s.io/podset-preferred-topology annotations are set", func() {
			job := testingjob.MakeJobSet("jobset", ns.Name).
				Queue("indexed_job").
				ReplicatedJobs(
					testingjob.ReplicatedJobRequirements{
						Name: "leader",
						PodAnnotations: map[string]string{
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
						},
					},
				).
				Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeForbiddenError())
		})
	})
})
