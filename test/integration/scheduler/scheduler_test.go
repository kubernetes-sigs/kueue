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

package scheduler

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Scheduler", func() {
	const (
		instanceKey = "cloud.provider.com/instance"
	)

	var (
		ns                  *corev1.Namespace
		onDemandFlavor      *kueue.ResourceFlavor
		spotTaintedFlavor   *kueue.ResourceFlavor
		spotUntaintedFlavor *kueue.ResourceFlavor
		spotToleration      corev1.Toleration
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").Label(instanceKey, "on-demand").Obj()

		spotTaintedFlavor = testing.MakeResourceFlavor("spot-tainted").
			Label(instanceKey, "spot-tainted").
			Taint(corev1.Taint{
				Key:    instanceKey,
				Value:  "spot-tainted",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()

		spotToleration = corev1.Toleration{
			Key:      instanceKey,
			Operator: corev1.TolerationOpEqual,
			Value:    spotTaintedFlavor.Name,
			Effect:   corev1.TaintEffectNoSchedule,
		}

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").Label(instanceKey, "spot-untainted").Obj()
	})

	ginkgo.When("Scheduling workloads on clusterQueues", func() {
		var (
			prodClusterQ *kueue.ClusterQueue
			devClusterQ  *kueue.ClusterQueue
			prodQueue    *kueue.LocalQueue
			devQueue     *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).To(gomega.Succeed())

			prodClusterQ = testing.MakeClusterQueue("prod-cq").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

			devClusterQ = testing.MakeClusterQueue("dev-clusterqueue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotUntaintedFlavor.Name, "5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

			prodQueue = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodClusterQ, true)
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, devClusterQ, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		})

		ginkgo.It("Should admit workloads as they fit in their ClusterQueue", func() {
			ginkgo.By("checking the first prod workload gets admitted")
			prodWl1 := testing.MakeWorkload("prod-wl1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl1)).Should(gomega.Succeed())
			onDemandFlavorAdmission := testing.MakeAdmission(prodClusterQ.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, prodWl1, onDemandFlavorAdmission)
			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)

			ginkgo.By("checking a second no-fit workload does not get admitted")
			prodWl2 := testing.MakeWorkload("prod-wl2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, prodWl2)
			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)

			ginkgo.By("checking a dev workload gets admitted")
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).Should(gomega.Succeed())
			spotUntaintedFlavorAdmission := testing.MakeAdmission(devClusterQ.Name).Flavor(corev1.ResourceCPU, spotUntaintedFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, devWl, spotUntaintedFlavorAdmission)
			framework.ExpectPendingWorkloadsMetric(devClusterQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(devClusterQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(devClusterQ, 1)

			ginkgo.By("checking the second workload gets admitted when the first workload finishes")
			framework.FinishWorkloads(ctx, k8sClient, prodWl1)
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, prodWl2, onDemandFlavorAdmission)
			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)
		})

		ginkgo.It("Should admit workloads according to their priorities", func() {
			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()

			lowPriorityVal, highPriorityVal := int32(10), int32(100)

			wlLowPriority := testing.MakeWorkload("wl-low-priority", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Priority(&lowPriorityVal).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlLowPriority)).Should(gomega.Succeed())
			wlHighPriority := testing.MakeWorkload("wl-high-priority", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Priority(&highPriorityVal).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlHighPriority)).Should(gomega.Succeed())

			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 0)
			// delay creating the queue until after workloads are created.
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking the workload with low priority does not get admitted")
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wlLowPriority)

			ginkgo.By("checking the workload with high priority gets admitted")
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, wlHighPriority)

			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)
		})

		ginkgo.It("Should admit two small workloads after a big one finishes", func() {
			bigWl := testing.MakeWorkload("big-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			ginkgo.By("Creating big workload")
			gomega.Expect(k8sClient.Create(ctx, bigWl)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, bigWl)
			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)

			smallWl1 := testing.MakeWorkload("small-wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			smallWl2 := testing.MakeWorkload("small-wl-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			ginkgo.By("Creating two small workloads")
			gomega.Expect(k8sClient.Create(ctx, smallWl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, smallWl2)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBePending(ctx, k8sClient, smallWl1, smallWl2)
			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 2)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)

			ginkgo.By("Marking the big workload as finished")
			framework.FinishWorkloads(ctx, k8sClient, bigWl)

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, smallWl1, smallWl2)
			framework.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 2)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 3)
		})
	})

	ginkgo.When("Handling workloads events", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Cohort("prod").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same ClusterQueue", func() {
			ginkgo.By("First big workload starts")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Second big workload is pending")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Third small workload starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 2)

			ginkgo.By("Second big workload starts after the first one is deleted")
			gomega.Expect(k8sClient.Delete(ctx, wl1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 3)
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same Cohort", func() {
			fooCQ := testing.MakeClusterQueue("foo-clusterqueue").
				Cohort(cq.Spec.Cohort).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, fooCQ)).Should(gomega.Succeed())
			}()

			fooQ := testing.MakeLocalQueue("foo-queue", ns.Name).ClusterQueue(fooCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())

			ginkgo.By("First big workload starts")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(fooCQ.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 1)

			ginkgo.By("Second big workload is pending")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("Third small workload starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			expectAdmission = testing.MakeAdmission(fooCQ.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 2)
			framework.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 2)

			ginkgo.By("Second big workload starts after the first one is deleted")
			gomega.Expect(k8sClient.Delete(ctx, wl1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			expectAdmission = testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
		})
	})

	ginkgo.When("Handling clusterQueue events", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})
		ginkgo.It("Should re-enqueue by the update event of ClusterQueue", func() {
			wl := testing.MakeWorkload("on-demand-wl", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "6").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("updating ClusterQueue")
			updatedCq := &kueue.ClusterQueue{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: cq.Name}, updatedCq)).Should(gomega.Succeed())

			updatedResource := testing.MakeResource(corev1.ResourceCPU).Flavor(testing.MakeFlavor(onDemandFlavor.Name, "6").Max("6").Obj()).Obj()
			updatedCq.Spec.Resources = []kueue.Resource{*updatedResource}
			gomega.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
		})
	})

	ginkgo.When("Using clusterQueue NamespaceSelector", func() {
		var (
			cq       *kueue.ClusterQueue
			queue    *kueue.LocalQueue
			nsFoo    *corev1.Namespace
			queueFoo *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue-with-selector").
				NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						},
					},
				}).
				Resource(testing.MakeResource(corev1.ResourceCPU).Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			nsFoo = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "foo-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, nsFoo)).To(gomega.Succeed())
			queueFoo = testing.MakeLocalQueue("foo", nsFoo.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queueFoo)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workloads from the selected namespaces", func() {
			ginkgo.By("checking the workloads don't get admitted at first")
			wl1 := testing.MakeWorkload("wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", nsFoo.Name).Queue(queueFoo.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 2)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("checking the first workload gets admitted after updating the namespace labels to match CQ selector")
			ns.Labels = map[string]string{"dep": "eng"}
			gomega.Expect(k8sClient.Update(ctx, ns)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, wl1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
		})
	})

	ginkgo.When("Referencing resourceFlavors in clusterQueue", func() {
		var (
			fooCQ *kueue.ClusterQueue
			fooQ  *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			fooCQ = testing.MakeClusterQueue("foo-cq").
				QueueingStrategy(kueue.BestEffortFIFO).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor("foo-flavor", "15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCQ)).Should(gomega.Succeed())
			fooQ = testing.MakeLocalQueue("foo-queue", ns.Name).ClusterQueue(fooCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, fooCQ, true)
		})

		ginkgo.It("Should be inactive until the flavor is created", func() {
			ginkgo.By("Creating one workload")
			framework.ExpectClusterQueueStatusMetric(fooCQ, metrics.CQStatusPending)
			wl := testing.MakeWorkload("workload", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBeFrozen(ctx, k8sClient, fooCQ.Name, wl)
			framework.ExpectPendingWorkloadsMetric(fooCQ, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 0)

			ginkgo.By("Creating foo flavor")
			fooFlavor := testing.MakeResourceFlavor("foo-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, fooFlavor)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, fooFlavor)).To(gomega.Succeed())
			}()
			framework.ExpectClusterQueueStatusMetric(fooCQ, metrics.CQStatusActive)
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, fooCQ.Name, wl)
			framework.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 1)
		})
	})

	ginkgo.When("Using taints in resourceFlavors", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				QueueingStrategy(kueue.BestEffortFIFO).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule workloads on tolerated flavors", func() {
			ginkgo.By("checking a workload without toleration starts on the non-tainted flavor")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("checking a second workload without toleration doesn't start")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("checking a third workload with toleration starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Toleration(spotToleration).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			expectAdmission = testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, spotTaintedFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 2)
		})
	})

	ginkgo.When("Using affinity in resourceFlavors", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotUntaintedFlavor.Name, "5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should admit workloads with affinity to specific flavor", func() {
			ginkgo.By("checking a workload without affinity gets admitted on the first flavor")
			wl1 := testing.MakeWorkload("no-affinity-workload", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, spotUntaintedFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)

			ginkgo.By("checking a second workload with affinity to on-demand gets admitted")
			wl2 := testing.MakeWorkload("affinity-wl", ns.Name).Queue(queue.Name).
				NodeSelector(map[string]string{instanceKey: onDemandFlavor.Name, "foo": "bar"}).
				Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			gomega.Expect(len(wl2.Spec.PodSets[0].Spec.NodeSelector)).Should(gomega.Equal(2))
			expectAdmission = testing.MakeAdmission(cq.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 2)
		})
	})

	ginkgo.When("Creating objects out-of-order", func() {
		var (
			cq *kueue.ClusterQueue
			q  *kueue.LocalQueue
			w  *kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			cq = testing.MakeClusterQueue("cq").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			q = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			w = testing.MakeWorkload("workload", ns.Name).Queue(q.Name).Request(corev1.ResourceCPU, "2").Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workload when creating ResourceFlavor->LocalQueue->Workload->ClusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ResourceFlavor->LocalQueue->ClusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ResourceFlavor->ClusterQueue->LocalQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ClusterQueue->LocalQueue->ResourceFlavor", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})
	})

	ginkgo.When("Using cohorts for fair-sharing", func() {
		var (
			prodCQ *kueue.ClusterQueue
			devCQ  *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodCQ, true)
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, devCQ)).ToNot(gomega.HaveOccurred())
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(framework.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should admit workloads using borrowed ClusterQueue", func() {
			prodCQ = testing.MakeClusterQueue("prod-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Max("5").Obj()).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking a no-fit workload does not get admitted")
			wl := testing.MakeWorkload("wl", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "10").Toleration(spotToleration).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			framework.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)

			ginkgo.By("checking the workload gets admitted when a fallback ClusterQueue gets added")
			fallbackClusterQueue := testing.MakeClusterQueue("fallback-cq").
				Cohort(prodCQ.Spec.Cohort).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(spotTaintedFlavor.Name, "5").Obj()). // cluster-queue can't borrow this flavor due to its max quota.
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fallbackClusterQueue)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, fallbackClusterQueue)).ToNot(gomega.HaveOccurred())
			}()

			expectAdmission := testing.MakeAdmission(prodCQ.Name).Flavor(corev1.ResourceCPU, onDemandFlavor.Name).Obj()
			framework.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			framework.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
		})

		ginkgo.It("Should schedule workloads borrowing quota from ClusterQueues in the same Cohort", func() {
			prodCQ = testing.MakeClusterQueue("prod-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Max("15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Max("15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).Should(gomega.Succeed())

			prodBEQueue := testing.MakeLocalQueue("prod-be-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodBEQueue)).Should(gomega.Succeed())

			devBEQueue := testing.MakeLocalQueue("dev-be-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devBEQueue)).Should(gomega.Succeed())
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodBEQueue.Name).Request(corev1.ResourceCPU, "11").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(devBEQueue.Name).Request(corev1.ResourceCPU, "11").Obj()

			ginkgo.By("Creating two workloads")
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			framework.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			framework.ExpectPendingWorkloadsMetric(devCQ, 0, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(devCQ, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)
			framework.ExpectAdmittedWorkloadsTotalMetric(devCQ, 0)

			// Delay cluster queue creation to make sure workloads are in the same
			// scheduling cycle.
			testBEClusterQ := testing.MakeClusterQueue("test-be-cq").
				Cohort("be").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name,
						"15").Max("15").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testBEClusterQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, testBEClusterQ)).Should(gomega.Succeed())
			}()

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodCQ.Name, wl1)
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devCQ.Name, wl2)
			framework.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			framework.ExpectPendingWorkloadsMetric(devCQ, 0, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 1)
			framework.ExpectAdmittedActiveWorkloadsMetric(devCQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(devCQ, 1)
		})

		ginkgo.It("Should start workloads that are under min quota before borrowing", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO). // BestEffortFIFO has less guarantees.
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "2").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).To(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "0").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).To(gomega.Succeed())

			prodQ := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQ)).To(gomega.Succeed())

			devQ := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQ)).To(gomega.Succeed())

			devWl1 := testing.MakeWorkload("dev-wl-1", ns.Name).Queue(devQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			devWl2 := testing.MakeWorkload("dev-wl-2", ns.Name).Queue(devQ.Name).Request(corev1.ResourceCPU, "1").Obj()

			ginkgo.By("Creating two workloads for dev ClusterQueue")
			gomega.Expect(k8sClient.Create(ctx, devWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, devWl2)).To(gomega.Succeed())
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devCQ.Name, devWl1, devWl2)

			ginkgo.By("Creating a workload for each ClusterQueue")
			devWl3 := testing.MakeWorkload("dev-wl-3", ns.Name).Queue(devQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(prodQ.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl3)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, prodWl)).To(gomega.Succeed())

			framework.ExpectWorkloadsToBePending(ctx, k8sClient, devWl3, prodWl)

			ginkgo.By("Finishing one workload from the dev ClusterQueue")
			framework.FinishWorkloads(ctx, k8sClient, devWl1)
			framework.ExpectPendingWorkloadsMetric(devCQ, 0, 1)
			framework.ExpectPendingWorkloadsMetric(prodCQ, 1, 0)

			ginkgo.By("Finishing second workload from the dev ClusterQueue")
			framework.FinishWorkloads(ctx, k8sClient, devWl2)

			// The prod workload gets accepted, even though it was created after pWl3.
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodCQ.Name, prodWl)
			framework.ExpectPendingWorkloadsMetric(devCQ, 0, 1)
		})
	})

	ginkgo.When("Queueing with StrictFIFO", func() {
		var (
			strictFIFOClusterQ *kueue.ClusterQueue
			matchingNS         *corev1.Namespace
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			strictFIFOClusterQ = testing.MakeClusterQueue("strict-fifo-cq").
				QueueingStrategy(kueue.StrictFIFO).
				NamespaceSelector(&metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "dep",
							Operator: metav1.LabelSelectorOpIn,
							Values:   []string{"eng"},
						},
					},
				}).
				Resource(testing.MakeResource(corev1.ResourceCPU).
					Flavor(testing.MakeFlavor(onDemandFlavor.Name, "5").Max("5").Obj()).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, strictFIFOClusterQ)).Should(gomega.Succeed())
			matchingNS = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "foo-",
					Labels:       map[string]string{"dep": "eng"},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, matchingNS)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, matchingNS)).To(gomega.Succeed())
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, strictFIFOClusterQ, true)
			framework.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should schedule workloads by their priority strictly", func() {
			strictFIFOQueue := testing.MakeLocalQueue("strict-fifo-q", matchingNS.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()

			ginkgo.By("Creating workloads")
			wl1 := testing.MakeWorkload("wl1", matchingNS.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "2").Priority(pointer.Int32(100)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", matchingNS.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "5").Priority(pointer.Int32(10)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			// wl3 can't be scheduled before wl2 even though there is enough quota.
			wl3 := testing.MakeWorkload("wl3", matchingNS.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "1").Priority(pointer.Int32(1)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			gomega.Expect(k8sClient.Create(ctx, strictFIFOQueue)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, strictFIFOClusterQ.Name, wl1)
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			// wl3 doesn't even get a scheduling attempt.
			gomega.Consistently(func() bool {
				lookupKey := types.NamespacedName{Name: wl3.Name, Namespace: wl3.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, wl3)).Should(gomega.Succeed())
				return wl3.Spec.Admission == nil
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal(true))
			framework.ExpectPendingWorkloadsMetric(strictFIFOClusterQ, 2, 0)
			framework.ExpectAdmittedActiveWorkloadsMetric(strictFIFOClusterQ, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(strictFIFOClusterQ, 1)
		})

		ginkgo.It("Workloads not matching namespaceSelector should not block others", func() {
			notMatchingQueue := testing.MakeLocalQueue("not-matching-queue", ns.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, notMatchingQueue)).Should(gomega.Succeed())

			matchingQueue := testing.MakeLocalQueue("matching-queue", matchingNS.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, matchingQueue)).Should(gomega.Succeed())

			ginkgo.By("Creating workloads")
			wl1 := testing.MakeWorkload("wl1", matchingNS.Name).Queue(matchingQueue.
				Name).Request(corev1.ResourceCPU, "2").Priority(pointer.Int32(100)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", ns.Name).Queue(notMatchingQueue.
				Name).Request(corev1.ResourceCPU, "5").Priority(pointer.Int32(10)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			// wl2 can't block wl3 from getting scheduled.
			wl3 := testing.MakeWorkload("wl3", matchingNS.Name).Queue(matchingQueue.
				Name).Request(corev1.ResourceCPU, "1").Priority(pointer.Int32(1)).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, strictFIFOClusterQ.Name, wl1, wl3)
			framework.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			framework.ExpectPendingWorkloadsMetric(strictFIFOClusterQ, 0, 1)
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.AfterEach(func() {
			gomega.Expect(framework.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			framework.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, false)
		})

		ginkgo.It("Should not admit new created workloads", func() {
			ginkgo.By("Create clusterQueue")
			cq = testing.MakeClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("New created workloads should be admitted")
			wl1 := testing.MakeWorkload("workload1", ns.Name).Queue(queue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteWorkload(ctx, k8sClient, wl1)).To(gomega.Succeed())
			}()
			framework.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, wl1)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(framework.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			gomega.Consistently(func() []string {
				var newCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				return newCQ.GetFinalizers()
			}, framework.ConsistentDuration, framework.Interval).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))

			ginkgo.By("New created workloads should be frozen")
			wl2 := testing.MakeWorkload("workload2", ns.Name).Queue(queue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(framework.DeleteWorkload(ctx, k8sClient, wl2)).To(gomega.Succeed())
			}()
			framework.ExpectWorkloadsToBeFrozen(ctx, k8sClient, cq.Name, wl2)
			framework.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			framework.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			framework.ExpectPendingWorkloadsMetric(cq, 0, 1)
		})
	})
})
