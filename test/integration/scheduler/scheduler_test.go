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
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
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
			prodClusterQ          *kueue.ClusterQueue
			devClusterQ           *kueue.ClusterQueue
			podsCountClusterQ     *kueue.ClusterQueue
			podsCountOnlyClusterQ *kueue.ClusterQueue
			prodQueue             *kueue.LocalQueue
			devQueue              *kueue.LocalQueue
			podsCountQueue        *kueue.LocalQueue
			podsCountOnlyQueue    *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).To(gomega.Succeed())

			prodClusterQ = testing.MakeClusterQueue("prod-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

			devClusterQ = testing.MakeClusterQueue("dev-clusterqueue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

			podsCountClusterQ = testing.MakeClusterQueue("pods-count-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "100").
						Resource(corev1.ResourcePods, "5").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountClusterQ)).Should(gomega.Succeed())

			podsCountOnlyClusterQ = testing.MakeClusterQueue("pods-count-only-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourcePods, "5").
						Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountOnlyClusterQ)).Should(gomega.Succeed())

			prodQueue = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())

			podsCountQueue = testing.MakeLocalQueue("pods-count-queue", ns.Name).ClusterQueue(podsCountClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountQueue)).Should(gomega.Succeed())

			podsCountOnlyQueue = testing.MakeLocalQueue("pods-count-only-queue", ns.Name).ClusterQueue(podsCountOnlyClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountOnlyQueue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodClusterQ, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, devClusterQ, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, podsCountClusterQ, true)
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, podsCountOnlyClusterQ, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		})

		ginkgo.It("Should admit workloads as they fit in their ClusterQueue", func() {
			ginkgo.By("checking the first prod workload gets admitted")
			prodWl1 := testing.MakeWorkload("prod-wl1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl1)).Should(gomega.Succeed())
			prodWl1Admission := testing.MakeAdmission(prodClusterQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, prodWl1, prodWl1Admission)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)

			ginkgo.By("checking a second no-fit workload does not get admitted")
			prodWl2 := testing.MakeWorkload("prod-wl2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, prodWl2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)

			ginkgo.By("checking a dev workload gets admitted")
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).Should(gomega.Succeed())
			spotUntaintedFlavorAdmission := testing.MakeAdmission(devClusterQ.Name).Assignment(corev1.ResourceCPU, "spot-untainted", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, devWl, spotUntaintedFlavorAdmission)
			util.ExpectPendingWorkloadsMetric(devClusterQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(devClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(devClusterQ, 1)

			ginkgo.By("checking the second workload gets admitted when the first workload finishes")
			util.FinishWorkloads(ctx, k8sClient, prodWl1)
			prodWl2Admission := testing.MakeAdmission(prodClusterQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, prodWl2, prodWl2Admission)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)
		})

		ginkgo.It("Should admit workloads as number of pods allows it", func() {
			wl1 := testing.MakeWorkload("wl1", ns.Name).
				Queue(podsCountQueue.Name).
				PodSets(*testing.MakePodSet("main", 3).
					Request(corev1.ResourceCPU, "2").
					Obj()).
				Obj()

			ginkgo.By("checking the first workload gets created and admitted", func() {
				gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				wl1Admission := testing.MakeAdmission(podsCountClusterQ.Name).
					Assignment(corev1.ResourceCPU, "on-demand", "6").
					Assignment(corev1.ResourcePods, "on-demand", "3").
					AssignmentPodCount(3).
					Obj()
				util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, wl1Admission)
				util.ExpectPendingWorkloadsMetric(podsCountClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(podsCountClusterQ, 1)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountClusterQ, 1)
			})

			wl2 := testing.MakeWorkload("wl2", ns.Name).
				Queue(podsCountQueue.Name).
				PodSets(*testing.MakePodSet("main", 3).
					Request(corev1.ResourceCPU, "2").
					Obj()).
				Obj()

			wl3 := testing.MakeWorkload("wl3", ns.Name).
				Queue(podsCountQueue.Name).
				PodSets(*testing.MakePodSet("main", 2).
					Request(corev1.ResourceCPU, "2").
					Obj()).
				Obj()

			ginkgo.By("creating the next two workloads", func() {
				gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			})

			ginkgo.By("checking the second workload is pending and the third admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, podsCountClusterQ.Name, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				util.ExpectPendingWorkloadsMetric(podsCountClusterQ, 0, 1)
				util.ExpectAdmittedActiveWorkloadsMetric(podsCountClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountClusterQ, 2)
			})

			ginkgo.By("finishing the first workload", func() {
				util.FinishWorkloads(ctx, k8sClient, wl1)
			})

			ginkgo.By("checking the second workload is also admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, podsCountClusterQ.Name, wl2, wl3)
				util.ExpectPendingWorkloadsMetric(podsCountClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(podsCountClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountClusterQ, 3)
			})
		})

		ginkgo.It("Should admit workloads as the number of pods (only) allows it", func() {
			wl1 := testing.MakeWorkload("wl1", ns.Name).
				Queue(podsCountOnlyQueue.Name).
				PodSets(*testing.MakePodSet("main", 3).
					Obj()).
				Obj()

			ginkgo.By("checking the first workload gets created and admitted", func() {
				gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				wl1Admission := testing.MakeAdmission(podsCountOnlyClusterQ.Name).
					Assignment(corev1.ResourcePods, "on-demand", "3").
					AssignmentPodCount(3).
					Obj()
				util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, wl1Admission)
				util.ExpectPendingWorkloadsMetric(podsCountOnlyClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(podsCountOnlyClusterQ, 1)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountOnlyClusterQ, 1)
			})

			wl2 := testing.MakeWorkload("wl2", ns.Name).
				Queue(podsCountOnlyQueue.Name).
				PodSets(*testing.MakePodSet("main", 3).
					Obj()).
				Obj()

			wl3 := testing.MakeWorkload("wl3", ns.Name).
				Queue(podsCountOnlyQueue.Name).
				PodSets(*testing.MakePodSet("main", 2).
					Obj()).
				Obj()

			ginkgo.By("creating the next two workloads", func() {
				gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			})

			ginkgo.By("checking the second workload is pending and the third admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, podsCountOnlyClusterQ.Name, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				util.ExpectPendingWorkloadsMetric(podsCountOnlyClusterQ, 0, 1)
				util.ExpectAdmittedActiveWorkloadsMetric(podsCountOnlyClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountOnlyClusterQ, 2)
			})

			ginkgo.By("finishing the first workload", func() {
				util.FinishWorkloads(ctx, k8sClient, wl1)
			})

			ginkgo.By("checking the second workload is also admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, podsCountOnlyClusterQ.Name, wl2, wl3)
				util.ExpectPendingWorkloadsMetric(podsCountOnlyClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(podsCountOnlyClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountOnlyClusterQ, 3)
			})
		})

		ginkgo.It("Should admit workloads according to their priorities", func() {
			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()

			const lowPrio, midPrio, highPrio = 0, 10, 100

			wlLow := testing.MakeWorkload("wl-low-priority", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2").Priority(lowPrio).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlLow)).Should(gomega.Succeed())
			wlMid1 := testing.MakeWorkload("wl-mid-priority-1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2").Priority(midPrio).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlMid1)).Should(gomega.Succeed())
			wlMid2 := testing.MakeWorkload("wl-mid-priority-2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2").Priority(midPrio).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlMid2)).Should(gomega.Succeed())
			wlHigh1 := testing.MakeWorkload("wl-high-priority-1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2").Priority(highPrio).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlHigh1)).Should(gomega.Succeed())
			wlHigh2 := testing.MakeWorkload("wl-high-priority-2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "2").Priority(highPrio).Obj()
			gomega.Expect(k8sClient.Create(ctx, wlHigh2)).Should(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 0)
			// delay creating the queue until after workloads are created.
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking the workloads with lower priority do not get admitted")
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wlLow, wlMid1, wlMid2)

			ginkgo.By("checking the workloads with high priority get admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, wlHigh1, wlHigh2)

			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 3)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)

			ginkgo.By("after the high priority workloads finish, only the mid priority workloads should be admitted")
			util.FinishWorkloads(ctx, k8sClient, wlHigh1, wlHigh2)

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, wlMid1, wlMid2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 4)
		})

		ginkgo.It("Should admit two small workloads after a big one finishes", func() {
			bigWl := testing.MakeWorkload("big-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			ginkgo.By("Creating big workload")
			gomega.Expect(k8sClient.Create(ctx, bigWl)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, bigWl)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)

			smallWl1 := testing.MakeWorkload("small-wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			smallWl2 := testing.MakeWorkload("small-wl-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			ginkgo.By("Creating two small workloads")
			gomega.Expect(k8sClient.Create(ctx, smallWl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, smallWl2)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBePending(ctx, k8sClient, smallWl1, smallWl2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 2)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)

			ginkgo.By("Marking the big workload as finished")
			util.FinishWorkloads(ctx, k8sClient, bigWl)

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, smallWl1, smallWl2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 3)
		})

		ginkgo.It("Should admit workloads when resources are dynamically reclaimed", func() {
			firstWl := testing.MakeWorkload("first-wl", ns.Name).Queue(prodQueue.Name).
				PodSets(*testing.MakePodSet("main", 1).Request(corev1.ResourceCPU, "3").Obj()).
				Obj()
			ginkgo.By("Creating first workload", func() {
				gomega.Expect(k8sClient.Create(ctx, firstWl)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, firstWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			})

			secondWl := testing.MakeWorkload("second-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "3").Obj()
			ginkgo.By("Creating the second workload", func() {
				gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBePending(ctx, k8sClient, secondWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
				util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			})

			ginkgo.By("Reclaim one pod from the first workload", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, firstWl, []kueue.ReclaimablePod{{Name: "main", Count: 1}})).To(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, firstWl, secondWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 2)
			})
		})

		ginkgo.It("Should admit workloads with 0 count for podSets due to reclaim", func() {
			firstWl := testing.MakeWorkload("first-wl", ns.Name).Queue(prodQueue.Name).
				PodSets(*testing.MakePodSet("main", 1).Request(corev1.ResourceCPU, "3").Obj()).
				Obj()
			ginkgo.By("Creating first workload", func() {
				gomega.Expect(k8sClient.Create(ctx, firstWl)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, firstWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			})

			secondWl := testing.MakeWorkload("second-wl", ns.Name).Queue(prodQueue.Name).
				Request(corev1.ResourceCPU, "3").
				PodSets(
					*testing.MakePodSet("main", 2).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					*testing.MakePodSet("secondary", 2).
						Request(corev1.ResourceCPU, "3").
						Obj(),
				).
				Obj()
			ginkgo.By("Creating the second workload", func() {
				gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBePending(ctx, k8sClient, secondWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
				util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 1)
			})

			ginkgo.By("Reclaim all the pods from the second PodSet", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, secondWl, []kueue.ReclaimablePod{{Name: "secondary", Count: 2}})).To(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodClusterQ.Name, firstWl, secondWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
				util.ExpectAdmittedActiveWorkloadsMetric(prodClusterQ, 2)
			})
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
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same ClusterQueue", func() {
			ginkgo.By("First big workload starts")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectWl1Admission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "4").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectWl1Admission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Second big workload is pending")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Third small workload starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			expectWl3Admission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectWl3Admission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 2)

			ginkgo.By("Second big workload starts after the first one is deleted")
			gomega.Expect(k8sClient.Delete(ctx, wl1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			expectWl2Admission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "4").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectWl2Admission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 3)
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same Cohort", func() {
			fooCQ := testing.MakeClusterQueue("foo-clusterqueue").
				Cohort(cq.Spec.Cohort).
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, fooCQ)).Should(gomega.Succeed())
			}()

			fooQ := testing.MakeLocalQueue("foo-queue", ns.Name).ClusterQueue(fooCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())

			ginkgo.By("First big workload starts")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(fooCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "8").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 1)

			ginkgo.By("Second big workload is pending")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("Third small workload starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			expectAdmission = testing.MakeAdmission(fooCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 2)

			ginkgo.By("Second big workload starts after the first one is deleted")
			gomega.Expect(k8sClient.Delete(ctx, wl1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			expectAdmission = testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "8").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
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
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})
		ginkgo.It("Should re-enqueue by the update event of ClusterQueue", func() {
			wl := testing.MakeWorkload("on-demand-wl", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "6").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("updating ClusterQueue")
			updatedCq := &kueue.ClusterQueue{}

			gomega.Eventually(func() error {
				err := k8sClient.Get(ctx, types.NamespacedName{Name: cq.Name}, updatedCq)
				if err != nil {
					return err
				}
				updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[0] = kueue.ResourceQuota{
					Name:           corev1.ResourceCPU,
					NominalQuota:   resource.MustParse("6"),
					BorrowingLimit: pointer.Quantity(resource.MustParse("0")),
				}
				return k8sClient.Update(ctx, updatedCq)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "6").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
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
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj()).
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
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workloads from the selected namespaces", func() {
			ginkgo.By("checking the workloads don't get admitted at first")
			wl1 := testing.MakeWorkload("wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", nsFoo.Name).Queue(queueFoo.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 2)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("checking the first workload gets admitted after updating the namespace labels to match CQ selector")
			ns.Labels = map[string]string{"dep": "eng"}
			gomega.Expect(k8sClient.Update(ctx, ns)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, wl1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
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
				ResourceGroup(*testing.MakeFlavorQuotas("foo-flavor").Resource(corev1.ResourceCPU, "15").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCQ)).Should(gomega.Succeed())
			fooQ = testing.MakeLocalQueue("foo-queue", ns.Name).ClusterQueue(fooCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, fooCQ, true)
		})

		ginkgo.It("Should be inactive until the flavor is created", func() {
			ginkgo.By("Creating one workload")
			util.ExpectClusterQueueStatusMetric(fooCQ, metrics.CQStatusPending)
			wl := testing.MakeWorkload("workload", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeFrozen(ctx, k8sClient, fooCQ.Name, wl)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 0)

			ginkgo.By("Creating foo flavor")
			fooFlavor := testing.MakeResourceFlavor("foo-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, fooFlavor)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, fooFlavor)).To(gomega.Succeed())
			}()
			util.ExpectClusterQueueStatusMetric(fooCQ, metrics.CQStatusActive)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, fooCQ.Name, wl)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(fooCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 1)
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
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should schedule workloads on tolerated flavors", func() {
			ginkgo.By("checking a workload without toleration starts on the non-tainted flavor")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("checking a second workload without toleration doesn't start")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("checking a third workload with toleration starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Toleration(spotToleration).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			expectAdmission = testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "spot-tainted", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 2)
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
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotUntaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should admit workloads with affinity to specific flavor", func() {
			ginkgo.By("checking a workload without affinity gets admitted on the first flavor")
			wl1 := testing.MakeWorkload("no-affinity-workload", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "spot-untainted", "1").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)

			ginkgo.By("checking a second workload with affinity to on-demand gets admitted")
			wl2 := testing.MakeWorkload("affinity-wl", ns.Name).Queue(queue.Name).
				NodeSelector(map[string]string{instanceKey: onDemandFlavor.Name, "foo": "bar"}).
				Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			gomega.Expect(len(wl2.Spec.PodSets[0].Template.Spec.NodeSelector)).Should(gomega.Equal(2))
			expectAdmission = testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 2)
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
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			q = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			w = testing.MakeWorkload("workload", ns.Name).Queue(q.Name).Request(corev1.ResourceCPU, "2").Obj()
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workload when creating ResourceFlavor->LocalQueue->Workload->ClusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ResourceFlavor->LocalQueue->ClusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ResourceFlavor->ClusterQueue->LocalQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ClusterQueue->LocalQueue->ResourceFlavor", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, w)
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
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, prodCQ, true)
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, devCQ)).ToNot(gomega.HaveOccurred())
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			gomega.Expect(util.DeleteResourceFlavor(ctx, k8sClient, spotTaintedFlavor)).To(gomega.Succeed())
		})

		ginkgo.It("Should admit workloads using borrowed ClusterQueue", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "0").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking a no-fit workload does not get admitted")
			wl := testing.MakeWorkload("wl", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "10").Toleration(spotToleration).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)

			ginkgo.By("checking the workload gets admitted when a fallback ClusterQueue gets added")
			fallbackClusterQueue := testing.MakeClusterQueue("fallback-cq").
				Cohort(prodCQ.Spec.Cohort).
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5").Obj(), // prod-cq can't borrow this due to its borrowingLimit
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, fallbackClusterQueue)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, fallbackClusterQueue)).ToNot(gomega.HaveOccurred())
			}()

			expectAdmission := testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
		})

		ginkgo.It("Should schedule workloads borrowing quota from ClusterQueues in the same Cohort", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "10").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "10").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).Should(gomega.Succeed())

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			podQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, podQueue)).Should(gomega.Succeed())
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "11").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(podQueue.Name).Request(corev1.ResourceCPU, "11").Obj()

			ginkgo.By("Creating two workloads")
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(devCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(devCQ, 0)

			// Delay cluster queue creation to make sure workloads are in the same
			// scheduling cycle.
			testCQ := testing.MakeClusterQueue("test-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "15", "0").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, testCQ)).Should(gomega.Succeed())
			}()

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodCQ.Name, wl1)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devCQ.Name, wl2)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectAdmittedActiveWorkloadsMetric(devCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(devCQ, 1)
		})

		ginkgo.It("Should start workloads that are under min quota before borrowing", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).To(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "0").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).To(gomega.Succeed())

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).To(gomega.Succeed())

			devQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).To(gomega.Succeed())

			ginkgo.By("Creating two workloads for prod ClusterQueue")
			pWl1 := testing.MakeWorkload("p-wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			pWl2 := testing.MakeWorkload("p-wl-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, pWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, pWl2)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodCQ.Name, pWl1, pWl2)

			ginkgo.By("Creating a workload for each ClusterQueue")
			dWl1 := testing.MakeWorkload("d-wl-1", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			pWl3 := testing.MakeWorkload("p-wl-3", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, dWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, pWl3)).To(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, dWl1, pWl3)

			ginkgo.By("Finishing one workload for prod ClusterQueue")
			util.FinishWorkloads(ctx, k8sClient, pWl1)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, dWl1, pWl3)

			ginkgo.By("Finishing second workload for prod ClusterQueue")
			util.FinishWorkloads(ctx, k8sClient, pWl2)
			// The pWl3 workload gets accepted, even though it was created after dWl1.
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, prodCQ.Name, pWl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, dWl1)

			ginkgo.By("Finishing third workload for prod ClusterQueue")
			util.FinishWorkloads(ctx, k8sClient, pWl3)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, devCQ.Name, dWl1)
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
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "0").Obj()).
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
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, matchingNS)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, strictFIFOClusterQ, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should schedule workloads by their priority strictly", func() {
			strictFIFOQueue := testing.MakeLocalQueue("strict-fifo-q", matchingNS.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()

			ginkgo.By("Creating workloads")
			wl1 := testing.MakeWorkload("wl1", matchingNS.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "2").Priority(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", matchingNS.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "5").Priority(10).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			// wl3 can't be scheduled before wl2 even though there is enough quota.
			wl3 := testing.MakeWorkload("wl3", matchingNS.Name).Queue(strictFIFOQueue.
				Name).Request(corev1.ResourceCPU, "1").Priority(1).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			gomega.Expect(k8sClient.Create(ctx, strictFIFOQueue)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, strictFIFOClusterQ.Name, wl1)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			// wl3 doesn't even get a scheduling attempt, so can't check for conditions.
			gomega.Consistently(func() bool {
				lookupKey := types.NamespacedName{Name: wl3.Name, Namespace: wl3.Namespace}
				gomega.Expect(k8sClient.Get(ctx, lookupKey, wl3)).Should(gomega.Succeed())
				return !workload.IsAdmitted(wl3)
			}, util.ConsistentDuration, util.Interval).Should(gomega.Equal(true))
			util.ExpectPendingWorkloadsMetric(strictFIFOClusterQ, 2, 0)
			util.ExpectAdmittedActiveWorkloadsMetric(strictFIFOClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(strictFIFOClusterQ, 1)
		})

		ginkgo.It("Workloads not matching namespaceSelector should not block others", func() {
			notMatchingQueue := testing.MakeLocalQueue("not-matching-queue", ns.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, notMatchingQueue)).Should(gomega.Succeed())

			matchingQueue := testing.MakeLocalQueue("matching-queue", matchingNS.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, matchingQueue)).Should(gomega.Succeed())

			ginkgo.By("Creating workloads")
			wl1 := testing.MakeWorkload("wl1", matchingNS.Name).Queue(matchingQueue.
				Name).Request(corev1.ResourceCPU, "2").Priority(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", ns.Name).Queue(notMatchingQueue.
				Name).Request(corev1.ResourceCPU, "5").Priority(10).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			// wl2 can't block wl3 from getting scheduled.
			wl3 := testing.MakeWorkload("wl3", matchingNS.Name).Queue(matchingQueue.
				Name).Request(corev1.ResourceCPU, "1").Priority(1).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, strictFIFOClusterQ.Name, wl1, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(strictFIFOClusterQ, 0, 1)
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, false)
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
				gomega.Expect(util.DeleteWorkload(ctx, k8sClient, wl1)).To(gomega.Succeed())
			}()
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, wl1)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(util.DeleteClusterQueue(ctx, k8sClient, cq)).To(gomega.Succeed())
			gomega.Consistently(func() []string {
				var newCQ kueue.ClusterQueue
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				return newCQ.GetFinalizers()
			}, util.ConsistentDuration, util.Interval).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))

			ginkgo.By("New created workloads should be frozen")
			wl2 := testing.MakeWorkload("workload2", ns.Name).Queue(queue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteWorkload(ctx, k8sClient, wl2)).To(gomega.Succeed())
			}()
			util.ExpectWorkloadsToBeFrozen(ctx, k8sClient, cq.Name, wl2)
			util.ExpectAdmittedActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
		})
	})

	ginkgo.When("The workload's podSet resource requests are not valid", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		type testParams struct {
			reqCPU          string
			limitCPU        string
			minCPU          string
			maxCPU          string
			limitType       corev1.LimitType
			wantedStatus    string
			shouldBeAdmited bool
		}

		ginkgo.DescribeTable("", func(tp testParams) {
			lrBuilder := testing.MakeLimitRange("limit", ns.Name)
			if tp.limitType != "" {
				lrBuilder.WithType(tp.limitType)
			}
			if tp.maxCPU != "" {
				lrBuilder.WithValue("Max", corev1.ResourceCPU, tp.maxCPU)
			}
			if tp.minCPU != "" {
				lrBuilder.WithValue("Min", corev1.ResourceCPU, tp.minCPU)
			}
			lr := lrBuilder.Obj()
			gomega.Expect(k8sClient.Create(ctx, lr)).To(gomega.Succeed())

			wlBuilder := testing.MakeWorkload("workload", ns.Name).Queue(queue.Name)

			if tp.reqCPU != "" {
				wlBuilder.Request(corev1.ResourceCPU, tp.reqCPU)
			}
			if tp.limitCPU != "" {
				wlBuilder.Limit(corev1.ResourceCPU, tp.limitCPU)
			}

			wl := wlBuilder.Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			if tp.shouldBeAdmited {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, cq.Name, wl)
			} else {
				gomega.Eventually(func() string {
					rwl := kueue.Workload{}
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &rwl); err != nil {
						return ""
					}

					cond := meta.FindStatusCondition(rwl.Status.Conditions, kueue.WorkloadAdmitted)
					if cond == nil {
						return ""
					}
					return cond.Message
				}, util.Timeout, util.Interval).Should(gomega.ContainSubstring(tp.wantedStatus))
			}
			gomega.Expect(util.DeleteWorkload(ctx, k8sClient, wl)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, lr)).To(gomega.Succeed())
		},
			ginkgo.Entry("request more that limits", testParams{reqCPU: "3", limitCPU: "2", wantedStatus: "resource validation failed:"}),
			ginkgo.Entry("request over container limits", testParams{reqCPU: "2", limitCPU: "3", maxCPU: "1", wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("request under container limits", testParams{reqCPU: "2", limitCPU: "3", minCPU: "3", wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("request over pod limits", testParams{reqCPU: "2", limitCPU: "3", maxCPU: "1", limitType: corev1.LimitTypePod, wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("request under pod limits", testParams{reqCPU: "2", limitCPU: "3", minCPU: "3", limitType: corev1.LimitTypePod, wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("valid", testParams{reqCPU: "2", limitCPU: "3", minCPU: "1", maxCPU: "4", shouldBeAdmited: true}),
		)
	})
})
