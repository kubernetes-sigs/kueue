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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
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

var _ = ginkgo.Describe("ClusterQueue controller", func() {
	var (
		ns                 *corev1.Namespace
		emptyUsedResources = kueue.UsedResources{
			corev1.ResourceCPU: {
				flavorOnDemand: {Total: pointer.Quantity(resource.MustParse("0"))},
				flavorSpot:     {Total: pointer.Quantity(resource.MustParse("0"))},
			},
			resourceGPU: {
				flavorModelA: {Total: pointer.Quantity(resource.MustParse("0"))},
				flavorModelB: {Total: pointer.Quantity(resource.MustParse("0"))},
			},
		}
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-clusterqueue-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Reconciling clusterQueue status", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			queue        *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(flavorOnDemand, "5").Max("10").Obj()).
					Flavor(testing.MakeFlavor(flavorSpot, "5").Max("10").Obj()).Obj()).
				Resource(testing.MakeResource(resourceGPU).
					Flavor(testing.MakeFlavor(flavorModelA, "5").Max("10").Obj()).
					Flavor(testing.MakeFlavor(flavorModelB, "5").Max("10").Obj()).Obj()).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
		})

		ginkgo.It("Should update status when workloads are assigned and finish", func() {
			workloads := []*kueue.Workload{
				testing.MakeWorkload("one", ns.Name).Queue(queue.Name).
					Request(corev1.ResourceCPU, "2").Request(resourceGPU, "2").Obj(),
				testing.MakeWorkload("two", ns.Name).Queue(queue.Name).
					Request(corev1.ResourceCPU, "3").Request(resourceGPU, "3").Obj(),
				testing.MakeWorkload("three", ns.Name).Queue(queue.Name).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				testing.MakeWorkload("four", ns.Name).Queue(queue.Name).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				testing.MakeWorkload("five", ns.Name).Queue("other").
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				testing.MakeWorkload("six", ns.Name).Queue(queue.Name).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
			}

			ginkgo.By("Creating workloads")
			for _, w := range workloads {
				gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			}
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status
			}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.ClusterQueueStatus{
				PendingWorkloads: 5,
				UsedResources:    emptyUsedResources,
			}))
			framework.ExpectPendingWorkloadsMetric(clusterQueue, 5)
			framework.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 0)

			ginkgo.By("Admitting workloads")
			admissions := []*kueue.Admission{
				testing.MakeAdmission(clusterQueue.Name).
					Flavor(corev1.ResourceCPU, flavorOnDemand).Flavor(resourceGPU, flavorModelA).Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Flavor(corev1.ResourceCPU, flavorOnDemand).Flavor(resourceGPU, flavorModelA).Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Flavor(corev1.ResourceCPU, flavorOnDemand).Flavor(resourceGPU, flavorModelB).Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Flavor(corev1.ResourceCPU, flavorSpot).Flavor(resourceGPU, flavorModelB).Obj(),
				testing.MakeAdmission("other").
					Flavor(corev1.ResourceCPU, flavorSpot).Flavor(resourceGPU, flavorModelB).Obj(),
				nil,
			}
			for i, w := range workloads {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
					newWL.Spec.Admission = admissions[i]
					return k8sClient.Update(ctx, &newWL)
				}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
			}
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.ClusterQueueStatus{
				PendingWorkloads:  1,
				AdmittedWorkloads: 4,
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
			framework.ExpectPendingWorkloadsMetric(clusterQueue, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 4)

			ginkgo.By("Finishing workloads")
			for _, w := range workloads {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
					newWL.Status.Conditions = append(newWL.Status.Conditions, kueue.WorkloadCondition{
						Type:   kueue.WorkloadFinished,
						Status: corev1.ConditionTrue,
					})
					return k8sClient.Status().Update(ctx, &newWL)
				}, framework.Timeout, framework.Interval).Should(gomega.Succeed())
			}
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status
			}, framework.Timeout, framework.Interval).Should(testing.Equal(kueue.ClusterQueueStatus{
				UsedResources: emptyUsedResources,
			}))
			framework.ExpectPendingWorkloadsMetric(clusterQueue, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 0)
		})
	})

	ginkgo.When("Creating clusterQueues", func() {
		var cq *kueue.ClusterQueue
		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("foo-cq").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
		})

		ginkgo.It("Should add a finalizer to the clusterQueue", func() {
			gomega.Eventually(func() []string {
				var newCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				return newCQ.GetFinalizers()
			}, framework.Timeout, framework.Interval).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.Queue
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("foo-cq").Obj()
			queue = testing.MakeQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
		})

		ginkgo.It("Should delete clusterQueues successfully when no admitted workloads are running", func() {
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should stuck in termination until admitted workloads finished running", func() {
			ginkgo.By("Admit workloads")
			admission := testing.MakeAdmission(cq.Name).Obj()
			wl := testing.MakeWorkload("workload", ns.Name).Queue(queue.Name).Admit(admission).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			var newCQ kueue.ClusterQueue
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				return newCQ.GetFinalizers()
			}, framework.Timeout, framework.Interval).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))

			ginkgo.By("Finish workloads")
			gomega.Eventually(func() error {
				var newWL kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &newWL)).To(gomega.Succeed())
				newWL.Status.Conditions = append(newWL.Status.Conditions, kueue.WorkloadCondition{
					Type:   kueue.WorkloadFinished,
					Status: corev1.ConditionTrue,
				})
				return k8sClient.Status().Update(ctx, &newWL)
			}, framework.Timeout, framework.Interval).Should(gomega.BeNil())

			ginkgo.By("The clusterQueue will be deleted")
			gomega.Eventually(func() error {
				var newCQ kueue.ClusterQueue
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)
			}, framework.Timeout, framework.Interval).Should(testing.BeNotFoundError())
		})
	})
})
