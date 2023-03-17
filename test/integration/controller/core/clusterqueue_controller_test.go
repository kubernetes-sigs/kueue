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
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
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
	flavorCPUArchA = "arch-a"
	flavorCPUArchB = "arch-b"
)

var ignoreConditionTimestamps = cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")

var _ = ginkgo.Describe("ClusterQueue controller", func() {
	var (
		ns               *corev1.Namespace
		emptyUsedFlavors = []kueue.FlavorUsage{
			{
				Name: flavorOnDemand,
				Resources: []kueue.ResourceUsage{
					{Name: corev1.ResourceCPU},
				},
			},
			{
				Name: flavorSpot,
				Resources: []kueue.ResourceUsage{
					{Name: corev1.ResourceCPU},
				},
			},
			{
				Name: flavorModelA,
				Resources: []kueue.ResourceUsage{
					{Name: resourceGPU},
				},
			},
			{
				Name: flavorModelB,
				Resources: []kueue.ResourceUsage{
					{Name: resourceGPU},
				},
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

	ginkgo.When("Reconciling clusterQueue usage status", func() {
		var (
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			onDemandFlavor *kueue.ResourceFlavor
			spotFlavor     *kueue.ResourceFlavor
			modelAFlavor   *kueue.ResourceFlavor
			modelBFlavor   *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas(flavorSpot).
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavorModelA).
						Resource(resourceGPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas(flavorModelB).
						Resource(resourceGPU, "5", "5").Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotFlavor, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, modelAFlavor, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, modelBFlavor, true)
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
				FlavorsUsage:     emptyUsedFlavors,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "FlavorNotFound",
						Message: "Can't admit new workloads; some flavors are not found",
					},
				},
			}, ignoreConditionTimestamps))
			// Workloads are inadmissible because ResourceFlavors don't exist here yet.
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 5)
			util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 0)

			ginkgo.By("Creating ResourceFlavors")
			onDemandFlavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			spotFlavor = testing.MakeResourceFlavor(flavorSpot).Obj()
			gomega.Expect(k8sClient.Create(ctx, spotFlavor)).To(gomega.Succeed())
			modelAFlavor = testing.MakeResourceFlavor(flavorModelA).Label(resourceGPU.String(), flavorModelA).Obj()
			gomega.Expect(k8sClient.Create(ctx, modelAFlavor)).To(gomega.Succeed())
			modelBFlavor = testing.MakeResourceFlavor(flavorModelB).Label(resourceGPU.String(), flavorModelB).Obj()
			gomega.Expect(k8sClient.Create(ctx, modelBFlavor)).To(gomega.Succeed())

			ginkgo.By("Admitting workloads")
			admissions := []*kueue.Admission{
				testing.MakeAdmission(clusterQueue.Name).
					Resource(corev1.ResourceCPU, "2").Resource(resourceGPU, "2").
					Flavor(corev1.ResourceCPU, flavorOnDemand).Flavor(resourceGPU, flavorModelA).Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Resource(corev1.ResourceCPU, "3").Resource(resourceGPU, "3").
					Flavor(corev1.ResourceCPU, flavorOnDemand).Flavor(resourceGPU, flavorModelA).Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Resource(corev1.ResourceCPU, "1").Resource(resourceGPU, "1").
					Flavor(corev1.ResourceCPU, flavorOnDemand).Flavor(resourceGPU, flavorModelB).Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Resource(corev1.ResourceCPU, "1").Resource(resourceGPU, "1").
					Flavor(corev1.ResourceCPU, flavorSpot).Flavor(resourceGPU, flavorModelB).Obj(),
				testing.MakeAdmission("other").
					Resource(corev1.ResourceCPU, "1").Resource(resourceGPU, "1").
					Flavor(corev1.ResourceCPU, flavorSpot).Flavor(resourceGPU, flavorModelB).Obj(),
				nil,
			}
			for i, w := range workloads {
				gomega.Eventually(func() error {
					var newWL kueue.Workload
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
					newWL.Status.Admission = admissions[i]
					return k8sClient.Status().Update(ctx, &newWL)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				return updatedCQ.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				PendingWorkloads:  1,
				AdmittedWorkloads: 4,
				FlavorsUsage: []kueue.FlavorUsage{
					{
						Name: flavorOnDemand,
						Resources: []kueue.ResourceUsage{{
							Name:     corev1.ResourceCPU,
							Total:    resource.MustParse("6"),
							Borrowed: resource.MustParse("1"),
						}},
					},
					{
						Name: flavorSpot,
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("1"),
						}},
					},
					{
						Name: flavorModelA,
						Resources: []kueue.ResourceUsage{{
							Name:  resourceGPU,
							Total: resource.MustParse("5"),
						}},
					},
					{
						Name: flavorModelB,
						Resources: []kueue.ResourceUsage{{
							Name:  resourceGPU,
							Total: resource.MustParse("2"),
						}},
					},
				},
				Conditions: []metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				},
			}, ignoreConditionTimestamps))
			util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 4)

			ginkgo.By("Finishing workloads")
			util.FinishWorkloads(ctx, k8sClient, workloads...)
			gomega.Eventually(func() kueue.ClusterQueueStatus {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
				FlavorsUsage: emptyUsedFlavors,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				},
			}, ignoreConditionTimestamps))
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(clusterQueue, 0)
		})
	})

	ginkgo.When("Reconciling clusterQueue status condition", func() {
		var (
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			wl             *kueue.Workload
			cpuArchAFlavor *kueue.ResourceFlavor
			cpuArchBFlavor *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("bar-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavorCPUArchA).Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas(flavorCPUArchB).Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			lq = testing.MakeLocalQueue("bar-lq", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
			wl = testing.MakeWorkload("bar-wl", ns.Name).Queue(lq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkload(ctx, k8sClient, wl)).To(gomega.Succeed())
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, lq)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, cpuArchAFlavor, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, cpuArchBFlavor, true)
		})

		ginkgo.It("Should update status conditions when flavors are created", func() {
			ginkgo.By("All Flavors are not found")

			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "FlavorNotFound",
					Message: "Can't admit new workloads; some flavors are not found",
				},
			}, ignoreConditionTimestamps))

			ginkgo.By("One of flavors is not found")
			cpuArchAFlavor = testing.MakeResourceFlavor(flavorCPUArchA).Obj()
			gomega.Expect(k8sClient.Create(ctx, cpuArchAFlavor)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionFalse,
					Reason:  "FlavorNotFound",
					Message: "Can't admit new workloads; some flavors are not found",
				},
			}, ignoreConditionTimestamps))

			ginkgo.By("All flavors are created")
			cpuArchBFlavor = testing.MakeResourceFlavor(flavorCPUArchB).Obj()
			gomega.Expect(k8sClient.Create(ctx, cpuArchBFlavor)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				var updatedCq kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				return updatedCq.Status.Conditions
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo([]metav1.Condition{
				{
					Type:    kueue.ClusterQueueActive,
					Status:  metav1.ConditionTrue,
					Reason:  "Ready",
					Message: "Can admit new workloads",
				},
			}, ignoreConditionTimestamps))
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
			wl := testing.MakeWorkload("workload", ns.Name).Queue(lq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			wl.Status.Admission = testing.MakeAdmission(cq.Name).Obj()
			gomega.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())

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
