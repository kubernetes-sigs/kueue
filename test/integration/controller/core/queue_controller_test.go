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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Queue controller", func() {
	var (
		ns           *corev1.Namespace
		queue        *kueue.Queue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-queue-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.BeforeEach(func() {
		clusterQueue = testing.MakeClusterQueue("clusterQueue_queue-controller").
			Resource(testing.MakeResource(resourceGPU).
				Flavor(testing.MakeFlavor(flavorModelA, "5").Max("10").Obj()).
				Flavor(testing.MakeFlavor(flavorModelB, "5").Max("10").Obj()).Obj()).Obj()
		queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteQueue(ctx, k8sClient, queue)).To(gomega.Succeed())
		gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
	})

	ginkgo.It("Should update status when workloads are created", func() {
		workloads := []*kueue.Workload{
			testing.MakeWorkload("one", ns.Name).
				Queue(queue.Name).
				Request(corev1.ResourceCPU, "2").Obj(),
			testing.MakeWorkload("two", ns.Name).
				Queue(queue.Name).
				Request(corev1.ResourceCPU, "3").Obj(),
			testing.MakeWorkload("three", ns.Name).
				Queue(queue.Name).
				Request(corev1.ResourceCPU, "1").Obj(),
		}

		ginkgo.By("Creating workloads")
		for _, w := range workloads {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.QueueStatus {
			var updatedQueue kueue.Queue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status
		}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.QueueStatus{PendingWorkloads: 3}))

		ginkgo.By("Admitting workloads")
		for _, w := range workloads {
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
				newWL.Spec.Admission = testing.MakeAdmission(clusterQueue.Name).
					Flavor(corev1.ResourceCPU, flavorOnDemand).Obj()
				return k8sClient.Update(ctx, &newWL)
			}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.QueueStatus {
			var updatedQueue kueue.Queue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status
		}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.QueueStatus{PendingWorkloads: 0}))

		ginkgo.By("Finishing workloads")
		for _, w := range workloads {
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
				newWL.Status.Conditions = append(w.Status.Conditions, kueue.WorkloadCondition{
					Type:   kueue.WorkloadFinished,
					Status: corev1.ConditionTrue,
				})
				return k8sClient.Status().Update(ctx, &newWL)
			}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.QueueStatus {
			var updatedQueue kueue.Queue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status
		}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.QueueStatus{}))
	})
})
