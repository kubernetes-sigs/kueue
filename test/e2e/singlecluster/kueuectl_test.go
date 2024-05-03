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
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueuectl Create", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		cq *v1beta1.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue("e2e-cq").Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
	})

	ginkgo.When("Creating a LocalQueue", func() {
		ginkgo.It("Should create local queue", func() {
			lqName := "e2e-lq"

			ginkgo.By("Create local queue by kueuectl", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName, "--clusterqueue", cq.Name, "--namespace", ns.Name)
				output, err := cmd.CombinedOutput()
				gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s: %s", err, output)
			})

			ginkgo.By("Check that the local queue successfully created", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).To(gomega.Succeed())
					g.Expect(createdQueue.Name).Should(gomega.Equal(lqName))
					g.Expect(createdQueue.Spec.ClusterQueue).Should(gomega.Equal(v1beta1.ClusterQueueReference(cq.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Shouldn't create local queue with unknown cluster queue", func() {
			lqName := "e2e-lq"
			cqName := "e2e-cq-unknown"

			ginkgo.By("Create local queue by kueuectl", func() {
				cmd := exec.Command(kueuectlPath, "create", "localqueue", lqName, "--clusterqueue", cqName, "--namespace", ns.Name)
				_, err := cmd.CombinedOutput()
				gomega.Expect(err).To(gomega.HaveOccurred())
			})

			ginkgo.By("Check that the local queue did not create", func() {
				var createdQueue v1beta1.LocalQueue
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: lqName, Namespace: ns.Name}, &createdQueue)).ToNot(gomega.Succeed())
			})
		})
	})
})
