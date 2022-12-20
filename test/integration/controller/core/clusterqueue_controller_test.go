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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
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
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Reconciling clusterQueue status", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
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
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, clusterQueue)).To(gomega.Succeed())
		})

		ginkgo.It("Should update status when workloads are assigned and finish", func() {
			workloads := []*kueue.Workload{
				testing.MakeWorkload("one", ns.Name).Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "2").Request(resourceGPU, "2").Obj(),
				testing.MakeWorkload("two", ns.Name).Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "3").Request(resourceGPU, "3").Obj(),
				testing.MakeWorkload("three", ns.Name).Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				testing.MakeWorkload("four", ns.Name).Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				testing.MakeWorkload("five", ns.Name).Queue("other").
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				testing.MakeWorkload("six", ns.Name).Queue(localQueue.Name).
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
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads: 5,
				UsedResources:    emptyUsedResources,
			}))
			// Workloads are inadmissible because ResourceFlavors don't exist in this test suite.
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 5)
			util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 0)

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
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
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
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 4)

			ginkgo.By("Finishing workloads")
			util.FinishWorkloads(ctx, k8sClient, workloads...)
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				UsedResources: emptyUsedResources,
			}))
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 0)
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("foo-cq").Obj()
			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
		})

		ginkgo.It("Should delete clusterQueues successfully when no admitted workloads are running", func() {
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should be stuck in termination until admitted workloads finished running", func() {
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)

			ginkgo.By("Admit workload")
			admission := testing.MakeAdmission(cq.Name).Obj()
			wl := testing.MakeWorkload("workload", ns.Name).Queue(lq.Name).Admit(admission).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusTerminating)
			var newCQ kueue.ClusterQueue
			gomega.Eventually(func() []string {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				return newCQ.GetFinalizers()
			}, util.Timeout, util.Interval).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))

			ginkgo.By("Finish workload")
			util.FinishWorkloads(ctx, k8sClient, wl)

			ginkgo.By("The clusterQueue will be deleted")
			gomega.Eventually(func() error {
				var newCQ kueue.ClusterQueue
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)
			}, util.Timeout, util.Interval).Should(testing.BeNotFoundError())
		})
	})
})
