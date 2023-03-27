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
	"fmt"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Queue controller", func() {
	const (
		flavorModelC = "model-c"
		flavorModelD = "model-d"
	)
	var (
		ns              *corev1.Namespace
		queue           *kueue.LocalQueue
		clusterQueue    *kueue.ClusterQueue
		resourceFlavors = []kueue.ResourceFlavor{
			*testing.MakeResourceFlavor(flavorModelC).Label(resourceGPU.String(), flavorModelC).Obj(),
			*testing.MakeResourceFlavor(flavorModelD).Label(resourceGPU.String(), flavorModelD).Obj(),
		}
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
		clusterQueue = testing.MakeClusterQueue("cluster-queue.queue-controller").
			ResourceGroup(
				*testing.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "5", "5").Obj(),
				*testing.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
			).Obj()
		queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		for _, rf := range resourceFlavors {
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, &rf)).To(gomega.Succeed())
		}
		gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, queue)).To(gomega.Succeed())
		gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
	})

	ginkgo.It("Should update conditions when clusterQueues that its localQueue references are updated", func() {
		gomega.Eventually(func() []metav1.Condition {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status.Conditions
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
			{
				Type:    kueue.LocalQueueActive,
				Status:  metav1.ConditionFalse,
				Reason:  "ClusterQueueDoesNotExist",
				Message: "Can't submit new workloads to clusterQueue",
			},
		}, ignoreConditionTimestamps))

		ginkgo.By("Creating a clusterQueue")
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		gomega.Eventually(func() []metav1.Condition {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status.Conditions
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
			{
				Type:    kueue.LocalQueueActive,
				Status:  metav1.ConditionFalse,
				Reason:  "ClusterQueueIsInactive",
				Message: "Can't submit new workloads to clusterQueue",
			},
		}, ignoreConditionTimestamps))

		ginkgo.By("Creating resourceFlavors")
		for _, rf := range resourceFlavors {
			gomega.Expect(k8sClient.Create(ctx, &rf)).To(gomega.Succeed())
		}
		gomega.Eventually(func() []metav1.Condition {
			var updatedCQ kueue.ClusterQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
			return updatedCQ.Status.Conditions
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
			{
				Type:    kueue.ClusterQueueActive,
				Status:  metav1.ConditionTrue,
				Reason:  "Ready",
				Message: "Can admit new workloads",
			},
		}, ignoreConditionTimestamps))
		gomega.Eventually(func() []metav1.Condition {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status.Conditions
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
			{
				Type:    kueue.LocalQueueActive,
				Status:  metav1.ConditionTrue,
				Reason:  "Ready",
				Message: "Can submit new workloads to clusterQueue",
			},
		}, ignoreConditionTimestamps))

		ginkgo.By("Deleting a clusterQueue")
		gomega.Expect(k8sClient.Delete(ctx, clusterQueue)).To(gomega.Succeed())
		gomega.Eventually(func() []metav1.Condition {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status.Conditions
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
			{
				Type:    kueue.LocalQueueActive,
				Status:  metav1.ConditionFalse,
				Reason:  "ClusterQueueDoesNotExist",
				Message: "Can't submit new workloads to clusterQueue",
			},
		}, ignoreConditionTimestamps))
	})

	ginkgo.It("Should update status when workloads are created", func() {
		ginkgo.By("Creating resourceFlavors")
		for _, rf := range resourceFlavors {
			gomega.Expect(k8sClient.Create(ctx, &rf)).To(gomega.Succeed())
		}
		ginkgo.By("Creating a clusterQueue")
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

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
		gomega.Eventually(func() kueue.LocalQueueStatus {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
			AdmittedWorkloads: 0,
			PendingWorkloads:  3,
			Conditions: []metav1.Condition{
				{
					Type:    kueue.LocalQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can submit new workloads to clusterQueue",
				},
			},
		}, ignoreConditionTimestamps))

		ginkgo.By("Admitting workloads")
		for _, w := range workloads {
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
				newWL.Status.Admission = testing.MakeAdmission(clusterQueue.Name).
					Assignment(corev1.ResourceCPU, flavorOnDemand, "1").Obj()
				newWL.Status.Conditions = []metav1.Condition{{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.Now(),
					Reason:             "AdmissionSet",
					Message:            fmt.Sprintf("Admitted by ClusterQueue %s", newWL.Status.Admission.ClusterQueue),
				}}
				return k8sClient.Status().Update(ctx, &newWL)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.LocalQueueStatus {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
			AdmittedWorkloads: 3,
			PendingWorkloads:  0,
			Conditions: []metav1.Condition{
				{
					Type:    kueue.LocalQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can submit new workloads to clusterQueue",
				},
			},
		}, ignoreConditionTimestamps))

		ginkgo.By("Finishing workloads")
		util.FinishWorkloads(ctx, k8sClient, workloads...)
		gomega.Eventually(func() kueue.LocalQueueStatus {
			var updatedQueue kueue.LocalQueue
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			return updatedQueue.Status
		}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
			Conditions: []metav1.Condition{
				{
					Type:    kueue.LocalQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can submit new workloads to clusterQueue",
				},
			},
		}, ignoreConditionTimestamps))
	})
})
