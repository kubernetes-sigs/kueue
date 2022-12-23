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

package v1alpha2

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var ns *corev1.Namespace

const (
	workloadName    = "workload-test"
	podSetsMaxItems = 8
)

var _ = ginkgo.BeforeEach(func() {
	ns = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "core-",
		},
	}
	gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
})

var _ = ginkgo.AfterEach(func() {
	gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
})

var _ = ginkgo.Describe("Workload defaulting webhook", func() {
	ginkgo.Context("When creating a Workload", func() {
		ginkgo.It("Should set default podSet name", func() {
			ginkgo.By("Creating a new Workload")
			workload := kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: ns.Name},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{
							Count: 1,
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{},
							},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, &workload)).Should(gomega.Succeed())

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

		ginkgo.It("Should have valid PriorityClassName when creating", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).
				PriorityClass("invalid_class").
				Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.DescribeTable("Should have valid PodSet when creating", func(podSetsCapacity int, podSetCount int, isInvalid bool) {
			podSets := make([]kueue.PodSet, podSetsCapacity)
			for i := range podSets {
				podSets[i].Name = fmt.Sprintf("ps%d", i)
				podSets[i].Count = int32(podSetCount)
				podSets[i].Spec = corev1.PodSpec{
					Containers: []corev1.Container{},
				}
			}
			workload := testing.MakeWorkload(workloadName, ns.Name).PodSets(podSets).Obj()
			err := k8sClient.Create(ctx, workload)
			if isInvalid {
				gomega.Expect(err).Should(gomega.HaveOccurred())
				gomega.Expect(errors.IsInvalid(err)).Should(gomega.BeTrue(), "error: %v", err)
			} else {
				gomega.Expect(err).Should(gomega.Succeed())
			}
		},
			ginkgo.Entry("podSets count less than 1", 0, 1, true),
			ginkgo.Entry("podSets count more than 8", podSetsMaxItems+1, 1, true),
			ginkgo.Entry("invalid podSet.Count", 3, 0, true),
			ginkgo.Entry("valid podSet", 3, 3, false),
		)
	})

	ginkgo.Context("When updating a Workload", func() {
		ginkgo.It("Should allow the change of priority", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating the priority")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.Priority = pointer.Int32(10)
				return k8sClient.Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should forbid the change of spec.podSet", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating podSet")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.PodSets[0].Count = 10
				return k8sClient.Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())
		})

		ginkgo.It("Should forbid the change of spec.queueName of an admitted workload", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).
				Queue("queue1").
				Admit(testing.MakeAdmission("cq").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.QueueName = "queue2"
				return k8sClient.Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())
		})

		ginkgo.It("Should forbid the change of spec.admission", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Admit(
				testing.MakeAdmission("cluster-queue").Obj(),
			).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.Admission.ClusterQueue = "foo-clusterQueue"
				return k8sClient.Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())

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
