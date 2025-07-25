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
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjobspaddlejob "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("PaddleJob Webhook", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(paddlejob.SetupPaddleJobWebhook))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "paddle-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("with TopologyAwareScheduling enabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
		})

		ginkgo.It("the creation doesn't succeed if job contain both kueue.x-k8s.io/podset-required-topology and kueue.x-k8s.io/podset-preferred-topology annotations", func() {
			job := testingjobspaddlejob.MakePaddleJob("job", ns.Name).
				PaddleReplicaSpecs(
					testingjobspaddlejob.PaddleReplicaSpecRequirement{
						ReplicaType:  kftraining.PaddleJobReplicaTypeMaster,
						ReplicaCount: 1,
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/rack",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/rack",
						},
					},
				).
				Queue("lq").
				Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(testing.BeForbiddenError(), "error: %v", err)
		})
	})
})
