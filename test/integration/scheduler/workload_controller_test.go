/*
Copyright 2023 The Kubernetes Authors.

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

package scheduler

import (
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var ignoreCqCondition = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "Conditions")
var ignoreInClusterQueueStatus = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "PendingWorkloadsStatus", "FlavorsUsage", "AdmittedWorkloads")

var _ = ginkgo.Describe("Workload controller with scheduler", func() {
	var (
		ns             *corev1.Namespace
		localQueue     *kueue.LocalQueue
		wl             *kueue.Workload
		onDemandFlavor *kueue.ResourceFlavor
		runtimeClass   *nodev1.RuntimeClass
		clusterQueue   *kueue.ClusterQueue
		updatedCQ      kueue.ClusterQueue
		resources      = corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("1"),
		}
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-workload-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").Obj()
	})

	ginkgo.AfterEach(func() {
		clusterQueue = nil
		localQueue = nil
		updatedCQ = kueue.ClusterQueue{}
	})

	ginkgo.When("the queue has admission check strategies", func() {
		var (
			flavor1           *kueue.ResourceFlavor
			flavor2           *kueue.ResourceFlavor
			check1            *kueue.AdmissionCheck
			check2            *kueue.AdmissionCheck
			check3            *kueue.AdmissionCheck
			reservationFlavor string = "reservation"
			updatedWl         kueue.Workload
			flavorOnDemand    string              = "on-demand"
			resourceGPU       corev1.ResourceName = "example.com/gpu"
		)

		ginkgo.BeforeEach(func() {
			flavor1 = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor1)).Should(gomega.Succeed())

			flavor2 = testing.MakeResourceFlavor(reservationFlavor).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor2)).Should(gomega.Succeed())

			check1 = testing.MakeAdmissionCheck("check1").ControllerName("ctrl1").Obj()
			gomega.Expect(k8sClient.Create(ctx, check1)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = testing.MakeAdmissionCheck("check2").ControllerName("ctrl2").Obj()
			gomega.Expect(k8sClient.Create(ctx, check2)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			check3 = testing.MakeAdmissionCheck("check3").ControllerName("ctrl3").Obj()
			gomega.Expect(k8sClient.Create(ctx, check3)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check3, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				AdmissionCheckStrategy(
					*testing.MakeAdmissionCheckStrategyRule("check1", kueue.ResourceFlavorReference(flavorOnDemand)).Obj(),
					*testing.MakeAdmissionCheckStrategyRule("check2").Obj(),
					*testing.MakeAdmissionCheckStrategyRule("check3", kueue.ResourceFlavorReference(reservationFlavor)).Obj()).
				ResourceGroup(
					*testing.MakeFlavorQuotas(reservationFlavor).Resource(resourceGPU, "1", "1").Obj(),
					*testing.MakeFlavorQuotas(flavorOnDemand).Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())

			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, check3, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, check2, true)
			util.ExpectAdmissionCheckToBeDeleted(ctx, k8sClient, check1, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, flavor1, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, flavor2, true)
		})

		ginkgo.It("the workload should have appropriate AdditionalChecks added", func() {
			wl := testing.MakeWorkload("wl", ns.Name).
				Queue("queue").
				Request(resourceGPU, "3").
				Obj()
			wlKey := client.ObjectKeyFromObject(wl)

			ginkgo.By("creating and waiting for workload to have a quota reservation", func() {
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(workload.HasQuotaReservation(&updatedWl)).Should(gomega.BeTrue(), "should have quota reservation")

					checks := slices.Map(updatedWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
					g.Expect(checks).Should(gomega.ConsistOf("check1", "check2"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Expect(workload.IsAdmitted(&updatedWl)).To(gomega.BeFalse())
			})

			ginkgo.By("adding an additional admission check to the clusterqueue", func() {
				createdQueue := kueue.ClusterQueue{}
				queueKey := client.ObjectKeyFromObject(clusterQueue)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, queueKey, &createdQueue)).To(gomega.Succeed())
					createdQueue.Spec.AdmissionChecksStrategy.AdmissionChecks = []kueue.AdmissionCheckStrategyRule{
						*testing.MakeAdmissionCheckStrategyRule("check1", kueue.ResourceFlavorReference(flavorOnDemand)).Obj(),
						*testing.MakeAdmissionCheckStrategyRule("check2", kueue.ResourceFlavorReference(reservationFlavor)).Obj(),
						*testing.MakeAdmissionCheckStrategyRule("check3").Obj()}
					g.Expect(k8sClient.Update(ctx, &createdQueue)).To(gomega.Succeed())
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					checks := slices.Map(updatedWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
					g.Expect(checks).Should(gomega.ConsistOf("check1", "check3"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("marking the checks as passed", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "check successfully passed",
					})
					workload.SetAdmissionCheckState(&updatedWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check3",
						State:   kueue.CheckStateReady,
						Message: "check successfully passed",
					})
					g.Expect(k8sClient.Status().Update(ctx, &updatedWl)).Should(gomega.Succeed())
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedWl)).Should(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&updatedWl)).Should(gomega.BeTrue(), "should have been admitted")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Workload with RuntimeClass defined", func() {
		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())

			runtimeClass = testing.MakeRuntimeClass("kata", "bar-handler").PodOverhead(resources).Obj()
			gomega.Expect(k8sClient.Create(ctx, runtimeClass)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteRuntimeClass(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should accumulate RuntimeClass's overhead", func() {
			ginkgo.By("Create and wait for workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").
					RuntimeClass("kata").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 1,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("2"),
						}},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})
		})
	})

	ginkgo.When("Workload with non-existent RuntimeClass defined", func() {
		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())

			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should not accumulate RuntimeClass's overhead", func() {
			ginkgo.By("Create and wait for workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").
					RuntimeClass("kata").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 1,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("1"),
						}},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})
		})
	})

	ginkgo.When("LimitRanges are defined", func() {
		ginkgo.BeforeEach(func() {
			limitRange := testing.MakeLimitRange("limits", ns.Name).WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj()
			gomega.Expect(k8sClient.Create(ctx, limitRange)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should use the range defined default requests, if provided", func() {
			ginkgo.By("Create and wait for workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 1,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{
							{
								Name:  corev1.ResourceCPU,
								Total: resource.MustParse("3"),
							},
						},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})

			ginkgo.By("Check podSets spec", func() {
				wlRead := kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &wlRead)).To(gomega.Succeed())
				gomega.Expect(equality.Semantic.DeepEqual(wl.Spec.PodSets, wlRead.Spec.PodSets)).To(gomega.BeTrue())
			})
		})
		ginkgo.It("Should not use the range defined requests, if provided by the workload", func() {
			ginkgo.By("Create and wait for workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 1,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{
							{
								Name:  corev1.ResourceCPU,
								Total: resource.MustParse("1"),
							},
						},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})

			ginkgo.By("Check podSets spec", func() {
				wlRead := kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &wlRead)).To(gomega.Succeed())
				gomega.Expect(equality.Semantic.DeepEqual(wl.Spec.PodSets, wlRead.Spec.PodSets)).To(gomega.BeTrue())
			})
		})
	})

	ginkgo.When("the workload defines only resource limits and the LocalQueue is created late", func() {
		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("The limits should be used as request values", func() {
			ginkgo.By("Create and wait for workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Limit(corev1.ResourceCPU, "1").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 1,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("1"),
						}},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})

			ginkgo.By("Check podSets spec", func() {
				wlRead := kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &wlRead)).To(gomega.Succeed())
				gomega.Expect(equality.Semantic.DeepEqual(wl.Spec.PodSets, wlRead.Spec.PodSets)).To(gomega.BeTrue())
			})
		})
	})

	ginkgo.When("RuntimeClass is defined and change", func() {
		ginkgo.BeforeEach(func() {
			runtimeClass = testing.MakeRuntimeClass("kata", "bar-handler").
				PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, runtimeClass)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteRuntimeClass(ctx, k8sClient, runtimeClass)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should sync the resource requests with the new overhead", func() {
			ginkgo.By("Create and wait for the first workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").
					RuntimeClass("kata").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			var wl2 *kueue.Workload
			ginkgo.By("Create a second workload, should stay pending", func() {
				wl2 = testing.MakeWorkload("two", ns.Name).
					Queue(localQueue.Name).
					Request(corev1.ResourceCPU, "1").
					RuntimeClass("kata").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

				gomega.Consistently(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.ConsistentDuration, util.Interval).Should(gomega.BeFalse())
			})

			ginkgo.By("Decreasing the runtimeClass", func() {
				updatedRC := nodev1.RuntimeClass{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(runtimeClass), &updatedRC)).To(gomega.Succeed())
				updatedRC.Overhead.PodFixed[corev1.ResourceCPU] = resource.MustParse("1")
				gomega.Expect(k8sClient.Update(ctx, &updatedRC)).To(gomega.Succeed())
			})

			ginkgo.By("The second workload now fits and is admitted", func() {
				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				// the total CPU usage in the queue should be 5
				// for the first workload: 3 = 1 (podSet provided) + 2 (initial class overhead, at the time of it's admission)
				// for the second workload: 2 = 1 (podSet provided) + 1 (updated class overhead, at the time of it's admission)
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 2,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("5"),
						}},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})
		})
	})
	ginkgo.When("LimitRanges are defined and change", func() {
		var limitRange *corev1.LimitRange
		ginkgo.BeforeEach(func() {
			limitRange = testing.MakeLimitRange("limits", ns.Name).WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj()
			gomega.Expect(k8sClient.Create(ctx, limitRange)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should sync the resource requests with the limit", func() {
			ginkgo.By("Create and wait for the first workload admission", func() {
				wl = testing.MakeWorkload("one", ns.Name).
					Queue(localQueue.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			var wl2 *kueue.Workload
			ginkgo.By("Create a second workload, should stay pending", func() {
				wl2 = testing.MakeWorkload("two", ns.Name).
					Queue(localQueue.Name).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

				gomega.Consistently(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.ConsistentDuration, util.Interval).Should(gomega.BeFalse())
			})

			ginkgo.By("Decreasing the limit's default", func() {
				updatedLr := corev1.LimitRange{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &updatedLr)).To(gomega.Succeed())
				updatedLr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
				gomega.Expect(k8sClient.Update(ctx, &updatedLr)).To(gomega.Succeed())
			})

			ginkgo.By("The second workload now fits and is admitted", func() {
				gomega.Eventually(func() bool {
					read := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &read); err != nil {
						return false
					}
					return workload.HasQuotaReservation(&read)
				}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			})

			ginkgo.By("Check queue resource consumption", func() {
				// the total CPU usage in the queue should be 5
				// for the first workload: 3 initial limitRange default, at the time of it's admission
				// for the second workload: 2 updated limitRange default, at the time of it's admission
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 2,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("5"),
						}},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})
		})
	})

	ginkgo.When("a LimitRange event occurs near workload deletion time", func() {
		var limitRange *corev1.LimitRange
		ginkgo.BeforeEach(func() {
			limitRange = testing.MakeLimitRange("limits", ns.Name).WithValue("DefaultRequest", corev1.ResourceCPU, "3").Obj()
			gomega.Expect(k8sClient.Create(ctx, limitRange)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("clusterqueue").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			ginkgo.By("Resource consumption should be 0", func() {
				gomega.Eventually(func() kueue.ClusterQueueStatus {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCQ)).To(gomega.Succeed())
					return updatedCQ.Status
				}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 0,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: kueue.ResourceFlavorReference(onDemandFlavor.Name),
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("0"),
						}},
					}},
				}, ignoreCqCondition, ignoreInClusterQueueStatus))
			})
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.When("When the workload is admissible", func() {
			ginkgo.It("Should not consume resources", func() {
				var wl *kueue.Workload
				ginkgo.By("Create the workload", func() {
					wl = testing.MakeWorkload("one", ns.Name).
						Queue(localQueue.Name).
						Request(corev1.ResourceCPU, "1").
						Obj()
					gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
				})

				updatedLr := corev1.LimitRange{}
				ginkgo.By("Preparing the updated limitRange", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &updatedLr)).To(gomega.Succeed())
					updatedLr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
				})
				ginkgo.By("Updating the limitRange and delete the workload", func() {
					gomega.Expect(k8sClient.Update(ctx, &updatedLr)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, wl)).To(gomega.Succeed())
				})
			})
		})

		ginkgo.When("When the workload is not admissible", func() {
			ginkgo.It("Should not consume resources", func() {
				var wl *kueue.Workload
				ginkgo.By("Create the workload", func() {
					wl = testing.MakeWorkload("one", ns.Name).
						Queue(localQueue.Name).
						Request(corev1.ResourceCPU, "7").
						Obj()
					gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
				})
				updatedLr := corev1.LimitRange{}
				ginkgo.By("Preparing the updated limitRange", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(limitRange), &updatedLr)).To(gomega.Succeed())
					updatedLr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
				})
				ginkgo.By("Updating the limitRange and delete the workload", func() {
					gomega.Expect(k8sClient.Update(ctx, &updatedLr)).To(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, wl)).To(gomega.Succeed())
				})
			})
		})
	})
})
