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
	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

const (
	resourceGPU corev1.ResourceName = "example.com/gpu"

	flavorOnDemand = "on-demand"
	flavorSpot     = "spot"
	flavorModelA   = "model-a"
	flavorModelB   = "model-b"
)

var _ = ginkgo.Describe("Capacity controller", func() {
	var (
		ns             *corev1.Namespace
		capacity       *kueue.Capacity
		emptyCapStatus kueue.CapacityStatus
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.BeforeEach(func() {
		capacity = testing.MakeCapacity("capacity").
			Resource(testing.MakeResource(corev1.ResourceCPU).
				Flavor(testing.MakeFlavor(flavorOnDemand, "5").Ceiling("10").Obj()).
				Flavor(testing.MakeFlavor(flavorSpot, "5").Ceiling("10").Obj()).Obj()).
			Resource(testing.MakeResource(resourceGPU).
				Flavor(testing.MakeFlavor(flavorModelA, "5").Ceiling("10").Obj()).
				Flavor(testing.MakeFlavor(flavorModelB, "5").Ceiling("10").Obj()).Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, capacity)).To(gomega.Succeed())
		emptyCapStatus = kueue.CapacityStatus{
			AssignedWorkloads: 0,
			UsedResources: kueue.UsedResources{
				corev1.ResourceCPU: {
					flavorOnDemand: {Total: pointer.Quantity(resource.MustParse("0"))},
					flavorSpot:     {Total: pointer.Quantity(resource.MustParse("0"))},
				},
				resourceGPU: {
					flavorModelA: {Total: pointer.Quantity(resource.MustParse("0"))},
					flavorModelB: {Total: pointer.Quantity(resource.MustParse("0"))},
				},
			},
		}
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteCapacity(ctx, k8sClient, capacity)).To(gomega.Succeed())
	})

	ginkgo.It("Should update status when workloads are assigned and finish", func() {
		workloads := []*kueue.QueuedWorkload{
			testing.MakeQueuedWorkload("one", ns.Name).
				Request(corev1.ResourceCPU, "2").AssignFlavor(corev1.ResourceCPU, flavorOnDemand).
				Request(resourceGPU, "2").AssignFlavor(resourceGPU, flavorModelA).Obj(),
			testing.MakeQueuedWorkload("two", ns.Name).
				Request(corev1.ResourceCPU, "3").AssignFlavor(corev1.ResourceCPU, flavorOnDemand).
				Request(resourceGPU, "3").AssignFlavor(resourceGPU, flavorModelA).Obj(),
			testing.MakeQueuedWorkload("three", ns.Name).
				Request(corev1.ResourceCPU, "1").AssignFlavor(corev1.ResourceCPU, flavorOnDemand).
				Request(resourceGPU, "1").AssignFlavor(resourceGPU, flavorModelB).Obj(),
			testing.MakeQueuedWorkload("four", ns.Name).
				Request(corev1.ResourceCPU, "1").AssignFlavor(corev1.ResourceCPU, flavorSpot).
				Request(resourceGPU, "1").AssignFlavor(resourceGPU, flavorModelB).Obj(),
			testing.MakeQueuedWorkload("five", ns.Name).
				Request(corev1.ResourceCPU, "1").AssignFlavor(corev1.ResourceCPU, flavorSpot).
				Request(resourceGPU, "1").AssignFlavor(resourceGPU, flavorModelB).Obj(),
			testing.MakeQueuedWorkload("six", ns.Name).
				Request(corev1.ResourceCPU, "1").AssignFlavor(corev1.ResourceCPU, flavorSpot).
				Request(resourceGPU, "1").AssignFlavor(resourceGPU, flavorModelA).Obj(),
		}

		ginkgo.By("Creating workloads")
		for _, w := range workloads {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.CapacityStatus {
			var updatedCap kueue.Capacity
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(capacity), &updatedCap)).To(gomega.Succeed())
			return updatedCap.Status
		}, framework.Timeout, framework.Interval).Should(testing.Equal(emptyCapStatus))

		ginkgo.By("Assigning workloads")
		assignments := []string{capacity.Name, capacity.Name, capacity.Name, capacity.Name, "other", ""}
		for i, w := range workloads {
			w.Spec.AssignedCapacity = kueue.CapacityReference(assignments[i])
			gomega.Expect(k8sClient.Update(ctx, w)).To(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.CapacityStatus {
			var updatedCap kueue.Capacity
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(capacity), &updatedCap)).To(gomega.Succeed())
			return updatedCap.Status
		}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.CapacityStatus{
			AssignedWorkloads: 4,
			UsedResources: kueue.UsedResources{
				corev1.ResourceCPU: {
					flavorOnDemand: {
						Total:    pointer.Quantity(resource.MustParse("6")),
						Borrowed: pointer.Quantity(resource.MustParse("1")),
					},
					flavorSpot: {
						Total: pointer.Quantity(resource.MustParse("1")),
					},
				},
				resourceGPU: {
					flavorModelA: {
						Total: pointer.Quantity(resource.MustParse("5")),
					},
					flavorModelB: {
						Total: pointer.Quantity(resource.MustParse("2")),
					},
				},
			},
		}))

		ginkgo.By("Finishing workloads")
		for _, w := range workloads {
			w.Status.Conditions = append(w.Status.Conditions, kueue.QueuedWorkloadCondition{
				Type:   kueue.QueuedWorkloadFinished,
				Status: corev1.ConditionTrue,
			})
			gomega.Expect(k8sClient.Status().Update(ctx, w)).To(gomega.Succeed())
		}
		gomega.Eventually(func() kueue.CapacityStatus {
			var updatedCap kueue.Capacity
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(capacity), &updatedCap)).To(gomega.Succeed())
			return updatedCap.Status
		}, framework.Timeout, framework.Interval).Should(testing.Equal(emptyCapStatus))
	})
})
