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
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
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

var _ = ginkgo.Describe("ClusterQueue controller", ginkgo.Label("controller:clusterqueue", "area:core"), func() {
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
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
		fwk.StartManager(ctx, cfg, managerSetup)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-clusterqueue-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
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
			ac = utiltestingapi.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, ac)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorSpot).
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorModelA).
						Resource(resourceGPU, "5", "5").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorModelB).
						Resource(resourceGPU, "5", "5").Obj(),
				).
				Cohort("cohort").
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, spotFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, modelAFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, modelBFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		})

		ginkgo.It("Should update status and report metrics when workloads are assigned and finish", framework.SlowSpec, func() {
			workloads := []*kueue.Workload{
				utiltestingapi.MakeWorkload("one", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "2").Request(resourceGPU, "2").Obj(),
				utiltestingapi.MakeWorkload("two", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "3").Request(resourceGPU, "3").Obj(),
				utiltestingapi.MakeWorkload("three", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				utiltestingapi.MakeWorkload("four", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				utiltestingapi.MakeWorkload("five", ns.Name).Queue("other").
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
				utiltestingapi.MakeWorkload("six", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").Request(resourceGPU, "1").Obj(),
			}

			ginkgo.By("Checking that the resource metrics are published", func() {
				behavioral.ExpectCQResourceNominalQuota(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 5)
				behavioral.ExpectCQResourceNominalQuota(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 5)
				behavioral.ExpectCQResourceNominalQuota(clusterQueue, flavorModelA, string(resourceGPU), 5)
				behavioral.ExpectCQResourceNominalQuota(clusterQueue, flavorModelB, string(resourceGPU), 5)

				behavioral.ExpectCQResourceBorrowingQuota(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 5)
				behavioral.ExpectCQResourceBorrowingQuota(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 5)
				behavioral.ExpectCQResourceBorrowingQuota(clusterQueue, flavorModelA, string(resourceGPU), 5)
				behavioral.ExpectCQResourceBorrowingQuota(clusterQueue, flavorModelB, string(resourceGPU), 5)

				behavioral.ExpectCQResourceReservations(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 0)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 0)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorModelA, string(resourceGPU), 0)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorModelB, string(resourceGPU), 0)
			})

			ginkgo.By("Creating workloads")
			for _, w := range workloads {
				behavioral.MustCreate(ctx, k8sClient, w)
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
							Message: "Can't admit new workloads: references missing ResourceFlavor(s): on-demand,spot,model-a,model-b.",
						},
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			// Workloads are inadmissible because ResourceFlavors don't exist here yet.
			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 5)
			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 0, 5)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)

			ginkgo.By("Checking the resource pending metrics reflect all pending workloads (cpu: 2+3+1+1+1=8, gpu: 2+3+1+1+1=8)", func() {
				// workloads one/two/three/four/six are in this CQ; "five" uses an unknown queue
				behavioral.ExpectCQResourcePendingMetric(clusterQueue, string(corev1.ResourceCPU), gomega.Equal(8.0))
				behavioral.ExpectCQResourcePendingMetric(clusterQueue, string(resourceGPU), gomega.Equal(8.0))
			})

			ginkgo.By("Creating ResourceFlavors")
			onDemandFlavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)
			spotFlavor = utiltestingapi.MakeResourceFlavor(flavorSpot).Obj()
			behavioral.MustCreate(ctx, k8sClient, spotFlavor)
			modelAFlavor = utiltestingapi.MakeResourceFlavor(flavorModelA).NodeLabel(resourceGPU.String(), flavorModelA).Obj()
			behavioral.MustCreate(ctx, k8sClient, modelAFlavor)
			modelBFlavor = utiltestingapi.MakeResourceFlavor(flavorModelB).NodeLabel(resourceGPU.String(), flavorModelB).Obj()
			behavioral.MustCreate(ctx, k8sClient, modelBFlavor)

			ginkgo.By("Set workloads quota reservation")
			admissions := []*kueue.Admission{
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, flavorOnDemand, "2").Assignment(resourceGPU, flavorModelA, "2").Obj()).Obj(),
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, flavorOnDemand, "3").Assignment(resourceGPU, flavorModelA, "3").Obj()).Obj(),
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, flavorOnDemand, "1").Assignment(resourceGPU, flavorModelB, "1").Obj()).Obj(),
				utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, flavorSpot, "1").Assignment(resourceGPU, flavorModelB, "1").Obj()).Obj(),
				utiltestingapi.MakeAdmission("other").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, flavorSpot, "1").Assignment(resourceGPU, flavorModelB, "1").Obj()).Obj(),
				nil,
			}
			for i, w := range workloads {
				gomega.Eventually(func(g gomega.Gomega) {
					if admissions[i] != nil {
						behavioral.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(w), admissions[i])
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 4)
			behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 4)

			ginkgo.By("Checking the resource pending metrics reflect only the remaining pending workload (\"six\": cpu=1, gpu=1)", func() {
				behavioral.ExpectCQResourcePendingMetric(clusterQueue, string(corev1.ResourceCPU), gomega.Equal(1.0))
				behavioral.ExpectCQResourcePendingMetric(clusterQueue, string(resourceGPU), gomega.Equal(1.0))
			})

			ginkgo.By("Checking the resource reservation metrics are updated", func() {
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 6)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 1)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorModelA, string(resourceGPU), 5)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorModelB, string(resourceGPU), 2)
			})

			ginkgo.By("Setting the admission check for the first 4 workloads")
			for _, w := range workloads[:4] {
				behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 4)
			behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 4)

			ginkgo.By("Checking the resource usage metrics are updated", func() {
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorOnDemand, string(corev1.ResourceCPU), 6)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorSpot, string(corev1.ResourceCPU), 1)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorModelA, string(resourceGPU), 5)
				behavioral.ExpectCQResourceReservations(clusterQueue, flavorModelB, string(resourceGPU), 2)
			})

			ginkgo.By("Finishing workloads")
			behavioral.FinishWorkloads(ctx, k8sClient, workloads...)
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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 0, 0)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)
		})

		ginkgo.It("Should update status and report metrics when a pending workload is deleted", func() {
			workload := utiltestingapi.MakeWorkload("one", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "5").Obj()

			ginkgo.By("Creating a workload", func() {
				behavioral.MustCreate(ctx, k8sClient, workload)
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
							Message: "Can't admit new workloads: references missing ResourceFlavor(s): on-demand,spot,model-a,model-b.",
						},
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 0, 1)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)

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
							Message: "Can't admit new workloads: references missing ResourceFlavor(s): on-demand,spot,model-a,model-b.",
						},
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
			behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 0, 0)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)
		})

		ginkgo.It("Should update pending resource metric when a pending workload's effective resources change", func() {
			wl := utiltestingapi.MakeWorkload("one", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Request(corev1.ResourceCPU, "2").Obj()).
				Obj()

			ginkgo.By("Creating a workload with 3 pods × 2 CPU = 6 CPU pending")
			behavioral.MustCreate(ctx, k8sClient, wl)

			// Workloads are inadmissible because ResourceFlavors don't exist yet.
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.PendingWorkloads).To(gomega.Equal(int32(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Checking initial pending resource metric shows 6 CPU")
			behavioral.ExpectCQResourcePendingMetric(clusterQueue, string(corev1.ResourceCPU), gomega.Equal(6.0))

			ginkgo.By("Setting 1 pod as reclaimable, reducing effective request to 4 CPU")
			behavioral.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{
				{Name: kueue.DefaultPodSetName, Count: 1},
			})

			ginkgo.By("Checking pending resource metric updates to reflect reduced request of 4 CPU")
			behavioral.ExpectCQResourcePendingMetric(clusterQueue, string(corev1.ResourceCPU), gomega.Equal(4.0))
		})

		ginkgo.It("Should update status when workloads have reclaimable pods", framework.SlowSpec, func() {
			ginkgo.By("Creating ResourceFlavors", func() {
				onDemandFlavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
				behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)
				spotFlavor = utiltestingapi.MakeResourceFlavor(flavorSpot).Obj()
				behavioral.MustCreate(ctx, k8sClient, spotFlavor)
				modelAFlavor = utiltestingapi.MakeResourceFlavor(flavorModelA).NodeLabel(resourceGPU.String(), flavorModelA).Obj()
				behavioral.MustCreate(ctx, k8sClient, modelAFlavor)
				modelBFlavor = utiltestingapi.MakeResourceFlavor(flavorModelB).NodeLabel(resourceGPU.String(), flavorModelB).Obj()
				behavioral.MustCreate(ctx, k8sClient, modelBFlavor)
			})

			wl := utiltestingapi.MakeWorkload("one", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(
					*utiltestingapi.MakePodSet("driver", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*utiltestingapi.MakePodSet("workers", 5).
						Request(resourceGPU, "1").
						Obj(),
				).
				Obj()
			ginkgo.By("Creating the workload", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
				behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
				behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			})

			ginkgo.By("Admitting the workload", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).PodSets(
					kueue.PodSetAssignment{
						Name: "driver",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						ResourceUsage: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: new(int32(2)),
					},
					kueue.PodSetAssignment{
						Name: "workers",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							resourceGPU: "model-a",
						},
						ResourceUsage: corev1.ResourceList{
							resourceGPU: resource.MustParse("5"),
						},
						Count: new(int32(5)),
					},
				).Obj()

				behavioral.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), admission)
			})

			behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
			behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
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
				}, behavioral.IgnoreConditionTimestamps))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Mark two workers as reclaimable", func() {
				behavioral.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{{Name: "workers", Count: 2}})
				behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
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
					}, behavioral.IgnoreConditionTimestamps))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Mark all workers and a driver as reclaimable", func() {
				reclaimablePods := []kueue.ReclaimablePod{{Name: "workers", Count: 5}, {Name: "driver", Count: 1}}
				behavioral.UpdateReclaimablePods(ctx, k8sClient, wl, reclaimablePods)
				behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
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
					}, behavioral.IgnoreConditionTimestamps))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing workload", func() {
				behavioral.FinishWorkloads(ctx, k8sClient, wl)
				behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
				behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 0, 0)
				behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 0)
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
			cq = utiltestingapi.MakeClusterQueue("bar-cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorCPUArchA).Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorCPUArchB).Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				Cohort("bar-cohort").
				AdmissionChecks("check1", "check2").
				Obj()

			behavioral.MustCreate(ctx, k8sClient, cq)
			lq = utiltestingapi.MakeLocalQueue("bar-lq", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)
			wl = utiltestingapi.MakeWorkload("bar-wl", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, wl)).To(gomega.Succeed())
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, lq)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cpuArchAFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cpuArchBFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check1, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check2, true)
		})

		ginkgo.It("Should update status conditions when flavors are created", framework.SlowSpec, func() {
			check1 = utiltestingapi.MakeAdmissionCheck("check1").ControllerName("ac-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, check1)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = utiltestingapi.MakeAdmissionCheck("check2").ControllerName("ac-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, check2)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			ginkgo.By("All Flavors are not found")

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "FlavorNotFound",
						Message: "Can't admit new workloads: references missing ResourceFlavor(s): arch-a,arch-b.",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("One of flavors is not found")
			cpuArchAFlavor = utiltestingapi.MakeResourceFlavor(flavorCPUArchA).Obj()
			behavioral.MustCreate(ctx, k8sClient, cpuArchAFlavor)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "FlavorNotFound",
						Message: "Can't admit new workloads: references missing ResourceFlavor(s): arch-b.",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("All flavors are created")
			cpuArchBFlavor = utiltestingapi.MakeResourceFlavor(flavorCPUArchB).Obj()
			behavioral.MustCreate(ctx, k8sClient, cpuArchBFlavor)
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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should update status conditions when admission checks are created", framework.SlowSpec, func() {
			cpuArchAFlavor = utiltestingapi.MakeResourceFlavor(flavorCPUArchA).Obj()
			behavioral.MustCreate(ctx, k8sClient, cpuArchAFlavor)

			cpuArchBFlavor = utiltestingapi.MakeResourceFlavor(flavorCPUArchB).Obj()
			behavioral.MustCreate(ctx, k8sClient, cpuArchBFlavor)

			ginkgo.By("All checks are not found")

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "AdmissionCheckNotFound",
						Message: "Can't admit new workloads: references missing AdmissionCheck(s): check1,check2.",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("One of the checks is not found")
			check1 = utiltestingapi.MakeAdmissionCheck("check1").ControllerName("ac-controller").Active(metav1.ConditionTrue).Obj()
			behavioral.MustCreate(ctx, k8sClient, check1)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "AdmissionCheckNotFound",
						Message: "Can't admit new workloads: references missing AdmissionCheck(s): check2.",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("One check is inactive")
			check2 = utiltestingapi.MakeAdmissionCheck("check2").ControllerName("ac-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, check2)
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "AdmissionCheckInactive",
						Message: "Can't admit new workloads: references inactive AdmissionCheck(s): check2.",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("All checks are created")
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)
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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should prevent workload admission due to multikueue contraints", func() {
			cpuArchAFlavor = utiltestingapi.MakeResourceFlavor(flavorCPUArchA).Obj()
			behavioral.MustCreate(ctx, k8sClient, cpuArchAFlavor)

			cpuArchBFlavor = utiltestingapi.MakeResourceFlavor(flavorCPUArchB).Obj()
			behavioral.MustCreate(ctx, k8sClient, cpuArchBFlavor)

			check1 = utiltestingapi.MakeAdmissionCheck("check1").ControllerName(kueue.MultiKueueControllerName).Obj()
			behavioral.MustCreate(ctx, k8sClient, check1)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = utiltestingapi.MakeAdmissionCheck("check2").ControllerName(kueue.MultiKueueControllerName).Obj()
			behavioral.MustCreate(ctx, k8sClient, check2)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Only one MultiKueue flavor dependent admission check assigned to cluster queue")
			gomega.Eventually(func(g gomega.Gomega) {
				updatedCq := &kueue.ClusterQueue{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), updatedCq)).Should(gomega.Succeed())
				updatedCq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: []kueue.AdmissionCheckStrategyRule{
						*utiltestingapi.MakeAdmissionCheckStrategyRule("check1", flavorCPUArchA).Obj(),
					},
				}
				g.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Only one MultiKueue flavor independent admission check assigned to cluster queue")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedAc kueue.AdmissionCheck
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(check2), &updatedAc)).Should(gomega.Succeed())
				g.Expect(k8sClient.Delete(ctx, &updatedAc)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).Should(gomega.Succeed())
				updatedCq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
					AdmissionChecks: []kueue.AdmissionCheckStrategyRule{
						{Name: "check1"},
					},
				}
				g.Expect(k8sClient.Update(ctx, &updatedCq)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

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
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Cohort hierarchy contains a cycle", func() {
		var (
			flavor       *kueue.ResourceFlavor
			cohortA      *kueue.Cohort
			cohortB      *kueue.Cohort
			cqWithCohort *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			flavor = utiltestingapi.MakeResourceFlavor("cycle-test-flavor").Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cohortA = utiltestingapi.MakeCohort("cycle-cohort-a").Obj()
			cohortB = utiltestingapi.MakeCohort("cycle-cohort-b").Parent("cycle-cohort-a").Obj()
			behavioral.MustCreate(ctx, k8sClient, cohortA)
			behavioral.MustCreate(ctx, k8sClient, cohortB)

			cqWithCohort = utiltestingapi.MakeClusterQueue("cq-cycle-test").
				Cohort("cycle-cohort-b").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("cycle-test-flavor").
						Resource(corev1.ResourceCPU, "10", "10").Obj(),
				).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, cqWithCohort)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqWithCohort, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohortB, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohortA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("Should mark ClusterQueue inactive when its Cohort hierarchy contains a cycle, and restore active when resolved", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqWithCohort), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating a cycle: setting cycle-cohort-a parent to cycle-cohort-b")
			gomega.Eventually(func(g gomega.Gomega) {
				var cohort kueue.Cohort
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohortA), &cohort)).To(gomega.Succeed())
				cohort.Spec.ParentName = "cycle-cohort-b"
				g.Expect(k8sClient.Update(ctx, &cohort)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying ClusterQueue becomes inactive with CohortCycleDetected reason")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqWithCohort), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.ClusterQueueActiveReasonCohortCycleDetected,
						Message: `Can't admit new workloads: Cohort "cycle-cohort-b" has a cycle in hierarchy.`,
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Resolving the cycle: removing parent from cycle-cohort-a")
			gomega.Eventually(func(g gomega.Gomega) {
				var cohort kueue.Cohort
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohortA), &cohort)).To(gomega.Succeed())
				cohort.Spec.ParentName = ""
				g.Expect(k8sClient.Update(ctx, &cohort)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying ClusterQueue becomes active again")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqWithCohort), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can admit new workloads",
					},
				}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("ReclaimablePods feature gate is off and clusterQueue usage status is reconciled", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			modelAFlavor *kueue.ResourceFlavor
			ac           *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ReclaimablePods, false)

			ac = utiltestingapi.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, ac)
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorModelA).
						Resource(resourceGPU, "5", "5").Obj(),
				).
				Cohort("cohort").
				AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, localQueue)

			modelAFlavor = utiltestingapi.MakeResourceFlavor(flavorModelA).NodeLabel(resourceGPU.String(), flavorModelA).Obj()
			behavioral.MustCreate(ctx, k8sClient, modelAFlavor)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, modelAFlavor, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		})

		ginkgo.It("Should ignore update status when workloads have reclaimable pods", framework.SlowSpec, func() {
			wl := utiltestingapi.MakeWorkload("one", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(
					*utiltestingapi.MakePodSet("workers", 5).
						Request(resourceGPU, "1").
						Obj(),
				).
				Obj()
			ginkgo.By("Creating the workload", func() {
				behavioral.MustCreate(ctx, k8sClient, wl)
				behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
				behavioral.ExpectLQPendingWorkloadsMetric(localQueue, 1, 0)
			})

			ginkgo.By("Admitting the workload", func() {
				admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(clusterQueue.Name)).PodSets(
					kueue.PodSetAssignment{
						Name: "workers",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							resourceGPU: "model-a",
						},
						ResourceUsage: corev1.ResourceList{
							resourceGPU: resource.MustParse("5"),
						},
						Count: new(int32(5)),
					},
				).Obj()
				behavioral.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), admission)
			})

			ginkgo.By("Validating CQ status has changed", func() {
				behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
					g.Expect(updatedCq.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{
						{
							Name: flavorModelA,
							Resources: []kueue.ResourceUsage{{
								Name:  resourceGPU,
								Total: resource.MustParse("5"),
							}},
						},
					}, behavioral.IgnoreConditionTimestamps))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Marking two workers as reclaimable", func() {
				behavioral.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{{Name: "workers", Count: 2}})
			})

			ginkgo.By("Validating CQ status hasn't changed", func() {
				behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				behavioral.ExpectLQReservingActiveWorkloadsMetric(localQueue, 1)
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
					g.Expect(updatedCq.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{
						{
							Name: flavorModelA,
							Resources: []kueue.ResourceUsage{{
								Name:  resourceGPU,
								Total: resource.MustParse("5"),
							}},
						},
					}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq     *kueue.ClusterQueue
			lq     *kueue.LocalQueue
			check  *kueue.AdmissionCheck
			flavor *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			check = utiltestingapi.MakeAdmissionCheck("check").ControllerName("check-controller").Obj()
			behavioral.MustCreate(ctx, k8sClient, check)

			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			behavioral.MustCreate(ctx, k8sClient, flavor)

			cq = utiltestingapi.MakeClusterQueue("foo-cq").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5").Obj(),
			).AdmissionChecks(kueue.AdmissionCheckReference(check.Name)).Obj()
			lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			behavioral.MustCreate(ctx, k8sClient, lq)
			behavioral.MustCreate(ctx, k8sClient, cq)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, check, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("Should delete clusterQueues successfully when no admitted workloads are running", func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should be stuck in termination until admitted workloads finished running", func() {
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			behavioral.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)
			behavioral.ExpectLQByStatusMetric(lq, metav1.ConditionTrue)

			ginkgo.By("Admit workload")
			wl := utiltestingapi.MakeWorkload("workload", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			key := client.ObjectKeyFromObject(wl)

			podSetAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(
				resourceGPU,
				kueue.ResourceFlavorReference(flavor.Name),
			).Obj()
			cqAdmission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).PodSets(podSetAssignment).Obj()
			behavioral.SetQuotaReservation(ctx, k8sClient, key, cqAdmission)

			ginkgo.By("Set admission check ready")
			behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, true)
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, key, updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, cq)).To(gomega.Succeed())
			behavioral.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusTerminating)
			// The ClusterQueue is now terminating (Active=False), so its LocalQueue is
			// no longer active either (it mirrors the ClusterQueue's Active condition).
			behavioral.ExpectLQByStatusMetric(lq, metav1.ConditionFalse)
			var newCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				g.Expect(newCQ.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Finish workload")
			behavioral.FinishWorkloads(ctx, k8sClient, wl)

			ginkgo.By("The clusterQueue will be deleted")
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, false)
		})

		ginkgo.It("Should keep the status counters and Active condition accurate while terminating", func() {
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			behavioral.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)

			ginkgo.By("Admitting a workload that consumes the whole quota")
			admittedWl := utiltestingapi.MakeWorkload("admitted", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(resourceGPU, "5").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, admittedWl)
			admittedKey := client.ObjectKeyFromObject(admittedWl)
			podSetAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(
				resourceGPU,
				kueue.ResourceFlavorReference(flavor.Name),
			).Obj()
			cqAdmission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).PodSets(podSetAssignment).Obj()
			behavioral.SetQuotaReservation(ctx, k8sClient, admittedKey, cqAdmission)
			behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, admittedWl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, true)

			ginkgo.By("Creating a second workload that stays pending (quota is exhausted)")
			pendingWl := utiltestingapi.MakeWorkload("pending", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(resourceGPU, "5").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, pendingWl)

			ginkgo.By("The ClusterQueue reports one admitted and one pending workload while active")
			createdCQ := &kueue.ClusterQueue{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), createdCQ)).To(gomega.Succeed())
				g.Expect(createdCQ.Status.AdmittedWorkloads).To(gomega.Equal(int32(1)))
				g.Expect(createdCQ.Status.ReservingWorkloads).To(gomega.Equal(int32(1)))
				g.Expect(createdCQ.Status.PendingWorkloads).To(gomega.Equal(int32(1)))
				g.Expect(createdCQ.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.ClusterQueueActive, "Ready"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Deleting the ClusterQueue - the resource-in-use finalizer keeps it while a workload reserves quota")
			gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, cq)).To(gomega.Succeed())
			behavioral.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusTerminating)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), createdCQ)).To(gomega.Succeed())
				g.Expect(createdCQ.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Deleting the pending workload while the ClusterQueue is terminating")
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pendingWl, true)

			ginkgo.By("The terminating ClusterQueue keeps its status accurate: Active=Terminating and pendingWorkloads back to 0")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), createdCQ)).To(gomega.Succeed())
				g.Expect(createdCQ.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(kueue.ClusterQueueActive, kueue.ClusterQueueActiveReasonTerminating))
				g.Expect(createdCQ.Status.PendingWorkloads).To(gomega.Equal(int32(0)))
				g.Expect(createdCQ.Status.ReservingWorkloads).To(gomega.Equal(int32(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing the admitted workload lets the ClusterQueue be deleted")
			behavioral.FinishWorkloads(ctx, k8sClient, admittedWl)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, false)
		})

		ginkgo.It("Should delete the cluster without waiting for reserving only workloads to finish", framework.SlowSpec, func() {
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			behavioral.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)
			behavioral.ExpectLQByStatusMetric(lq, metav1.ConditionTrue)

			ginkgo.By("Setting quota reservation")
			wl := utiltestingapi.MakeWorkload("workload", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			podSetAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(
				resourceGPU,
				kueue.ResourceFlavorReference(flavor.Name),
			).Obj()
			cqAdmission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).PodSets(podSetAssignment).Obj()
			behavioral.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), cqAdmission)

			ginkgo.By("Delete clusterQueue")
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should remove finalizer promptly when workload finishes", func() {
			behavioral.SetAdmissionCheckActive(ctx, k8sClient, check, metav1.ConditionTrue)
			behavioral.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)

			ginkgo.By("Creating and admitting workload")
			wl := utiltestingapi.MakeWorkload("workload", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			key := client.ObjectKeyFromObject(wl)

			podSetAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(
				resourceGPU,
				kueue.ResourceFlavorReference(flavor.Name),
			).Obj()
			cqAdmission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).PodSets(podSetAssignment).Obj()
			behavioral.SetQuotaReservation(ctx, k8sClient, key, cqAdmission)

			behavioral.SetWorkloadsAdmissionCheck(ctx, k8sClient, wl, kueue.AdmissionCheckReference(check.Name), kueue.CheckStateReady, true)
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, key, updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing workload")
			behavioral.FinishWorkloads(ctx, k8sClient, wl)

			ginkgo.By("Deleting clusterQueue - should succeed without waiting")
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})
	})

	ginkgo.When("ClusterQueue is concurrently modified", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			cq = utiltestingapi.MakeClusterQueue("foo-cq").Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should log concurrent modification errors with log level smaller than error", func() {
			_ = fwk.ObservedLogs.TakeAll() // clear logs

			const nGoroutines = 25

			// Using a WaitGroup ensures we don't leak goroutines into the next It() block.
			var wg sync.WaitGroup
			defer wg.Wait() // Wait for goroutines stopped.

			ctx, cancel := context.WithTimeout(ginkgo.GinkgoTB().Context(), behavioral.MediumTimeout)
			defer cancel() // Stop goroutines.

			setClusterStatusPending := func(id int) {
				defer ginkgo.GinkgoRecover()

				for i := 0; ; i++ {
					var updatedCq kueue.ClusterQueue
					err := k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)
					if errors.Is(err, context.Canceled) {
						return // Test is over, exit quietly
					}
					gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
					// Use a distinct message for every write so that each update is a real change.
					// Identical writes are dropped by the API server as no-ops, and then the
					// reconciler's corrective status updates would never race against them.
					apimeta.SetStatusCondition(&updatedCq.Status.Conditions, metav1.Condition{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "ByTest",
						Message: fmt.Sprintf("by test goroutine %d iteration %d", id, i),
					})
					err = k8sClient.Status().Update(ctx, &updatedCq)
					if errors.Is(err, context.Canceled) {
						return // Test is over, exit quietly
					}
					gomega.Expect(behavioral.IgnoreConflict(err)).To(gomega.Succeed())

					select {
					case <-ctx.Done():
						return
					case <-time.After(behavioral.Interval):
						// Just continue to the next loop iteration.
					}
				}
			}

			for i := range nGoroutines {
				wg.Go(func() { setClusterStatusPending(i) })
			}

			gomega.Eventually(func(g gomega.Gomega) {
				reconcileLogs := fwk.ObservedLogs
				reconcileConcurrentModificationLogs := reconcileLogs.Filter(behavioral.IsLoggedEntryAConcurrentModification)
				g.Expect(reconcileConcurrentModificationLogs.All()).ShouldNot(gomega.BeEmpty(),
					"There should be some concurrent modifcation error log entries")
				g.Expect(reconcileConcurrentModificationLogs.Filter(func(le observer.LoggedEntry) bool {
					return le.Level >= zapcore.ErrorLevel
				}).All()).Should(gomega.BeEmpty(),
					"Log level should be smaller than error")
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("ClusterQueue controller with RoleTracker", ginkgo.Label("controller:clusterqueue", "area:core"), func() {
	var (
		electedChan   chan struct{}
		trackerCancel context.CancelFunc
		rf            *kueue.ResourceFlavor
		cq            *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		electedChan = make(chan struct{})
		tracker := roletracker.NewRoleTracker(electedChan)
		var trackerCtx context.Context
		trackerCtx, trackerCancel = context.WithCancel(ctx)
		go tracker.Start(trackerCtx, ctrl.Log)
		fwk.StartManager(ctx, cfg, managerAndControllerSetup(nil, withRoleTracker(tracker)))

		rf = utiltestingapi.MakeResourceFlavor("ha-transition-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("ha-transition-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("ha-transition-flavor").Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		select {
		case <-electedChan:
		default:
			close(electedChan)
		}
		trackerCancel()
		fwk.StopManager(ctx)
	})

	ginkgo.It("Should resync metrics after role transition from follower to leader", func() {
		followerLabels := map[string]string{
			"cluster_queue": cq.Name,
			"replica_role":  roletracker.RoleFollower,
		}

		ginkgo.By("Verifying metrics exist with replica_role=follower")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, followerLabels)).NotTo(gomega.BeEmpty())
			g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueByStatus, followerLabels)).NotTo(gomega.BeEmpty())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Triggering role transition by closing electedChan")
		close(electedChan)

		ginkgo.By("Verifying metrics with replica_role=leader have correct values")
		gomega.Eventually(func(g gomega.Gomega) {
			nominalQuota, err := testbehavioral.GetGaugeMetricValue(metrics.ClusterQueueResourceNominalQuota.WithLabelValues(
				"", cq.Name, "ha-transition-flavor", string(corev1.ResourceCPU), roletracker.RoleLeader,
			))
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(nominalQuota).To(gomega.Equal(float64(10)))

			activeStatus, err := testbehavioral.GetGaugeMetricValue(metrics.ClusterQueueByStatus.WithLabelValues(
				cq.Name, string(metrics.CQStatusActive), roletracker.RoleLeader,
			))
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(activeStatus).To(gomega.Equal(float64(1)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying metrics with replica_role=follower are gone")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, followerLabels)).To(gomega.BeEmpty())
			g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueByStatus, followerLabels)).To(gomega.BeEmpty())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
})
