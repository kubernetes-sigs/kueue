/*
Copyright The Kubernetes Authors.

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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	resourceGPU corev1.ResourceName = "example.com/gpu"

	flavorOnDemand = "on-demand"
	flavorSpot     = "spot"
	flavorModelA   = "model-a"
	flavorModelB   = "model-b"
	flavorCPUArchA = "arch-a"
	flavorCPUArchB = "arch-b"
)

var ignoreLastChangeTime = cmpopts.IgnoreFields(kueue.ClusterQueuePendingWorkloadsStatus{}, "LastChangeTime")
var ignorePendingWorkloadsStatus = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "PendingWorkloadsStatus")

var _ = ginkgo.Describe("ClusterQueue controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-clusterqueue-")
	})

	ginkgo.BeforeEach(func() {
		gomega.Expect(features.SetEnable(features.LocalQueueMetrics, true)).To(gomega.Succeed())
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
			ac             *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			ac = testing.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, ac)
			util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

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
				Cohort("cohort").
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, modelAFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, modelBFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		})

		ginkgo.It("Should update status and report metrics when workloads are assigned and finish", framework.SlowSpec, func() {
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

			ginkgo.By("Checking that the resource metrics are published", func() {
				util.ExpectCQResourceNominalQuota(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 5)
				util.ExpectCQResourceNominalQuota(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 5)
				util.ExpectCQResourceNominalQuota(clusterQueue, flavorModelA, string(resourceGPU), 5)
				util.ExpectCQResourceNominalQuota(clusterQueue, flavorModelB, string(resourceGPU), 5)

				util.ExpectCQResourceBorrowingQuota(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 5)
				util.ExpectCQResourceBorrowingQuota(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 5)
				util.ExpectCQResourceBorrowingQuota(clusterQueue, flavorModelA, string(resourceGPU), 5)
				util.ExpectCQResourceBorrowingQuota(clusterQueue, flavorModelB, string(resourceGPU), 5)

				util.ExpectCQResourceReservations(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 0)
				util.ExpectCQResourceReservations(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 0)
				util.ExpectCQResourceReservations(clusterQueue, flavorModelA, string(resourceGPU), 0)
				util.ExpectCQResourceReservations(clusterQueue, flavorModelB, string(resourceGPU), 0)
			})

			ginkgo.By("Creating workloads")
			for _, w := range workloads {
				util.MustCreate(ctx, k8sClient, w)
			}
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   5,
					FlavorsReservation: emptyUsedFlavors,
					FlavorsUsage:       emptyUsedFlavors,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.ClusterQueueActive,
							Status:  metav1.ConditionFalse,
							Reason:  "FlavorNotFound",
							Message: "Can't admit new workloads: references missing ResourceFlavor(s): [on-demand spot model-a model-b].",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			// Workloads are inadmissible because ResourceFlavors don't exist here yet.
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 5)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			util.ExpectLQPendingWorkloadsMetric(localQueue, 0, 5)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)

			ginkgo.By("Creating ResourceFlavors")
			onDemandFlavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, onDemandFlavor)
			spotFlavor = testing.MakeResourceFlavor(flavorSpot).Obj()
			util.MustCreate(ctx, k8sClient, spotFlavor)
			modelAFlavor = testing.MakeResourceFlavor(flavorModelA).NodeLabel(resourceGPU.String(), flavorModelA).Obj()
			util.MustCreate(ctx, k8sClient, modelAFlavor)
			modelBFlavor = testing.MakeResourceFlavor(flavorModelB).NodeLabel(resourceGPU.String(), flavorModelB).Obj()
			util.MustCreate(ctx, k8sClient, modelBFlavor)

			ginkgo.By("Set workloads quota reservation")
			admissions := []*kueue.Admission{
				testing.MakeAdmission(clusterQueue.Name).
					Assignment(corev1.ResourceCPU, flavorOnDemand, "2").Assignment(resourceGPU, flavorModelA, "2").Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Assignment(corev1.ResourceCPU, flavorOnDemand, "3").Assignment(resourceGPU, flavorModelA, "3").Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Assignment(corev1.ResourceCPU, flavorOnDemand, "1").Assignment(resourceGPU, flavorModelB, "1").Obj(),
				testing.MakeAdmission(clusterQueue.Name).
					Assignment(corev1.ResourceCPU, flavorSpot, "1").Assignment(resourceGPU, flavorModelB, "1").Obj(),
				testing.MakeAdmission("other").
					Assignment(corev1.ResourceCPU, flavorSpot, "1").Assignment(resourceGPU, flavorModelB, "1").Obj(),
				nil,
			}
			for i, w := range workloads {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
					if admissions[i] != nil {
						g.Expect(util.SetQuotaReservation(ctx, k8sClient, &newWL, admissions[i])).Should(gomega.Succeed())
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			totalUsage := []kueue.FlavorUsage{
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
			}

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   1,
					ReservingWorkloads: 4,
					AdmittedWorkloads:  0,
					FlavorsReservation: totalUsage,
					FlavorsUsage:       emptyUsedFlavors,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.ClusterQueueActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Ready",
							Message: "Can admit new workloads",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 4)
			util.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 4)

			ginkgo.By("Checking the resource reservation metrics are updated", func() {
				util.ExpectCQResourceReservations(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 6)
				util.ExpectCQResourceReservations(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 1)
				util.ExpectCQResourceReservations(clusterQueue, flavorModelA, string(resourceGPU), 5)
				util.ExpectCQResourceReservations(clusterQueue, flavorModelB, string(resourceGPU), 2)
			})

			ginkgo.By("Setting the admission check for the first 4 workloads")
			for _, w := range workloads[:4] {
				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
			}

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   1,
					ReservingWorkloads: 4,
					AdmittedWorkloads:  4,
					FlavorsReservation: totalUsage,
					FlavorsUsage:       totalUsage,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.ClusterQueueActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Ready",
							Message: "Can admit new workloads",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 4)
			util.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 4)

			ginkgo.By("Checking the resource usage metrics are updated", func() {
				util.ExpectCQResourceReservations(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 6)
				util.ExpectCQResourceReservations(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 1)
				util.ExpectCQResourceReservations(clusterQueue, flavorModelA, string(resourceGPU), 5)
				util.ExpectCQResourceReservations(clusterQueue, flavorModelB, string(resourceGPU), 2)
			})

			ginkgo.By("Finishing workloads")
			util.FinishWorkloads(ctx, k8sClient, workloads...)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					FlavorsReservation: emptyUsedFlavors,
					FlavorsUsage:       emptyUsedFlavors,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.ClusterQueueActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Ready",
							Message: "Can admit new workloads",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			util.ExpectLQPendingWorkloadsMetric(localQueue, 0, 0)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)
		})

		ginkgo.It("Should update status and report metrics when a pending workload is deleted", func() {
			workload := testing.MakeWorkload("one", ns.Name).Queue(localQueue.Name).
				Request(corev1.ResourceCPU, "5").Obj()

			ginkgo.By("Creating a workload", func() {
				util.MustCreate(ctx, k8sClient, workload)
			})

			// Pending workloads count is incremented as the workload is inadmissible
			// because ResourceFlavors don't exist.
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   1,
					FlavorsReservation: emptyUsedFlavors,
					FlavorsUsage:       emptyUsedFlavors,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.ClusterQueueActive,
							Status:  metav1.ConditionFalse,
							Reason:  "FlavorNotFound",
							Message: "Can't admit new workloads: references missing ResourceFlavor(s): [on-demand spot model-a model-b].",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			util.ExpectLQPendingWorkloadsMetric(localQueue, 0, 1)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)

			ginkgo.By("Deleting the pending workload", func() {
				gomega.Expect(k8sClient.Delete(ctx, workload)).To(gomega.Succeed())
			})

			// Pending workloads count is decrement as the deleted workload has been removed from the queue.
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					FlavorsReservation: emptyUsedFlavors,
					FlavorsUsage:       emptyUsedFlavors,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.ClusterQueueActive,
							Status:  metav1.ConditionFalse,
							Reason:  "FlavorNotFound",
							Message: "Can't admit new workloads: references missing ResourceFlavor(s): [on-demand spot model-a model-b].",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			util.ExpectLQPendingWorkloadsMetric(localQueue, 0, 0)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)
		})

		ginkgo.It("Should update status when workloads have reclaimable pods", framework.SlowSpec, func() {
			ginkgo.By("Creating ResourceFlavors", func() {
				onDemandFlavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
				util.MustCreate(ctx, k8sClient, onDemandFlavor)
				spotFlavor = testing.MakeResourceFlavor(flavorSpot).Obj()
				util.MustCreate(ctx, k8sClient, spotFlavor)
				modelAFlavor = testing.MakeResourceFlavor(flavorModelA).NodeLabel(resourceGPU.String(), flavorModelA).Obj()
				util.MustCreate(ctx, k8sClient, modelAFlavor)
				modelBFlavor = testing.MakeResourceFlavor(flavorModelB).NodeLabel(resourceGPU.String(), flavorModelB).Obj()
				util.MustCreate(ctx, k8sClient, modelBFlavor)
			})

			wl := testing.MakeWorkload("one", ns.Name).
				Queue(localQueue.Name).
				PodSets(
					*testing.MakePodSet("driver", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*testing.MakePodSet("workers", 5).
						Request(resourceGPU, "1").
						Obj(),
				).
				Obj()
			ginkgo.By("Creating the workload", func() {
				util.MustCreate(ctx, k8sClient, wl)
				util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
				util.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			})

			ginkgo.By("Admitting the workload", func() {
				admission := testing.MakeAdmission(clusterQueue.Name).PodSets(
					kueue.PodSetAssignment{
						Name: "driver",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						ResourceUsage: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: ptr.To[int32](2),
					},
					kueue.PodSetAssignment{
						Name: "workers",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							resourceGPU: "model-a",
						},
						ResourceUsage: corev1.ResourceList{
							resourceGPU: resource.MustParse("5"),
						},
						Count: ptr.To[int32](5),
					},
				).Obj()

				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &newWL)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &newWL, admission)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
			util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{
					{
						Name: flavorOnDemand,
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("2"),
						}},
					},
					{
						Name: flavorSpot,
						Resources: []kueue.ResourceUsage{{
							Name: corev1.ResourceCPU,
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
							Name: resourceGPU,
						}},
					},
				}, util.IgnoreConditionTimestamps))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Mark two workers as reclaimable", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{{Name: "workers", Count: 2}})).To(gomega.Succeed())

				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
					g.Expect(updatedCq.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{
						{
							Name: flavorOnDemand,
							Resources: []kueue.ResourceUsage{{
								Name:  corev1.ResourceCPU,
								Total: resource.MustParse("2"),
							}},
						},
						{
							Name: flavorSpot,
							Resources: []kueue.ResourceUsage{{
								Name: corev1.ResourceCPU,
							}},
						},
						{
							Name: flavorModelA,
							Resources: []kueue.ResourceUsage{{
								Name:  resourceGPU,
								Total: resource.MustParse("3"),
							}},
						},
						{
							Name: flavorModelB,
							Resources: []kueue.ResourceUsage{{
								Name: resourceGPU,
							}},
						},
					}, util.IgnoreConditionTimestamps))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Mark all workers and a driver as reclaimable", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{{Name: "workers", Count: 5}, {Name: "driver", Count: 1}})).To(gomega.Succeed())

				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
					g.Expect(updatedCq.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{
						{
							Name: flavorOnDemand,
							Resources: []kueue.ResourceUsage{{
								Name:  corev1.ResourceCPU,
								Total: resource.MustParse("1"),
							}},
						},
						{
							Name: flavorSpot,
							Resources: []kueue.ResourceUsage{{
								Name: corev1.ResourceCPU,
							}},
						},
						{
							Name: flavorModelA,
							Resources: []kueue.ResourceUsage{{
								Name: resourceGPU,
							}},
						},
						{
							Name: flavorModelB,
							Resources: []kueue.ResourceUsage{{
								Name: resourceGPU,
							}},
						},
					}, util.IgnoreConditionTimestamps))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing workload", func() {
				util.FinishWorkloads(ctx, k8sClient, wl)
				util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
				util.ExpectLQPendingWorkloadsMetric(localQueue, 0, 0)
				util.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)
			})
		})
	})

	ginkgo.When("Reconciling clusterQueue status condition", func() {
		var (
			cq             *kueue.ClusterQueue
			lq             *kueue.LocalQueue
			wl             *kueue.Workload
			cpuArchAFlavor *kueue.ResourceFlavor
			cpuArchBFlavor *kueue.ResourceFlavor
			check1         *kueue.AdmissionCheck
			check2         *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("bar-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavorCPUArchA).Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas(flavorCPUArchB).Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				Cohort("bar-cohort").
				AdmissionChecks("check1", "check2").
				Obj()

			util.MustCreate(ctx, k8sClient, cq)
			lq = testing.MakeLocalQueue("bar-lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			wl = testing.MakeWorkload("bar-wl", ns.Name).Queue(lq.Name).Obj()
			util.MustCreate(ctx, k8sClient, wl)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, wl)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, lq)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cpuArchAFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cpuArchBFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, check1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, check2, true)
		})

		ginkgo.It("Should update status conditions when flavors are created", func() {
			check1 = testing.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, check1)
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = testing.MakeAdmissionCheck("check2").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, check2)
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			ginkgo.By("All Flavors are not found")

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "FlavorNotFound",
						Message: "Can't admit new workloads: references missing ResourceFlavor(s): [arch-a arch-b].",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("One of flavors is not found")
			cpuArchAFlavor = testing.MakeResourceFlavor(flavorCPUArchA).Obj()
			util.MustCreate(ctx, k8sClient, cpuArchAFlavor)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "FlavorNotFound",
						Message: "Can't admit new workloads: references missing ResourceFlavor(s): [arch-b].",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("All flavors are created")
			cpuArchBFlavor = testing.MakeResourceFlavor(flavorCPUArchB).Obj()
			util.MustCreate(ctx, k8sClient, cpuArchBFlavor)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should update status conditions when admission checks are created", func() {
			cpuArchAFlavor = testing.MakeResourceFlavor(flavorCPUArchA).Obj()
			util.MustCreate(ctx, k8sClient, cpuArchAFlavor)

			cpuArchBFlavor = testing.MakeResourceFlavor(flavorCPUArchB).Obj()
			util.MustCreate(ctx, k8sClient, cpuArchBFlavor)

			ginkgo.By("All checks are not found")

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "AdmissionCheckNotFound",
						Message: "Can't admit new workloads: references missing AdmissionCheck(s): [check1 check2].",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("One of the checks is not found")
			check1 = testing.MakeAdmissionCheck("check1").ControllerName("ac-controller").Active(metav1.ConditionTrue).Obj()
			util.MustCreate(ctx, k8sClient, check1)
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "AdmissionCheckNotFound",
						Message: "Can't admit new workloads: references missing AdmissionCheck(s): [check2].",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("One check is inactive")
			check2 = testing.MakeAdmissionCheck("check2").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, check2)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "AdmissionCheckInactive",
						Message: "Can't admit new workloads: references inactive AdmissionCheck(s): [check2].",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("All checks are created")
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should prevent workload admission due to multikueue contraints", func() {
			cpuArchAFlavor = testing.MakeResourceFlavor(flavorCPUArchA).Obj()
			util.MustCreate(ctx, k8sClient, cpuArchAFlavor)

			cpuArchBFlavor = testing.MakeResourceFlavor(flavorCPUArchB).Obj()
			util.MustCreate(ctx, k8sClient, cpuArchBFlavor)

			check1 = testing.MakeAdmissionCheck("check1").ControllerName(kueue.MultiKueueControllerName).Obj()
			util.MustCreate(ctx, k8sClient, check1)
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = testing.MakeAdmissionCheck("check2").ControllerName(kueue.MultiKueueControllerName).Obj()
			util.MustCreate(ctx, k8sClient, check2)
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			ginkgo.By("Multiple MultiKueue admission checks for the same cluster queue")

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "MultipleMultiKueueAdmissionChecks",
						Message: `Can't admit new workloads: Cannot use multiple MultiKueue AdmissionChecks on the same ClusterQueue, found: check1,check2.`,
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Only one MultiKueue flavor dependent admission check assigned to cluster queue")
			gomega.Eventually(func(g gomega.Gomega) {
				updatedCq := &kueue.ClusterQueue{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), updatedCq)).Should(gomega.Succeed())
				updatedCq.Spec.AdmissionChecks = nil
				updatedCq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: []kueue.AdmissionCheckStrategyRule{
						*testing.MakeAdmissionCheckStrategyRule("check1", flavorCPUArchA).Obj(),
					},
				}
				g.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "MultiKueueAdmissionCheckAppliedPerFlavor",
						Message: `Can't admit new workloads: Cannot specify MultiKueue AdmissionCheck per flavor, found: check1.`,
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Only one MultiKueue flavor independent admission check assigned to cluster queue")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedAc kueue.AdmissionCheck
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(check2), &updatedAc)).Should(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, &updatedAc)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).Should(gomega.Succeed())
				updatedCq.Spec.AdmissionChecks = []kueue.AdmissionCheckReference{"check1"}
				updatedCq.Spec.AdmissionChecksStrategy = nil
				g.Expect(k8sClient.Update(ctx, &updatedCq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq    *kueue.ClusterQueue
			lq    *kueue.LocalQueue
			check *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			check = testing.MakeAdmissionCheck("check").ControllerName("check-controller").Obj()
			util.MustCreate(ctx, k8sClient, check)

			cq = testing.MakeClusterQueue("foo-cq").AdmissionChecks(kueue.AdmissionCheckReference(check.Name)).Obj()
			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.MustCreate(ctx, k8sClient, cq)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
		})

		ginkgo.It("Should delete clusterQueues successfully when no admitted workloads are running", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should be stuck in termination until admitted workloads finished running", func() {
			util.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)
			util.ExpectLQByStatusMetric(lq, metav1.ConditionTrue)

			ginkgo.By("Admit workload")
			wl := testing.MakeWorkload("workload", ns.Name).Queue(lq.Name).Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, testing.MakeAdmission(cq.Name).Obj())).To(gomega.Succeed())
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, true)
			gomega.Eventually(func(g gomega.Gomega) {
				key := client.ObjectKeyFromObject(wl)
				updatedWl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, key, updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(util.DeleteObject(ctx, k8sClient, cq)).To(gomega.Succeed())
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusTerminating)
			util.ExpectLQByStatusMetric(lq, metav1.ConditionTrue)
			var newCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				g.Expect(newCQ.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finish workload")
			util.FinishWorkloads(ctx, k8sClient, wl)

			ginkgo.By("The clusterQueue will be deleted")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, false)
			util.ExpectLQByStatusMetric(lq, metav1.ConditionFalse)
		})

		ginkgo.It("Should delete the cluster without waiting for reserving only workloads to finish", func() {
			util.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)
			util.ExpectLQByStatusMetric(lq, metav1.ConditionTrue)

			ginkgo.By("Setting quota reservation")
			wl := testing.MakeWorkload("workload", ns.Name).Queue(lq.Name).Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, testing.MakeAdmission(cq.Name).Obj())).To(gomega.Succeed())

			ginkgo.By("Delete clusterQueue")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})
	})
})

var _ = ginkgo.Describe("ClusterQueue controller with queue visibility is enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		ginkgo.By("Enabling queue visibility feature", func() {
			gomega.Expect(features.SetEnable(features.QueueVisibility, true)).To(gomega.Succeed())
		})
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-clusterqueue-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Reconciling clusterQueue pending workload status", func() {
		var (
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			onDemandFlavor *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			onDemandFlavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, onDemandFlavor)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				Cohort("cohort").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should update of the pending workloads when a new workload is scheduled", framework.SlowSpec, func() {
			const lowPrio, midLowerPrio, midHigherPrio, highPrio = 0, 10, 20, 100
			workloadsFirstBatch := []*kueue.Workload{
				testing.MakeWorkload("one", ns.Name).Queue(localQueue.Name).Priority(highPrio).
					Request(corev1.ResourceCPU, "2").Request(resourceGPU, "2").Obj(),
				testing.MakeWorkload("two", ns.Name).Queue(localQueue.Name).Priority(midHigherPrio).
					Request(corev1.ResourceCPU, "3").Request(resourceGPU, "3").Obj(),
			}

			ginkgo.By("Verify pending workload status before adding workloads")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.PendingWorkloadsStatus).Should(gomega.BeComparableTo(&kueue.ClusterQueuePendingWorkloadsStatus{}, ignoreLastChangeTime))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating workloads")
			for _, w := range workloadsFirstBatch {
				util.MustCreate(ctx, k8sClient, w)
			}

			ginkgo.By("Awaiting for the pending workloads to be updated")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.PendingWorkloadsStatus).Should(gomega.BeComparableTo(&kueue.ClusterQueuePendingWorkloadsStatus{
					Head: []kueue.ClusterQueuePendingWorkload{
						{
							Name:      "one",
							Namespace: ns.Name,
						},
						{
							Name:      "two",
							Namespace: ns.Name,
						},
					},
				}, ignoreLastChangeTime))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating new workloads to so that the number of workloads exceeds the MaxCount")
			workloadsSecondBatch := []*kueue.Workload{
				testing.MakeWorkload("three", ns.Name).Queue(localQueue.Name).Priority(midLowerPrio).
					Request(corev1.ResourceCPU, "2").Request(resourceGPU, "2").Obj(),
				testing.MakeWorkload("four", ns.Name).Queue(localQueue.Name).Priority(lowPrio).
					Request(corev1.ResourceCPU, "3").Request(resourceGPU, "3").Obj(),
			}
			for _, w := range workloadsSecondBatch {
				util.MustCreate(ctx, k8sClient, w)
			}

			ginkgo.By("Verify the head of pending workloads when the number of pending workloads exceeds MaxCount")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.PendingWorkloadsStatus).Should(gomega.BeComparableTo(&kueue.ClusterQueuePendingWorkloadsStatus{
					Head: []kueue.ClusterQueuePendingWorkload{
						{
							Name:      "one",
							Namespace: ns.Name,
						},
						{
							Name:      "two",
							Namespace: ns.Name,
						},
						{
							Name:      "three",
							Namespace: ns.Name,
						},
					},
				}, ignoreLastChangeTime))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Admitting workloads")
			for _, w := range workloadsFirstBatch {
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &newWL, testing.MakeAdmission(clusterQueue.Name).Obj())).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("Awaiting for the pending workloads status to be updated after the workloads are admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status.PendingWorkloadsStatus).Should(gomega.BeComparableTo(&kueue.ClusterQueuePendingWorkloadsStatus{
					Head: []kueue.ClusterQueuePendingWorkload{
						{
							Name:      "three",
							Namespace: ns.Name,
						},
						{
							Name:      "four",
							Namespace: ns.Name,
						},
					},
				}, ignoreLastChangeTime))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing workload", func() {
				util.FinishWorkloads(ctx, k8sClient, workloadsFirstBatch...)
				util.FinishWorkloads(ctx, k8sClient, workloadsSecondBatch...)
			})

			ginkgo.By("Awaiting for the pending workloads status to be updated after the workloads are finished")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.PendingWorkloadsStatus).Should(gomega.BeComparableTo(&kueue.ClusterQueuePendingWorkloadsStatus{
					Head: []kueue.ClusterQueuePendingWorkload{},
				}, ignoreLastChangeTime))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
