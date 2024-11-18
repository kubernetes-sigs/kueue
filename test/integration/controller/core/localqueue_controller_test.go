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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Queue controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const (
		flavorModelC = "model-c"
		flavorModelD = "model-d"
	)
	var (
		ns              *corev1.Namespace
		queue           *kueue.LocalQueue
		clusterQueue    *kueue.ClusterQueue
		resourceFlavors = []kueue.ResourceFlavor{
			*testing.MakeResourceFlavor(flavorModelC).
				NodeLabel(resourceGPU.String(), flavorModelC).
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
			*testing.MakeResourceFlavor(flavorModelD).NodeLabel(resourceGPU.String(), flavorModelD).Obj(),
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
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-queue-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.BeforeEach(func() {
		ac = testing.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
		gomega.Expect(k8sClient.Create(ctx, ac)).To(gomega.Succeed())
		util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

		clusterQueue = testing.MakeClusterQueue("cluster-queue.queue-controller").
			ResourceGroup(
				*testing.MakeFlavorQuotas(flavorModelD).Resource(resourceGPU, "5", "5").Obj(),
				*testing.MakeFlavorQuotas(flavorModelC).Resource(resourceGPU, "5", "5").Obj(),
			).
			Cohort("cohort").
			AdmissionChecks(ac.Name).
			Obj()
		queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteObject(ctx, k8sClient, queue)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		for _, rf := range resourceFlavors {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &rf, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
	})

	ginkgo.It("Should update conditions when clusterQueues that its localQueue references are updated", func() {
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
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
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
				FlavorUsage:        emptyUsage,
				Flavors: []kueue.LocalQueueFlavorStatus{
					{Name: flavorModelD, Resources: []corev1.ResourceName{"example.com/gpu"}},
					{Name: flavorModelC, Resources: []corev1.ResourceName{"example.com/gpu"}},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating resourceFlavors")
		for _, rf := range resourceFlavors {
			gomega.Expect(k8sClient.Create(ctx, &rf)).To(gomega.Succeed())
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
						Message: "Can submit new workloads to clusterQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorUsage:        emptyUsage,
				Flavors: []kueue.LocalQueueFlavorStatus{
					{
						Name:       flavorModelD,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-d"},
					},
					{
						Name:       flavorModelC,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-c"},
						NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
					},
				},
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
			gomega.Expect(k8sClient.Create(ctx, &rf)).To(gomega.Succeed())
		}
		ginkgo.By("Creating a clusterQueue")
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

		workloads := []*kueue.Workload{
			testing.MakeWorkload("one", ns.Name).
				Queue(queue.Name).
				Request(resourceGPU, "2").
				Obj(),
			testing.MakeWorkload("two", ns.Name).
				Queue(queue.Name).
				Request(resourceGPU, "3").
				Obj(),
			testing.MakeWorkload("three", ns.Name).
				Queue(queue.Name).
				Request(resourceGPU, "1").
				Obj(),
		}
		admissions := []*kueue.Admission{
			testing.MakeAdmission(clusterQueue.Name).
				Assignment(resourceGPU, flavorModelC, "2").Obj(),
			testing.MakeAdmission(clusterQueue.Name).
				Assignment(resourceGPU, flavorModelC, "3").Obj(),
			testing.MakeAdmission(clusterQueue.Name).
				Assignment(resourceGPU, flavorModelD, "1").Obj(),
		}

		ginkgo.By("Creating workloads")
		for _, w := range workloads {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
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
						Message: "Can submit new workloads to clusterQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorUsage:        emptyUsage,
				Flavors: []kueue.LocalQueueFlavorStatus{
					{
						Name:       flavorModelD,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-d"},
					},
					{
						Name:       flavorModelC,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-c"},
						NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
					},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Setting the workloads quota reservation")
		for i, w := range workloads {
			gomega.Eventually(func(g gomega.Gomega) {
				var newWL kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(ctx, k8sClient, &newWL, admissions[i])).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
						Message: "Can submit new workloads to clusterQueue",
					},
				},
				FlavorsReservation: fullUsage,
				FlavorUsage:        emptyUsage,
				Flavors: []kueue.LocalQueueFlavorStatus{
					{
						Name:       flavorModelD,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-d"},
					},
					{
						Name:       flavorModelC,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-c"},
						NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
					},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Setting the workloads admission checks")
		for _, w := range workloads {
			util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, ac.Name, kueue.CheckStateReady, true)
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
						Message: "Can submit new workloads to clusterQueue",
					},
				},
				FlavorsReservation: fullUsage,
				FlavorUsage:        fullUsage,
				Flavors: []kueue.LocalQueueFlavorStatus{
					{
						Name:       flavorModelD,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-d"},
					},
					{
						Name:       flavorModelC,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-c"},
						NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
					},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
						Message: "Can submit new workloads to clusterQueue",
					},
				},
				FlavorsReservation: emptyUsage,
				FlavorUsage:        emptyUsage,
				Flavors: []kueue.LocalQueueFlavorStatus{
					{
						Name:       flavorModelD,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-d"},
					},
					{
						Name:       flavorModelC,
						Resources:  []corev1.ResourceName{"example.com/gpu"},
						NodeLabels: map[string]string{"example.com/gpu": "model-c"},
						NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
					},
				},
			}, util.IgnoreConditionTimestampsAndObservedGeneration))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should update status when ClusterQueue are forcefully deleted", framework.SlowSpec, func() {
		ginkgo.By("Creating resourceFlavors", func() {
			for _, rf := range resourceFlavors {
				gomega.Expect(k8sClient.Create(ctx, &rf)).To(gomega.Succeed())
			}
		})

		ginkgo.By("Creating a clusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
		})

		workloads := []*kueue.Workload{
			testing.MakeWorkload("one", ns.Name).
				Queue(queue.Name).
				Request(resourceGPU, "2").
				Obj(),
			testing.MakeWorkload("two", ns.Name).
				Queue(queue.Name).
				Request(resourceGPU, "3").
				Obj(),
			testing.MakeWorkload("three", ns.Name).
				Queue(queue.Name).
				Request(resourceGPU, "1").
				Obj(),
		}
		admissions := []*kueue.Admission{
			testing.MakeAdmission(clusterQueue.Name).
				Assignment(resourceGPU, flavorModelC, "2").Obj(),
			testing.MakeAdmission(clusterQueue.Name).
				Assignment(resourceGPU, flavorModelC, "3").Obj(),
			testing.MakeAdmission(clusterQueue.Name).
				Assignment(resourceGPU, flavorModelD, "1").Obj(),
		}

		ginkgo.By("Creating workloads", func() {
			for _, w := range workloads {
				gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
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
							Message: "Can submit new workloads to clusterQueue",
						},
					},
					FlavorsReservation: emptyUsage,
					FlavorUsage:        emptyUsage,
					Flavors: []kueue.LocalQueueFlavorStatus{
						{
							Name:       flavorModelD,
							Resources:  []corev1.ResourceName{"example.com/gpu"},
							NodeLabels: map[string]string{"example.com/gpu": "model-d"},
						},
						{
							Name:       flavorModelC,
							Resources:  []corev1.ResourceName{"example.com/gpu"},
							NodeLabels: map[string]string{"example.com/gpu": "model-c"},
							NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
						},
					},
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
				gomega.Eventually(func(g gomega.Gomega) {
					var newWL kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &newWL)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &newWL, admissions[i])).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
							Message: "Can submit new workloads to clusterQueue",
						},
					},
					FlavorsReservation: fullUsage,
					FlavorUsage:        emptyUsage,
					Flavors: []kueue.LocalQueueFlavorStatus{
						{
							Name:       flavorModelD,
							Resources:  []corev1.ResourceName{"example.com/gpu"},
							NodeLabels: map[string]string{"example.com/gpu": "model-d"},
						},
						{
							Name:       flavorModelC,
							Resources:  []corev1.ResourceName{"example.com/gpu"},
							NodeLabels: map[string]string{"example.com/gpu": "model-c"},
							NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
						},
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Setting the workloads admission checks", func() {
			for _, w := range workloads {
				util.SetWorkloadsAdmissionCheck(ctx, k8sClient, w, ac.Name, kueue.CheckStateReady, true)
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
							Message: "Can submit new workloads to clusterQueue",
						},
					},
					FlavorsReservation: fullUsage,
					FlavorUsage:        fullUsage,
					Flavors: []kueue.LocalQueueFlavorStatus{
						{
							Name:       flavorModelD,
							Resources:  []corev1.ResourceName{"example.com/gpu"},
							NodeLabels: map[string]string{"example.com/gpu": "model-d"},
						},
						{
							Name:       flavorModelC,
							Resources:  []corev1.ResourceName{"example.com/gpu"},
							NodeLabels: map[string]string{"example.com/gpu": "model-c"},
							NodeTaints: []corev1.Taint{{Key: "spot", Value: "true", Effect: "NoSchedule"}},
						},
					},
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
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedClusterQueue)).Should(testing.BeNotFoundError())
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
