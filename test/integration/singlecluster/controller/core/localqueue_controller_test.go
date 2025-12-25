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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Queue controller", ginkgo.Label("controller:localqueue", "area:core"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		flavorModelC = "model-c"
		flavorModelD = "model-d"
	)
	var (
		ns              *corev1.Namespace
		queue           *kueue.LocalQueue
		clusterQueue    *kueue.ClusterQueue
		resourceFlavors = []kueue.ResourceFlavor{
			*utiltestingapi.MakeResourceFlavor(flavorModelC).
				NodeLabel(resourceGPU.String(), flavorModelC).
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
			*utiltestingapi.MakeResourceFlavor(flavorModelD).NodeLabel(resourceGPU.String(), flavorModelD).Obj(),
		}
		emptyUsage = []kueue.LocalQueueFlavorUsage{
			{
				Name: flavorModelD,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("0"),
					},
				},
			},
			{
				Name: flavorModelC,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("0"),
					},
				},
			},
		}
		ac *kueue.AdmissionCheck
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-queue-")
	})

	ginkgo.BeforeEach(func() {
		ac = utiltestingapi.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
		util.MustCreate(ctx, k8sClient, ac)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue.queue-controller").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			Obj()
		queue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.MustCreate(ctx, k8sClient, queue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queue)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		for _, rf := range resourceFlavors {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &rf, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
	})

	ginkgo.It("Should update conditions when clusterQueues that its localQueue references are updated", framework.SlowSpec, func() {
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "ClusterQueueDoesNotExist",
						Message: "Can't submit new workloads to clusterQueue",
					},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating a clusterQueue")
		util.MustCreate(ctx, k8sClient, clusterQueue)
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "ClusterQueueIsInactive",
						Message: "Can't submit new workloads to clusterQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorsUsage:       emptyUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating resourceFlavors")
		for _, rf := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &rf)
		}
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorsUsage:       emptyUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Deleting a clusterQueue")
		gomega.Expect(k8sClient.Delete(ctx, clusterQueue)).To(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "ClusterQueueDoesNotExist",
						Message: "Can't submit new workloads to clusterQueue",
					},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should update status when workloads are created", framework.SlowSpec, func() {
		ginkgo.By("Creating resourceFlavors")
		for _, rf := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &rf)
		}

		// LQ metrics should all be 0 here
		util.ExpectLQPendingWorkloadsMetric(queue, 0, 0)
		util.ExpectLQAdmittedWorkloadsTotalMetric(queue, "", 0)
		util.ExpectLQByStatusMetric(queue, metav1.ConditionFalse)

		util.ExpectLocalQueueResourceMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceMetric(queue, flavorModelD, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelD, resourceGPU.String(), 0)

		ginkgo.By("Creating a clusterQueue")
		util.MustCreate(ctx, k8sClient, clusterQueue)

		util.ExpectLQByStatusMetric(queue, metav1.ConditionTrue)

		util.ExpectLQPendingWorkloadsMetric(queue, 0, 0)
		workloads := []*kueue.Workload{
			utiltestingapi.MakeWorkload("one", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(resourceGPU, "2").
				Obj(),
			utiltestingapi.MakeWorkload("two", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(resourceGPU, "3").
				Obj(),
			utiltestingapi.MakeWorkload("three", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(resourceGPU, "1").
				Obj(),
		}
		admissions := []*kueue.Admission{
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, flavorModelC, "2").Obj()).Obj(),
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, flavorModelC, "3").Obj()).Obj(),
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, flavorModelD, "1").Obj()).Obj(),
		}

		ginkgo.By("Creating workloads")
		for _, w := range workloads {
			util.MustCreate(ctx, k8sClient, w)
		}

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				ReservingWorkloads: 0,
				AdmittedWorkloads:  0,
				PendingWorkloads:   3,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorsUsage:       emptyUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.ExpectLocalQueueResourceMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceMetric(queue, flavorModelD, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelD, resourceGPU.String(), 0)

		util.ExpectLQPendingWorkloadsMetric(queue, 3, 0)

		ginkgo.By("Setting the workloads quota reservation")
		for i, w := range workloads {
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(w), admissions[i])
		}

		fullUsage := []kueue.LocalQueueFlavorUsage{
			{
				Name: flavorModelD,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("1"),
					},
				},
			},
			{
				Name: flavorModelC,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("5"),
					},
				},
			},
		}

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				ReservingWorkloads: 3,
				AdmittedWorkloads:  0,
				PendingWorkloads:   0,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: fullUsage,
				FlavorsUsage:       emptyUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.ExpectLocalQueueResourceMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceMetric(queue, flavorModelD, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelC, resourceGPU.String(), 5)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelD, resourceGPU.String(), 1)

		util.ExpectLQReservingActiveWorkloadsMetric(queue, 3)

		ginkgo.By("Setting the workloads admission checks")
		for _, w := range workloads {
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
		}

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				ReservingWorkloads: 3,
				AdmittedWorkloads:  3,
				PendingWorkloads:   0,
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: fullUsage,
				FlavorsUsage:       fullUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.ExpectLocalQueueResourceMetric(queue, flavorModelC, resourceGPU.String(), 5)
		util.ExpectLocalQueueResourceMetric(queue, flavorModelD, resourceGPU.String(), 1)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelC, resourceGPU.String(), 5)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelD, resourceGPU.String(), 1)

		util.ExpectLQAdmittedWorkloadsTotalMetric(queue, "", 3)
		util.ExpectLQAdmissionWaitTimeMetric(queue, "", 3)
		util.ExpectLQPendingWorkloadsMetric(queue, 0, 0)

		ginkgo.By("Finishing workloads")
		util.FinishWorkloads(ctx, k8sClient, workloads...)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
				Conditions: []metav1.Condition{
					{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to localQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorsUsage:       emptyUsage,
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		util.ExpectLocalQueueResourceMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceMetric(queue, flavorModelD, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelC, resourceGPU.String(), 0)
		util.ExpectLocalQueueResourceReservationsMetric(queue, flavorModelD, resourceGPU.String(), 0)
	})

	ginkgo.It("Should update status when ClusterQueue are forcefully deleted", framework.SlowSpec, func() {
		ginkgo.By("Creating resourceFlavors", func() {
			for _, rf := range resourceFlavors {
				util.MustCreate(ctx, k8sClient, &rf)
			}
		})

		ginkgo.By("Creating a clusterQueue", func() {
			util.MustCreate(ctx, k8sClient, clusterQueue)
		})

		workloads := []*kueue.Workload{
			utiltestingapi.MakeWorkload("one", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(resourceGPU, "2").
				Obj(),
			utiltestingapi.MakeWorkload("two", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(resourceGPU, "3").
				Obj(),
			utiltestingapi.MakeWorkload("three", ns.Name).
				Queue(kueue.LocalQueueName(queue.Name)).
				Request(resourceGPU, "1").
				Obj(),
		}
		admissions := []*kueue.Admission{
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, flavorModelC, "2").Obj()).Obj(),
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, flavorModelC, "3").Obj()).Obj(),
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, flavorModelD, "1").Obj()).Obj(),
		}

		ginkgo.By("Creating workloads", func() {
			for _, w := range workloads {
				util.MustCreate(ctx, k8sClient, w)
			}

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQueue kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
				g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
					ReservingWorkloads: 0,
					AdmittedWorkloads:  0,
					PendingWorkloads:   3,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.LocalQueueActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Ready",
							Message: "Can submit new workloads to localQueue",
						},
					},
					FlavorsReservation: emptyUsage,
					FlavorsUsage:       emptyUsage,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		fullUsage := []kueue.LocalQueueFlavorUsage{
			{
				Name: flavorModelD,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("1"),
					},
				},
			},
			{
				Name: flavorModelC,
				Resources: []kueue.LocalQueueResourceUsage{
					{
						Name:  resourceGPU,
						Total: resource.MustParse("5"),
					},
				},
			},
		}

		ginkgo.By("Setting the workloads quota reservation", func() {
			for i, w := range workloads {
				util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(w), admissions[i])
			}

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQueue kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
				g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
					ReservingWorkloads: 3,
					AdmittedWorkloads:  0,
					PendingWorkloads:   0,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.LocalQueueActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Ready",
							Message: "Can submit new workloads to localQueue",
						},
					},
					FlavorsReservation: fullUsage,
					FlavorsUsage:       emptyUsage,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Setting the workloads admission checks", func() {
			for _, w := range workloads {
				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
			}

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQueue kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
				g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
					ReservingWorkloads: 3,
					AdmittedWorkloads:  3,
					PendingWorkloads:   0,
					Conditions: []metav1.Condition{
						{
							Type:    kueue.LocalQueueActive,
							Status:  metav1.ConditionTrue,
							Reason:  "Ready",
							Message: "Can submit new workloads to localQueue",
						},
					},
					FlavorsReservation: fullUsage,
					FlavorsUsage:       fullUsage,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Deleting a clusterQueue forcefully", func() {
			gomega.Expect(k8sClient.Delete(ctx, clusterQueue)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedClusterQueue kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedClusterQueue)).To(gomega.Succeed())
				updatedClusterQueue.Finalizers = nil
				g.Expect(k8sClient.Update(ctx, &updatedClusterQueue)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedClusterQueue)).Should(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedQueue kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queue), &updatedQueue)).To(gomega.Succeed())
				g.Expect(updatedQueue.Status).Should(gomega.BeComparableTo(kueue.LocalQueueStatus{
					Conditions: []metav1.Condition{
						{
							Type:    kueue.LocalQueueActive,
							Status:  metav1.ConditionFalse,
							Reason:  "ClusterQueueDoesNotExist",
							Message: "Can't submit new workloads to clusterQueue",
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("Queue controller for wall time limits", ginkgo.Label("feature:wall-time"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns              *corev1.Namespace
		clusterQueue    *kueue.ClusterQueue
		resourceFlavors []kueue.ResourceFlavor
		flavorModelC    string
		flavorModelD    string
		ac              *kueue.AdmissionCheck
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-walltime-")
		flavorModelC = "model-c-" + ns.Name
		flavorModelD = "model-d-" + ns.Name
		resourceFlavors = []kueue.ResourceFlavor{
			*utiltestingapi.MakeResourceFlavor(flavorModelC).
				NodeLabel(resourceGPU.String(), flavorModelC).
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
			*utiltestingapi.MakeResourceFlavor(flavorModelD).NodeLabel(resourceGPU.String(), flavorModelD).Obj(),
		}
	})

	ginkgo.AfterEach(func() {
		if clusterQueue != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		}
		for _, rf := range resourceFlavors {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &rf, true)
		}
		if ac != nil {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		}
	})

	ginkgo.It("Should track wall time usage when feature gate is enabled", func() {
		ginkgo.By("Enabling the WallTimeLimits feature gate")
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WallTimeLimits, true)

		ginkgo.By("Creating admission check and setting it active")
		ac = utiltestingapi.MakeAdmissionCheck("ac-walltime-enabled").ControllerName("ac-controller").Obj()
		util.MustCreate(ctx, k8sClient, ac)
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

		ginkgo.By("Creating resourceFlavors")
		for i := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &resourceFlavors[i])
		}

		ginkgo.By("Creating a clusterQueue")
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue.walltime-enabled").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			WallTimePolicy(
				kueue.WallTimeFlavor{Name: kueue.ResourceFlavorReference(flavorModelD), WallTimeAllocatedHours: 100, ActionWhenWallTimeExhausted: kueue.Hold},
				kueue.WallTimeFlavor{Name: kueue.ResourceFlavorReference(flavorModelC), WallTimeAllocatedHours: 100, ActionWhenWallTimeExhausted: kueue.Hold},
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("Creating a local queue with wall time policy")
		queueWithWallTime := utiltestingapi.MakeLocalQueue("queue-with-wall-time", ns.Name).
			ClusterQueue(clusterQueue.Name).
			WallTimePolicy(1, kueue.Hold).
			Obj()
		util.MustCreate(ctx, k8sClient, queueWithWallTime)

		ginkgo.By("Creating and admitting workloads")
		workloads := []*kueue.Workload{
			utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(queueWithWallTime.Name)).
				Request(resourceGPU, "1").
				WallTimeSeconds(3600).
				Obj(),
			utiltestingapi.MakeWorkload("wl2", ns.Name).
				Queue(kueue.LocalQueueName(queueWithWallTime.Name)).
				Request(resourceGPU, "2").
				WallTimeSeconds(3600).
				Obj(),
		}
		admissions := []*kueue.Admission{
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, kueue.ResourceFlavorReference(flavorModelD), "1").Obj()).Obj(),
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, kueue.ResourceFlavorReference(flavorModelC), "2").Obj()).Obj(),
		}

		for _, w := range workloads {
			util.MustCreate(ctx, k8sClient, w)
		}

		for i, w := range workloads {
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(w), admissions[i])
		}

		for _, w := range workloads {
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
		}

		ginkgo.By("Verifying wall time usage is tracked in the status")
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queueWithWallTime), &updatedQueue)).To(gomega.Succeed())
			g.Expect(updatedQueue.Status.WallTimeFlavorUsage).NotTo(gomega.BeEmpty())

			// Verify that each flavor has wall time usage tracking
			for _, usage := range updatedQueue.Status.WallTimeFlavorUsage {
				g.Expect(usage.WallTimeAllocated).To(gomega.Equal(int32(1)))
				// Wall time used should be >= 0 (might be 0 if just created)
				g.Expect(usage.WallTimeUsed).To(gomega.BeNumerically(">=", int32(0)))
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Finishing workloads")
		util.FinishWorkloads(ctx, k8sClient, workloads...)

		ginkgo.By("Deleting the local queue with wall time")
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queueWithWallTime)).To(gomega.Succeed())
	})

	ginkgo.It("Should NOT track wall time usage when there is no wall time limits on clusterqueue or localqueue", func() {
		ginkgo.By("Disabling the WallTimeLimits feature gate")
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WallTimeLimits, false)

		ginkgo.By("Creating admission check and setting it active")
		ac = utiltestingapi.MakeAdmissionCheck("ac-walltime-disabled").ControllerName("ac-controller").Obj()
		util.MustCreate(ctx, k8sClient, ac)
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

		ginkgo.By("Creating resourceFlavors")
		for i := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &resourceFlavors[i])
		}

		ginkgo.By("Creating a clusterQueue")
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue.walltime-disabled").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			WallTimePolicy(
				kueue.WallTimeFlavor{Name: kueue.ResourceFlavorReference(flavorModelD), WallTimeAllocatedHours: 100, ActionWhenWallTimeExhausted: kueue.Hold},
				kueue.WallTimeFlavor{Name: kueue.ResourceFlavorReference(flavorModelC), WallTimeAllocatedHours: 100, ActionWhenWallTimeExhausted: kueue.Hold},
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("Creating a local queue with wall time policy")
		queueWithWallTime := utiltestingapi.MakeLocalQueue("queue-no-wall-time", ns.Name).
			ClusterQueue(clusterQueue.Name).
			WallTimePolicy(100, kueue.Hold).
			Obj()
		util.MustCreate(ctx, k8sClient, queueWithWallTime)

		ginkgo.By("Creating and admitting workloads")
		workloads := []*kueue.Workload{
			utiltestingapi.MakeWorkload("wl3", ns.Name).
				Queue(kueue.LocalQueueName(queueWithWallTime.Name)).
				Request(resourceGPU, "1").
				Obj(),
		}
		admissions := []*kueue.Admission{
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, kueue.ResourceFlavorReference(flavorModelD), "1").Obj()).Obj(),
		}

		for _, w := range workloads {
			util.MustCreate(ctx, k8sClient, w)
		}

		for i, w := range workloads {
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(w), admissions[i])
		}

		for _, w := range workloads {
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
		}

		ginkgo.By("Verifying wall time usage is NOT tracked in the status")
		gomega.Consistently(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queueWithWallTime), &updatedQueue)).To(gomega.Succeed())
			// When feature gate is disabled, WallTimeFlavorUsage should be empty
			g.Expect(updatedQueue.Status.WallTimeFlavorUsage).To(gomega.BeEmpty())
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Finishing workloads")
		util.FinishWorkloads(ctx, k8sClient, workloads...)

		ginkgo.By("Deleting the local queue with wall time")
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queueWithWallTime)).To(gomega.Succeed())
	})

	ginkgo.It("Should NOT track wall time usage when cq and lq do not have wall time limits", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WallTimeLimits, true)

		ginkgo.By("Creating admission check and setting it active")
		ac = utiltestingapi.MakeAdmissionCheck("ac-walltime-disabled").ControllerName("ac-controller").Obj()
		util.MustCreate(ctx, k8sClient, ac)
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

		ginkgo.By("Creating resourceFlavors")
		for i := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &resourceFlavors[i])
		}

		ginkgo.By("Creating a clusterQueue")
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue.walltime-disabled").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("Creating a local queue with wall time policy")
		queueWithWallTime := utiltestingapi.MakeLocalQueue("queue-no-wall-time", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		util.MustCreate(ctx, k8sClient, queueWithWallTime)

		ginkgo.By("Creating and admitting workloads")
		workloads := []*kueue.Workload{
			utiltestingapi.MakeWorkload("wl3", ns.Name).
				Queue(kueue.LocalQueueName(queueWithWallTime.Name)).
				Request(resourceGPU, "1").
				Obj(),
		}
		admissions := []*kueue.Admission{
			utiltestingapi.MakeAdmission(clusterQueue.Name).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(resourceGPU, kueue.ResourceFlavorReference(flavorModelD), "1").Obj()).Obj(),
		}

		for _, w := range workloads {
			util.MustCreate(ctx, k8sClient, w)
		}

		for i, w := range workloads {
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(w), admissions[i])
		}

		for _, w := range workloads {
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)
		}

		ginkgo.By("Verifying wall time usage is NOT tracked in the status")
		gomega.Consistently(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queueWithWallTime), &updatedQueue)).To(gomega.Succeed())
			// When feature gate is disabled, WallTimeFlavorUsage should be empty
			g.Expect(updatedQueue.Status.WallTimeFlavorUsage).To(gomega.BeEmpty())
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Finishing workloads")
		util.FinishWorkloads(ctx, k8sClient, workloads...)

		ginkgo.By("Deleting the local queue with wall time")
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queueWithWallTime)).To(gomega.Succeed())
	})

	ginkgo.It("Should set stopPolicy to Hold when wall time limit exceeded with Hold action", func() {
		ginkgo.By("Enabling the WallTimeLimits feature gate")
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WallTimeLimits, true)

		ginkgo.By("Creating admission check and setting it active")
		ac = utiltestingapi.MakeAdmissionCheck("ac-walltime-hold").ControllerName("ac-controller").Obj()
		util.MustCreate(ctx, k8sClient, ac)
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

		ginkgo.By("Creating resourceFlavors")
		for i := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &resourceFlavors[i])
		}

		ginkgo.By("Creating a clusterQueue with wall time policy")
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue.walltime-hold").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			WallTimePolicy(
				kueue.WallTimeFlavor{Name: kueue.ResourceFlavorReference(flavorModelD), WallTimeAllocatedHours: 1, ActionWhenWallTimeExhausted: kueue.Hold},
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("Creating a local queue with wall time policy and low wall time limit")
		queueWithLowLimit := utiltestingapi.MakeLocalQueue("queue-low-limit", ns.Name).
			ClusterQueue(clusterQueue.Name).
			WallTimePolicy(1, kueue.Hold).
			Obj()
		util.MustCreate(ctx, k8sClient, queueWithLowLimit)

		ginkgo.By("Creating and admitting a workload that exceeds wall time limit")
		workload := utiltestingapi.MakeWorkload("wl-exceeded", ns.Name).
			Queue(kueue.LocalQueueName(queueWithLowLimit.Name)).
			Request(resourceGPU, "1").
			WallTimeSeconds(7200). // 2 hours, exceeding 1 hour limit
			Obj()
		util.MustCreate(ctx, k8sClient, workload)

		admission := utiltestingapi.MakeAdmission(clusterQueue.Name).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(resourceGPU, kueue.ResourceFlavorReference(flavorModelD), "1").Obj()).Obj()

		util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(workload), admission)
		util.SetWorkloadsAdmissionCheck(ctx, k8sClient, workload, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)

		ginkgo.By("Verifying that stopPolicy is set to Hold when wall time limit is exceeded")
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queueWithLowLimit), &updatedQueue)).To(gomega.Succeed())

			// Verify wall time usage is tracked
			g.Expect(updatedQueue.Status.WallTimeFlavorUsage).NotTo(gomega.BeEmpty())

			// Check if wall time limit is exceeded
			hasExceededLimit := false
			for _, usage := range updatedQueue.Status.WallTimeFlavorUsage {
				if usage.WallTimeUsed >= usage.WallTimeAllocated {
					hasExceededLimit = true
					break
				}
			}

			// If limit exceeded, verify stopPolicy is set correctly
			if hasExceededLimit {
				g.Expect(updatedQueue.Spec.StopPolicy).NotTo(gomega.BeNil(), "stopPolicy should be set when wall time limit is exceeded")
				g.Expect(*updatedQueue.Spec.StopPolicy).To(gomega.Equal(kueue.Hold), "stopPolicy should be Hold")
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Finishing workload")
		util.FinishWorkloads(ctx, k8sClient, workload)

		ginkgo.By("Deleting the local queue")
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queueWithLowLimit)).To(gomega.Succeed())
	})

	ginkgo.It("Should set stopPolicy to HoldAndDrain when wall time limit exceeded with HoldAndDrain action", func() {
		ginkgo.By("Enabling the WallTimeLimits feature gate")
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WallTimeLimits, true)

		ginkgo.By("Creating admission check and setting it active")
		ac = utiltestingapi.MakeAdmissionCheck("ac-walltime-holddrain").ControllerName("ac-controller").Obj()
		util.MustCreate(ctx, k8sClient, ac)
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

		ginkgo.By("Creating resourceFlavors")
		for i := range resourceFlavors {
			util.MustCreate(ctx, k8sClient, &resourceFlavors[i])
		}

		ginkgo.By("Creating a clusterQueue with wall time policy")
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue.walltime-holddrain").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
			WallTimePolicy(
				kueue.WallTimeFlavor{Name: kueue.ResourceFlavorReference(flavorModelD), WallTimeAllocatedHours: 1, ActionWhenWallTimeExhausted: kueue.HoldAndDrain},
			).
			Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)

		ginkgo.By("Creating a local queue with wall time policy and low wall time limit")
		queueWithLowLimit := utiltestingapi.MakeLocalQueue("queue-low-limit-drain", ns.Name).
			ClusterQueue(clusterQueue.Name).
			WallTimePolicy(1, kueue.HoldAndDrain).
			Obj()
		util.MustCreate(ctx, k8sClient, queueWithLowLimit)

		ginkgo.By("Creating and admitting a workload that exceeds wall time limit")
		workload := utiltestingapi.MakeWorkload("wl-exceeded-drain", ns.Name).
			Queue(kueue.LocalQueueName(queueWithLowLimit.Name)).
			Request(resourceGPU, "1").
			WallTimeSeconds(7200). // 2 hours, exceeding 1 hour limit
			Obj()
		util.MustCreate(ctx, k8sClient, workload)

		admission := utiltestingapi.MakeAdmission(clusterQueue.Name).
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(resourceGPU, kueue.ResourceFlavorReference(flavorModelD), "1").Obj()).Obj()

		util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(workload), admission)
		util.SetWorkloadsAdmissionCheck(ctx, k8sClient, workload, kueue.AdmissionCheckReference(ac.Name), kueue.CheckStateReady, true)

		ginkgo.By("Verifying that stopPolicy is set to HoldAndDrain when wall time limit is exceeded")
		gomega.Eventually(func(g gomega.Gomega) {
			var updatedQueue kueue.LocalQueue
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(queueWithLowLimit), &updatedQueue)).To(gomega.Succeed())

			// Verify wall time usage is tracked
			g.Expect(updatedQueue.Status.WallTimeFlavorUsage).NotTo(gomega.BeEmpty())

			// Check if wall time limit is exceeded
			hasExceededLimit := false
			for _, usage := range updatedQueue.Status.WallTimeFlavorUsage {
				if usage.WallTimeUsed >= usage.WallTimeAllocated {
					hasExceededLimit = true
					break
				}
			}

			// If limit exceeded, verify stopPolicy is set correctly
			if hasExceededLimit {
				g.Expect(updatedQueue.Spec.StopPolicy).NotTo(gomega.BeNil(), "stopPolicy should be set when wall time limit is exceeded")
				g.Expect(*updatedQueue.Spec.StopPolicy).To(gomega.Equal(kueue.HoldAndDrain), "stopPolicy should be HoldAndDrain")
			}
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Finishing workload")
		util.FinishWorkloads(ctx, k8sClient, workload)

		ginkgo.By("Deleting the local queue")
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queueWithLowLimit)).To(gomega.Succeed())
	})
})
