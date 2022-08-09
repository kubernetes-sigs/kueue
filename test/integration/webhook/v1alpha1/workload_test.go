/*
Copyright 2022 The Kubernetes Authors.
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

package v1alpha1

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var ns *corev1.Namespace

const workloadName = "workload-test"

var _ = ginkgo.BeforeEach(func() {
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "core-",
		},
	}
	gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
})

var _ = ginkgo.AfterEach(func() {
	gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
})

var _ = ginkgo.Describe("Workload defaulting webhook", func() {
	ginkgo.Context("When creating a Workload", func() {
		ginkgo.It("Should set default values", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			created := &kueue.Workload{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name:      workload.Name,
				Namespace: workload.Namespace,
			}, created)).Should(gomega.Succeed())

			gomega.Expect(created.Spec.PodSets[0].Name).Should(gomega.Equal(kueue.DefaultPodSetName))
		})
	})
})

var _ = ginkgo.Describe("Workload validating webhook", func() {
	ginkgo.Context("When creating a Workload", func() {
		ginkgo.It("Should validate Workload", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).
				PodSets([]kueue.PodSet{
					{
						Name:  "main",
						Count: -1,
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{
									Name: "c",
									Resources: corev1.ResourceRequirements{
										Requests: make(corev1.ResourceList),
									},
								},
							},
						},
					},
				}).
				PriorityClass("invalid_class").
				Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})
	})

	ginkgo.Context("When updating a Workload", func() {
		ginkgo.It("Should allow the change of priority", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating the priority")
			priority := int32(10)
			workload.Spec.Priority = &priority
			gomega.Expect(k8sClient.Update(ctx, workload)).Should(gomega.Succeed())
		})

		ginkgo.It("Should forbid the change of spec.podSet", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating podSet")
			workload.Spec.PodSets[0].Count = 10
			err := k8sClient.Update(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should forbid the change of spec.queueName", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Queue("queue1").Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			workload.Spec.QueueName = "queue2"
			err := k8sClient.Update(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should forbid the change of spec.admission", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Admit(
				testing.MakeAdmission("cluster-queue").Obj(),
			).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			workload.Spec.Admission.ClusterQueue = "foo-clusterQueue"
			err := k8sClient.Update(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should have priority once priorityClassName is set", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).PriorityClass("priority").Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})
	})
})
