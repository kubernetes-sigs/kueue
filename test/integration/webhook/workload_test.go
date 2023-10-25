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

package webhook

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
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
			// Not using the wrappers to avoid hiding any defaulting.
			workload := kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: ns.Name},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						{
							Count: 1,
							Template: corev1.PodTemplateSpec{
								Spec: corev1.PodSpec{
									Containers: []corev1.Container{},
								},
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
				podSets[i] = *testing.MakePodSet(fmt.Sprintf("ps%d", i), podSetCount).Obj()
			}
			workload := testing.MakeWorkload(workloadName, ns.Name).PodSets(podSets...).Obj()
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
		var (
			updatedQueueWorkload  kueue.Workload
			finalQueueWorkload    kueue.Workload
			workloadPriorityClass *kueue.WorkloadPriorityClass
			priorityClass         *schedulingv1.PriorityClass
		)
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = testing.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			priorityClass = testing.MakePriorityClass("priority-class").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, priorityClass)).To(gomega.Succeed())

		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, priorityClass)).To(gomega.Succeed())
		})

		ginkgo.It("Should allow the change of priority", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Updating the priority")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Spec.Priority = ptr.To[int32](10)
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
			ginkgo.By("Creating and admitting a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).Queue("queue1").Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())
			gomega.EventuallyWithOffset(1, func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				return util.SetQuotaReservation(ctx, k8sClient, &newWL, testing.MakeAdmission("cq").Obj())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
			workload := testing.MakeWorkload(workloadName, ns.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, workload)).Should(gomega.Succeed())

			ginkgo.By("Admitting the Workload")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = testing.MakeAdmission("cluster-queue").Obj()
				return k8sClient.Status().Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating queueName")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workload), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission.ClusterQueue = "foo-cluster-queue"
				return k8sClient.Status().Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(testing.BeForbiddenError())

		})

		ginkgo.It("Should have priority once priorityClassName is set", func() {
			ginkgo.By("Creating a new Workload")
			workload := testing.MakeWorkload(workloadName, ns.Name).PriorityClass("priority").Obj()
			err := k8sClient.Create(ctx, workload)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(errors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("workload's priority should be mutable when referencing WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl := testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("workload-priority-class").PriorityClassSource(constants.WorkloadPriorityClassSource).Priority(200).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
			initialPriority := int32(200)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("Updating workload's priority")
			updatedPriority := int32(150)
			updatedQueueWorkload.Spec.Priority = &updatedPriority
			gomega.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func() *int32 {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				return finalQueueWorkload.Spec.Priority
			}, util.Timeout, util.Interval).Should(gomega.Equal(&updatedPriority))
		})

		ginkgo.It("workload's priority should be mutable when referencing PriorityClass", func() {
			ginkgo.By("creating workload")
			wl := testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("priority-class").PriorityClassSource(constants.PodPriorityClassSource).Priority(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
			initialPriority := int32(100)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("Updating workload's priority")
			updatedPriority := int32(50)
			updatedQueueWorkload.Spec.Priority = &updatedPriority
			gomega.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
			gomega.Eventually(func() *int32 {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				return finalQueueWorkload.Spec.Priority
			}, util.Timeout, util.Interval).Should(gomega.Equal(&updatedPriority))
		})
	})
})
