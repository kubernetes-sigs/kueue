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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

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
		_ = features.SetEnable(features.FlavorFungibility, true)
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		onDemandFlavor = testing.MakeResourceFlavor("on-demand").NodeLabel(instanceKey, "on-demand").Obj()

		spotTaintedFlavor = testing.MakeResourceFlavor("spot-tainted").
			NodeLabel(instanceKey, "spot-tainted").
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

		spotUntaintedFlavor = testing.MakeResourceFlavor("spot-untainted").NodeLabel(instanceKey, "spot-untainted").Obj()
	})

	ginkgo.When("Scheduling workloads on clusterQueues", func() {
		var (
			admissionCheck1        *kueue.AdmissionCheck
			admissionCheck2        *kueue.AdmissionCheck
			prodClusterQ           *kueue.ClusterQueue
			devClusterQ            *kueue.ClusterQueue
			podsCountClusterQ      *kueue.ClusterQueue
			podsCountOnlyClusterQ  *kueue.ClusterQueue
			preemptionClusterQ     *kueue.ClusterQueue
			admissionCheckClusterQ *kueue.ClusterQueue
			prodQueue              *kueue.LocalQueue
			devQueue               *kueue.LocalQueue
			podsCountQueue         *kueue.LocalQueue
			podsCountOnlyQueue     *kueue.LocalQueue
			preemptionQueue        *kueue.LocalQueue
			admissionCheckQueue    *kueue.LocalQueue
			cqsStopPolicy          *kueue.StopPolicy
			lqsStopPolicy          *kueue.StopPolicy
		)

		ginkgo.JustBeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).To(gomega.Succeed())
			cqsStopPolicy := ptr.Deref(cqsStopPolicy, kueue.None)
			lqsStopPolicy := ptr.Deref(lqsStopPolicy, kueue.None)

			admissionCheck1 = testing.MakeAdmissionCheck("check1").ControllerName("ctrl").Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheck1)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck1, metav1.ConditionTrue)

			admissionCheck2 = testing.MakeAdmissionCheck("check2").ControllerName("ctrl").Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheck2)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck2, metav1.ConditionTrue)

			prodClusterQ = testing.MakeClusterQueue("prod-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5", "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				Cohort("prod-cohort").
				StopPolicy(cqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodClusterQ)).Should(gomega.Succeed())

			devClusterQ = testing.MakeClusterQueue("dev-clusterqueue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "5").Obj(),
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				StopPolicy(cqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devClusterQ)).Should(gomega.Succeed())

			podsCountClusterQ = testing.MakeClusterQueue("pods-count-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "100").
						Resource(corev1.ResourcePods, "5").
						Obj(),
				).
				StopPolicy(cqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountClusterQ)).Should(gomega.Succeed())

			podsCountOnlyClusterQ = testing.MakeClusterQueue("pods-count-only-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourcePods, "5").
						Obj(),
				).
				StopPolicy(cqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountOnlyClusterQ)).Should(gomega.Succeed())

			preemptionClusterQ = testing.MakeClusterQueue("preemption-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "3").Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				StopPolicy(cqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, preemptionClusterQ)).Should(gomega.Succeed())

			admissionCheckClusterQ = testing.MakeClusterQueue("admission-check-cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionChecks("check1", "check2").
				StopPolicy(cqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheckClusterQ)).Should(gomega.Succeed())

			prodQueue = testing.MakeLocalQueue("prod-queue", ns.Name).
				StopPolicy(lqsStopPolicy).
				ClusterQueue(prodClusterQ.Name).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue = testing.MakeLocalQueue("dev-queue", ns.Name).
				ClusterQueue(devClusterQ.Name).
				StopPolicy(lqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())

			podsCountQueue = testing.MakeLocalQueue("pods-count-queue", ns.Name).
				ClusterQueue(podsCountClusterQ.Name).
				StopPolicy(lqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountQueue)).Should(gomega.Succeed())

			podsCountOnlyQueue = testing.MakeLocalQueue("pods-count-only-queue", ns.Name).
				ClusterQueue(podsCountOnlyClusterQ.Name).
				StopPolicy(lqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, podsCountOnlyQueue)).Should(gomega.Succeed())

			preemptionQueue = testing.MakeLocalQueue("preemption-queue", ns.Name).
				ClusterQueue(preemptionClusterQ.Name).
				StopPolicy(lqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, preemptionQueue)).Should(gomega.Succeed())

			admissionCheckQueue = testing.MakeLocalQueue("admission-check-queue", ns.Name).
				ClusterQueue(admissionCheckClusterQ.Name).
				StopPolicy(lqsStopPolicy).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, admissionCheckQueue)).Should(gomega.Succeed())
		})

		ginkgo.JustAfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prodClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, devClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, podsCountClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, podsCountOnlyClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheckClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		})

		ginkgo.It("Should admit workloads as they fit in their ClusterQueue", framework.SlowSpec, func() {
			ginkgo.By("checking the first prod workload gets admitted")
			prodWl1 := testing.MakeWorkload("prod-wl1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl1)).Should(gomega.Succeed())
			prodWl1Admission := testing.MakeAdmission(prodClusterQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, prodWl1, prodWl1Admission)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)

			ginkgo.By("checking a second no-fit workload does not get admitted")
			prodWl2 := testing.MakeWorkload("prod-wl2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, prodWl2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)

			ginkgo.By("checking a workload with replica count 0 gets admitted")
			emptyWl := testing.MakeWorkload("empty-wl", ns.Name).
				Queue(prodQueue.Name).
				PodSets(*testing.MakePodSet("main", 0).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, emptyWl)).Should(gomega.Succeed())

			ginkgo.By("checking jobSet with no replica do nopt modify metrics", func() {
				emptyWlAdmission := testing.MakeAdmission(prodClusterQ.Name).PodSets(
					kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: "on-demand",
						},
						ResourceUsage: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("0"),
						},
						Count: ptr.To[int32](0),
					}).Obj()

				util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, emptyWl, emptyWlAdmission)
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, emptyWl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)
			})

			ginkgo.By("finishing the empty workload", func() {
				util.FinishWorkloads(ctx, k8sClient, emptyWl)
			})

			ginkgo.By("checking a dev workload gets admitted")
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).Should(gomega.Succeed())
			spotUntaintedFlavorAdmission := testing.MakeAdmission(devClusterQ.Name).Assignment(corev1.ResourceCPU, "spot-untainted", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, devWl, spotUntaintedFlavorAdmission)
			util.ExpectPendingWorkloadsMetric(devClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(devClusterQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(devClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(devClusterQ, 1)

			ginkgo.By("checking the second workload gets admitted when the first workload finishes")
			util.FinishWorkloads(ctx, k8sClient, prodWl1)
			prodWl2Admission := testing.MakeAdmission(prodClusterQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, prodWl2, prodWl2Admission)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 3)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 3)
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
				util.ExpectReservingActiveWorkloadsMetric(podsCountClusterQ, 1)
				util.ExpectQuotaReservedWorkloadsTotalMetric(podsCountClusterQ, 1)
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
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, podsCountClusterQ.Name, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				util.ExpectPendingWorkloadsMetric(podsCountClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(podsCountClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(podsCountClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountClusterQ, 2)
			})

			ginkgo.By("finishing the first workload", func() {
				util.FinishWorkloads(ctx, k8sClient, wl1)
			})

			ginkgo.By("checking the second workload is also admitted", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, podsCountClusterQ.Name, wl2, wl3)
				util.ExpectPendingWorkloadsMetric(podsCountClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(podsCountClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(podsCountClusterQ, 3)
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
				util.ExpectReservingActiveWorkloadsMetric(podsCountOnlyClusterQ, 1)
				util.ExpectQuotaReservedWorkloadsTotalMetric(podsCountOnlyClusterQ, 1)
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
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, podsCountOnlyClusterQ.Name, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				util.ExpectPendingWorkloadsMetric(podsCountOnlyClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(podsCountOnlyClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(podsCountOnlyClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountOnlyClusterQ, 2)
			})

			ginkgo.By("finishing the first workload", func() {
				util.FinishWorkloads(ctx, k8sClient, wl1)
			})

			ginkgo.By("checking the second workload is also admitted", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, podsCountOnlyClusterQ.Name, wl2, wl3)
				util.ExpectPendingWorkloadsMetric(podsCountOnlyClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(podsCountOnlyClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(podsCountOnlyClusterQ, 3)
				util.ExpectAdmittedWorkloadsTotalMetric(podsCountOnlyClusterQ, 3)
			})
		})

		ginkgo.It("Should admit workloads when resources are dynamically reclaimed", func() {
			firstWl := testing.MakeWorkload("first-wl", ns.Name).Queue(preemptionQueue.Name).
				PodSets(
					*testing.MakePodSet("first", 1).Request(corev1.ResourceCPU, "1").Obj(),
					*testing.MakePodSet("second", 1).Request(corev1.ResourceCPU, "1").Obj(),
					*testing.MakePodSet("third", 1).Request(corev1.ResourceCPU, "1").Obj(),
				).
				Obj()
			ginkgo.By("Creating first workload", func() {
				gomega.Expect(k8sClient.Create(ctx, firstWl)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, firstWl)
				util.ExpectPendingWorkloadsMetric(preemptionClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(preemptionClusterQ, 1)
			})

			ginkgo.By("Reclaim one pod from the first workload", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, firstWl, []kueue.ReclaimablePod{{Name: "third", Count: 1}})).To(gomega.Succeed())

				util.ExpectPendingWorkloadsMetric(preemptionClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(preemptionClusterQ, 1)
			})

			secondWl := testing.MakeWorkload("second-wl", ns.Name).Queue(preemptionQueue.Name).
				PodSets(
					*testing.MakePodSet("first", 1).Request(corev1.ResourceCPU, "1").Obj(),
					*testing.MakePodSet("second", 1).Request(corev1.ResourceCPU, "1").Obj(),
					*testing.MakePodSet("third", 1).Request(corev1.ResourceCPU, "1").Obj(),
				).
				Priority(100).
				Obj()
			ginkgo.By("Creating the second workload", func() {
				gomega.Expect(k8sClient.Create(ctx, secondWl)).Should(gomega.Succeed())

				util.FinishEvictionForWorkloads(ctx, k8sClient, firstWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, secondWl)
				util.ExpectPendingWorkloadsMetric(preemptionClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(preemptionClusterQ, 1)
			})

			ginkgo.By("Reclaim two pods from the second workload so that the first workload is resumed", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, secondWl, []kueue.ReclaimablePod{{Name: "first", Count: 1}, {Name: "second", Count: 1}})).To(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, firstWl, secondWl)
				util.ExpectPendingWorkloadsMetric(preemptionClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(preemptionClusterQ, 2)
			})
		})

		ginkgo.It("Should admit workloads with admission checks", func() {
			wl1 := testing.MakeWorkload("admission-check-wl1", ns.Name).
				Queue(admissionCheckQueue.Name).
				Request(corev1.ResourceCPU, "2").
				Obj()

			ginkgo.By("checking the first workload gets created and gets quota reserved", func() {
				gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, nil)
				util.ExpectPendingWorkloadsMetric(admissionCheckClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(admissionCheckClusterQ, 1)
				util.ExpectQuotaReservedWorkloadsTotalMetric(admissionCheckClusterQ, 1)
				util.ExpectAdmittedWorkloadsTotalMetric(admissionCheckClusterQ, 0)
			})
		})

		ginkgo.When("Hold ClusterQueue at startup", func() {
			ginkgo.BeforeEach(func() {
				cqsStopPolicy = ptr.To(kueue.Hold)
			})
			ginkgo.AfterEach(func() {
				cqsStopPolicy = nil
			})
			ginkgo.It("Should admit workloads according to their priorities", func() {
				const lowPrio, midPrio, highPrio = 0, 10, 100

				wlLow := testing.MakeWorkload("wl-low-priority", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(lowPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlLow)).Should(gomega.Succeed())
				wlMid1 := testing.MakeWorkload("wl-mid-priority-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(midPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlMid1)).Should(gomega.Succeed())
				wlMid2 := testing.MakeWorkload("wl-mid-priority-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(midPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlMid2)).Should(gomega.Succeed())
				wlHigh1 := testing.MakeWorkload("wl-high-priority-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(highPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlHigh1)).Should(gomega.Succeed())
				wlHigh2 := testing.MakeWorkload("wl-high-priority-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(highPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlHigh2)).Should(gomega.Succeed())

				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 5)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 0)

				util.UnholdClusterQueue(ctx, k8sClient, prodClusterQ)

				ginkgo.By("checking the workloads with lower priority do not get admitted")
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wlLow, wlMid1, wlMid2)

				ginkgo.By("checking the workloads with high priority get admitted")
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wlHigh1, wlHigh2)

				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 3)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)

				ginkgo.By("after the high priority workloads finish, only the mid priority workloads should be admitted")
				util.FinishWorkloads(ctx, k8sClient, wlHigh1, wlHigh2)

				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wlMid1, wlMid2)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 4)
				util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 4)
			})
		})

		ginkgo.When("Hold LocalQueue at startup", func() {
			ginkgo.BeforeEach(func() {
				lqsStopPolicy = ptr.To(kueue.Hold)
			})
			ginkgo.AfterEach(func() {
				lqsStopPolicy = nil
			})
			ginkgo.It("Should admit workloads according to their priorities", func() {
				const lowPrio, midPrio, highPrio = 0, 10, 100

				wlLow := testing.MakeWorkload("wl-low-priority", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(lowPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlLow)).Should(gomega.Succeed())
				wlMid1 := testing.MakeWorkload("wl-mid-priority-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(midPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlMid1)).Should(gomega.Succeed())
				wlMid2 := testing.MakeWorkload("wl-mid-priority-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(midPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlMid2)).Should(gomega.Succeed())
				wlHigh1 := testing.MakeWorkload("wl-high-priority-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(highPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlHigh1)).Should(gomega.Succeed())
				wlHigh2 := testing.MakeWorkload("wl-high-priority-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2").Priority(highPrio).Obj()
				gomega.Expect(k8sClient.Create(ctx, wlHigh2)).Should(gomega.Succeed())

				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 0)

				util.UnholdLocalQueue(ctx, k8sClient, prodQueue)

				ginkgo.By("checking the workloads with lower priority do not get admitted")
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wlLow, wlMid1, wlMid2)

				ginkgo.By("checking the workloads with high priority get admitted")
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wlHigh1, wlHigh2)

				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 3)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 2)
				util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)

				ginkgo.By("after the high priority workloads finish, only the mid priority workloads should be admitted")
				util.FinishWorkloads(ctx, k8sClient, wlHigh1, wlHigh2)

				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wlMid1, wlMid2)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
				util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 4)
				util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 4)
			})
		})

		ginkgo.It("Should admit two small workloads after a big one finishes", func() {
			bigWl := testing.MakeWorkload("big-wl", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			ginkgo.By("Creating big workload")
			gomega.Expect(k8sClient.Create(ctx, bigWl)).Should(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, bigWl)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)

			smallWl1 := testing.MakeWorkload("small-wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			smallWl2 := testing.MakeWorkload("small-wl-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "2.5").Obj()
			ginkgo.By("Creating two small workloads")
			gomega.Expect(k8sClient.Create(ctx, smallWl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, smallWl2)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBePending(ctx, k8sClient, smallWl1, smallWl2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 2)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)

			ginkgo.By("Marking the big workload as finished")
			util.FinishWorkloads(ctx, k8sClient, bigWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, smallWl1, smallWl2)
			util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 3)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 3)
		})

		ginkgo.It("Reclaimed resources are not accounted during admission", func() {
			wl := testing.MakeWorkload("first-wl", ns.Name).Queue(prodQueue.Name).
				PodSets(*testing.MakePodSet("main", 2).Request(corev1.ResourceCPU, "3").Obj()).
				Obj()
			ginkgo.By("Creating the workload", func() {
				gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 0)
			})
			ginkgo.By("Mark one pod as reclaimable", func() {
				gomega.Expect(workload.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{{Name: "main", Count: 1}})).To(gomega.Succeed())

				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wl)
				util.ExpectPendingWorkloadsMetric(prodClusterQ, 0, 0)
				util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 1)

				createWl := &kueue.Workload{}
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), createWl)).To(gomega.Succeed())
				gomega.Expect(*createWl.Status.Admission.PodSetAssignments[0].Count).To(gomega.Equal(int32(1)))
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same ClusterQueue", func() {
			ginkgo.By("First big workload starts")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectWl1Admission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "4").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectWl1Admission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Second big workload is pending")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "4").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Third small workload starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			expectWl3Admission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectWl3Admission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 2)

			ginkgo.By("Second big workload starts after the first one is deleted")
			gomega.Expect(k8sClient.Delete(ctx, wl1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			expectWl2Admission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "4").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectWl2Admission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 3)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 3)
		})

		ginkgo.It("Should re-enqueue by the delete event of workload belonging to the same Cohort", func() {
			fooCQ := testing.MakeClusterQueue("foo-clusterqueue").
				Cohort(cq.Spec.Cohort).
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, fooCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, fooCQ)).Should(gomega.Succeed())
			}()

			fooQ := testing.MakeLocalQueue("foo-queue", ns.Name).ClusterQueue(fooCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, fooQ)).Should(gomega.Succeed())

			ginkgo.By("First big workload starts")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(fooCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "8").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(fooCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(fooCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 1)

			ginkgo.By("Second big workload is pending")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "8").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("Third small workload starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "2").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			expectAdmission = testing.MakeAdmission(fooCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(fooCQ, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(fooCQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 2)

			ginkgo.By("Second big workload starts after the first one is deleted")
			gomega.Expect(k8sClient.Delete(ctx, wl1, client.PropagationPolicy(metav1.DeletePropagationBackground))).Should(gomega.Succeed())
			expectAdmission = testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "8").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})
		ginkgo.It("Should re-enqueue by the update event of ClusterQueue", func() {
			metrics.AdmissionAttemptsTotal.Reset()
			wl := testing.MakeWorkload("on-demand-wl", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "6").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)
			util.ExpectAdmissionAttemptsMetric(1, 0)

			ginkgo.By("updating ClusterQueue")
			updatedCq := &kueue.ClusterQueue{}

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), updatedCq)).Should(gomega.Succeed())
				updatedCq.Spec.Cohort = "cohort"
				updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[0] = kueue.ResourceQuota{
					Name:           corev1.ResourceCPU,
					NominalQuota:   resource.MustParse("6"),
					BorrowingLimit: ptr.To(resource.MustParse("0")),
				}
				g.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "6").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmissionAttemptsMetric(1, 1)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workloads from the selected namespaces", func() {
			ginkgo.By("checking the workloads don't get admitted at first")
			wl1 := testing.MakeWorkload("wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			wl2 := testing.MakeWorkload("wl2", nsFoo.Name).Queue(queueFoo.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 2)
			util.ExpectReservingActiveWorkloadsMetric(cq, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("checking the first workload gets admitted after updating the namespace labels to match CQ selector")
			ns.Labels = map[string]string{"dep": "eng"}
			gomega.Expect(k8sClient.Update(ctx, ns)).Should(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, fooCQ, true)
		})

		ginkgo.It("Should be inactive until the flavor is created", func() {
			ginkgo.By("Creating one workload")
			util.ExpectClusterQueueStatusMetric(fooCQ, metrics.CQStatusPending)
			wl := testing.MakeWorkload("workload", ns.Name).Queue(fooQ.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeFrozen(ctx, k8sClient, fooCQ.Name, wl)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(fooCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(fooCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(fooCQ, 0)

			ginkgo.By("Creating foo flavor")
			fooFlavor := testing.MakeResourceFlavor("foo-flavor").Obj()
			gomega.Expect(k8sClient.Create(ctx, fooFlavor)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, fooFlavor)).To(gomega.Succeed())
			}()
			util.ExpectClusterQueueStatusMetric(fooCQ, metrics.CQStatusActive)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, fooCQ.Name, wl)
			util.ExpectPendingWorkloadsMetric(fooCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(fooCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(fooCQ, 1)
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
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
		})

		ginkgo.It("Should schedule workloads on tolerated flavors", func() {
			ginkgo.By("checking a workload without toleration starts on the non-tainted flavor")
			wl1 := testing.MakeWorkload("on-demand-wl1", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())

			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("checking a second workload without toleration doesn't start")
			wl2 := testing.MakeWorkload("on-demand-wl2", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("checking a third workload with toleration starts")
			wl3 := testing.MakeWorkload("on-demand-wl3", ns.Name).Queue(queue.Name).Toleration(spotToleration).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

			expectAdmission = testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "spot-tainted", "5").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 2)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
		})

		ginkgo.It("Should admit workloads with affinity to specific flavor", func() {
			ginkgo.By("checking a workload without affinity gets admitted on the first flavor")
			wl1 := testing.MakeWorkload("no-affinity-workload", ns.Name).Queue(queue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "spot-untainted", "1").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, expectAdmission)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)

			ginkgo.By("checking a second workload with affinity to on-demand gets admitted")
			wl2 := testing.MakeWorkload("affinity-wl", ns.Name).Queue(queue.Name).
				NodeSelector(map[string]string{instanceKey: onDemandFlavor.Name, "foo": "bar"}).
				Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			gomega.Expect(wl2.Spec.PodSets[0].Template.Spec.NodeSelector).Should(gomega.HaveLen(2))
			expectAdmission = testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(cq, 2)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workload when creating ResourceFlavor->LocalQueue->Workload->ClusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ResourceFlavor->LocalQueue->ClusterQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ResourceFlavor->ClusterQueue->LocalQueue", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, w)
		})

		ginkgo.It("Should admit workload when creating Workload->ClusterQueue->LocalQueue->ResourceFlavor", func() {
			gomega.Expect(k8sClient.Create(ctx, w)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, w)
		})
	})

	ginkgo.When("Using cohorts for sharing unused resources", func() {
		var (
			prodCQ *kueue.ClusterQueue
			devCQ  *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotTaintedFlavor)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, spotUntaintedFlavor)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prodCQ, true)
			if devCQ != nil {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, devCQ, true)
			}
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotUntaintedFlavor, true)
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
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 0)

			ginkgo.By("checking the workload gets admitted when a fallback ClusterQueue gets added")
			fallbackClusterQueue := testing.MakeClusterQueue("fallback-cq").
				Cohort(prodCQ.Spec.Cohort).
				ResourceGroup(
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "5").Obj(), // prod-cq can't borrow this due to its borrowingLimit
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, fallbackClusterQueue)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, fallbackClusterQueue)).ToNot(gomega.HaveOccurred())
			}()

			expectAdmission := testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 1)
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

			devQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "11").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "11").Obj()

			ginkgo.By("Creating two workloads")
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1, wl2)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 0)
			util.ExpectReservingActiveWorkloadsMetric(devCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(devCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(devCQ, 0)

			// Delay cluster queue creation to make sure workloads are in the same
			// scheduling cycle.
			testCQ := testing.MakeClusterQueue("test-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "15", "0").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, testCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, testCQ)).Should(gomega.Succeed())
			}()

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, wl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devCQ.Name, wl2)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectReservingActiveWorkloadsMetric(devCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(devCQ, 1)
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
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, pWl1, pWl2)

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
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, pWl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, dWl1)

			ginkgo.By("Finishing third workload for prod ClusterQueue")
			util.FinishWorkloads(ctx, k8sClient, pWl3)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devCQ.Name, dWl1)
		})

		ginkgo.It("Should try next flavor if can't preempt on first", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				QueueingStrategy(kueue.StrictFIFO).
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj(),
					*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.Preempt,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			ginkgo.By("Creating 2 workloads and ensuring they are admitted")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1,
				testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj())
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2,
				testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "1").Obj())

			ginkgo.By("Creating an additional workload that can't fit in the first flavor")
			wl3 := testing.MakeWorkload("wl-3", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3,
				testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "spot-untainted", "1").Obj())
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 3)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 3)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 3)
		})

		ginkgo.It("Should try next flavor instead of borrowing", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10", "10").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor}).
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10", "10").Obj(),
					*testing.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "11").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).Should(gomega.Succeed())

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())

			ginkgo.By("Creating one workload")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "9").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			prodWl1Admission := testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "9").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl1, prodWl1Admission)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)

			ginkgo.By("Creating another workload")
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(devQueue.Name).Request(corev1.ResourceCPU, "11").Toleration(spotToleration).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			prodWl2Admission := testing.MakeAdmission(devCQ.Name).Assignment(corev1.ResourceCPU, "spot-tainted", "11").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl2, prodWl2Admission)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(devCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(devCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(devCQ, 1)
		})

		ginkgo.It("Should preempt before try next flavor", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10", "10").Obj(),
					*testing.MakeFlavorQuotas("spot-untainted").Resource(corev1.ResourceCPU, "11").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanPreempt: kueue.Preempt}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor}).
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10", "10").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).Should(gomega.Succeed())

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())

			ginkgo.By("Creating two workloads")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Priority(0).Queue(devQueue.Name).Request(corev1.ResourceCPU, "9").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Priority(1).Queue(devQueue.Name).Request(corev1.ResourceCPU, "9").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)

			ginkgo.By("Creating another workload")
			wl3 := testing.MakeWorkload("wl-3", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1)

			util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)

			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3,
				testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "5").Obj())
		})
	})

	ginkgo.When("Cohort provides resources directly", func() {
		var (
			cq     *kueue.ClusterQueue
			cohort *kueuealpha.Cohort
			wl     *kueue.Workload
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workload using resources borrowed from cohort", func() {
			cq = testing.MakeClusterQueue("clusterqueue").
				Cohort("cohort").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "0").Obj(),
				).Obj()
			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("workload not admitted when Cohort doesn't provide any resources")
			wl = testing.MakeWorkload("wl", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "10").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("workload not admitted when Cohort provides too few resources")
			cohort = testing.MakeCohort("cohort").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cohort)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("workload admitted when Cohort updated to provide sufficient resources")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), cohort)).Should(gomega.Succeed())
				cohort.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("10")
				g.Expect(k8sClient.Update(ctx, cohort)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
		})

		ginkgo.It("Should admit workload when cq switches into cohort with capacity", func() {
			cq = testing.MakeClusterQueue("clusterqueue").
				Cohort("impecunious-cohort").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "0").Obj(),
				).Obj()
			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("workload not admitted")
			wl = testing.MakeWorkload("wl", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "10").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			ginkgo.By("cohort created")
			cohort = testing.MakeCohort("cohort").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
			gomega.Expect(k8sClient.Create(ctx, cohort)).Should(gomega.Succeed())

			ginkgo.By("cq switches and workload admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())
				cq.Spec.Cohort = "cohort"
				g.Expect(k8sClient.Update(ctx, cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
		})
		ginkgo.It("Long distance resource allows workload to be scheduled", func() {
			cq = testing.MakeClusterQueue("cq").
				Cohort("left").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "0").Obj(),
				).Obj()
			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("cohorts created")
			cohortRight := testing.MakeCohort("left").Parent("root").Obj()
			cohortLeft := testing.MakeCohort("right").Parent("root").Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortLeft)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, cohortRight)).Should(gomega.Succeed())

			//         root
			//        /    \
			//      left    right
			//     /
			//    cq
			ginkgo.By("workload not admitted")
			wl = testing.MakeWorkload("wl", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "10").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 0)

			//         root
			//        /    \
			//      left    right
			//     /           \
			//    cq            bank
			ginkgo.By("cohort with resources created and workload admitted")
			cohortBank := testing.MakeCohort("bank").Parent("right").
				ResourceGroup(
					testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "10").FlavorQuotas,
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortBank)).Should(gomega.Succeed())
			expectAdmission := testing.MakeAdmission(cq.Name).Assignment(corev1.ResourceCPU, "on-demand", "10").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)
		})
	})

	ginkgo.When("Using cohorts for sharing with LendingLimit", func() {
		var (
			prodCQ *kueue.ClusterQueue
			devCQ  *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prodCQ, true)
			if devCQ != nil {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, devCQ, true)
			}
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should admit workloads using borrowed ClusterQueue", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "", "1").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			queue := testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).Should(gomega.Succeed())

			ginkgo.By("checking a no-fit workload does not get admitted")
			wl := testing.MakeWorkload("wl", ns.Name).Queue(queue.Name).
				Request(corev1.ResourceCPU, "9").Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 0)

			ginkgo.By("checking the workload gets admitted when another ClusterQueue gets added")
			devCQ := testing.MakeClusterQueue("dev-cq").
				Cohort(prodCQ.Spec.Cohort).
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "", "4").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, devCQ)).ToNot(gomega.HaveOccurred())
			}()

			expectAdmission := testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "on-demand", "9").Obj()
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl, expectAdmission)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
		})

		ginkgo.It("Should admit workloads after updating lending limit", func() {
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "", "0").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).Should(gomega.Succeed())

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5", "", "0").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).Should(gomega.Succeed())

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodQueue)).Should(gomega.Succeed())

			devQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devQueue)).Should(gomega.Succeed())

			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "5").Obj()

			ginkgo.By("Creating two workloads")
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, wl1)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 1)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 1)
			util.ExpectReservingActiveWorkloadsMetric(devCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 1)
			util.ExpectQuotaReservedWorkloadsTotalMetric(devCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(devCQ, 0)

			// Update lending limit of cluster queue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(devCQ), devCQ)).Should(gomega.Succeed())
				devCQ.Spec.ResourceGroups[0].Flavors[0].Resources[0] = kueue.ResourceQuota{
					Name:         corev1.ResourceCPU,
					NominalQuota: resource.MustParse("5"),
					LendingLimit: ptr.To(resource.MustParse("5")),
				}
				g.Expect(k8sClient.Update(ctx, devCQ)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, wl2)
			util.ExpectPendingWorkloadsMetric(prodCQ, 0, 0)
			util.ExpectPendingWorkloadsMetric(devCQ, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(prodCQ, 2)
			util.ExpectReservingActiveWorkloadsMetric(devCQ, 0)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodCQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(prodCQ, 2)
			util.ExpectQuotaReservedWorkloadsTotalMetric(devCQ, 0)
			util.ExpectAdmittedWorkloadsTotalMetric(devCQ, 0)
		})
	})

	ginkgo.When("Queueing with StrictFIFO", func() {
		var (
			strictFIFOClusterQ *kueue.ClusterQueue
			matchingNS         *corev1.Namespace
			chName             string
		)

		ginkgo.BeforeEach(func() {
			chName = "cohort"
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
				Cohort(chName).
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, strictFIFOClusterQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
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

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, strictFIFOClusterQ.Name, wl1)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			// wl3 doesn't even get a scheduling attempt, so can't check for conditions.
			gomega.Consistently(func(g gomega.Gomega) {
				lookupKey := types.NamespacedName{Name: wl3.Name, Namespace: wl3.Namespace}
				g.Expect(k8sClient.Get(ctx, lookupKey, wl3)).Should(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(wl3)).Should(gomega.BeFalse())
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(strictFIFOClusterQ, 2, 0)
			util.ExpectReservingActiveWorkloadsMetric(strictFIFOClusterQ, 1)
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

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, strictFIFOClusterQ.Name, wl1, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
			util.ExpectPendingWorkloadsMetric(strictFIFOClusterQ, 0, 1)
		})

		ginkgo.It("Pending workload with StrictFIFO doesn't block other CQ from borrowing from a third CQ", func() {
			ginkgo.By("Creating ClusterQueues and LocalQueues")
			cqTeamA := testing.MakeClusterQueue("team-a").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
				Cohort(chName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cqTeamA)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteNamespace(ctx, k8sClient, matchingNS)).To(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cqTeamA, true)
			}()

			strictFIFOLocalQueue := testing.MakeLocalQueue("strict-fifo-q", matchingNS.Name).ClusterQueue(strictFIFOClusterQ.Name).Obj()
			lqTeamA := testing.MakeLocalQueue("team-a-lq", matchingNS.Name).ClusterQueue(cqTeamA.Name).Obj()

			gomega.Expect(k8sClient.Create(ctx, strictFIFOLocalQueue)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, lqTeamA)).Should(gomega.Succeed())

			cqSharedResources := testing.MakeClusterQueue("shared-resources").
				ResourceGroup(
					*testing.MakeFlavorQuotas(onDemandFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
				Cohort(chName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cqSharedResources)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteNamespace(ctx, k8sClient, matchingNS)).To(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cqSharedResources, true)
			}()

			ginkgo.By("Creating workloads")
			admittedWl1 := testing.MakeWorkload("wl", matchingNS.Name).Queue(strictFIFOLocalQueue.
				Name).Request(corev1.ResourceCPU, "3").Priority(10).Obj()
			gomega.Expect(k8sClient.Create(ctx, admittedWl1)).Should(gomega.Succeed())

			admittedWl2 := testing.MakeWorkload("player1-a", matchingNS.Name).Queue(lqTeamA.
				Name).Request(corev1.ResourceCPU, "5").Priority(1).Obj()
			gomega.Expect(k8sClient.Create(ctx, admittedWl2)).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, admittedWl1, admittedWl2)
			gomega.Eventually(func(g gomega.Gomega) {
				var cq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqTeamA), &cq)).To(gomega.Succeed())
				cq.Spec.StopPolicy = ptr.To(kueue.Hold)
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// pendingWl exceed nominal+borrowing quota and cannot preempt as priority based
			// premption within CQ is disabled.
			pendingWl := testing.MakeWorkload("pending-wl", matchingNS.Name).Queue(strictFIFOLocalQueue.
				Name).Request(corev1.ResourceCPU, "3").Priority(99).Obj()
			gomega.Expect(k8sClient.Create(ctx, pendingWl)).Should(gomega.Succeed())

			// borrowingWL can borrow shared resources, so it should be scheduled even if workloads
			// in other cluster queues are waiting to reclaim nominal resources.
			borrowingWl := testing.MakeWorkload("player2-a", matchingNS.Name).Queue(lqTeamA.
				Name).Request(corev1.ResourceCPU, "5").Priority(11).Obj()
			gomega.Expect(k8sClient.Create(ctx, borrowingWl)).Should(gomega.Succeed())

			// blockedWL wants to borrow resources from strictFIFO CQ, but should be blocked
			// from borrowing because there is a pending workload in strictFIFO CQ.
			blockedWl := testing.MakeWorkload("player3-a", matchingNS.Name).Queue(lqTeamA.
				Name).Request(corev1.ResourceCPU, "1").Priority(10).Obj()
			gomega.Expect(k8sClient.Create(ctx, blockedWl)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var cq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqTeamA), &cq)).To(gomega.Succeed())
				cq.Spec.StopPolicy = ptr.To(kueue.None)
				g.Expect(k8sClient.Update(ctx, &cq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBePending(ctx, k8sClient, pendingWl, blockedWl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, borrowingWl)
		})
	})

	ginkgo.When("Deleting clusterQueues", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, false)
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
				gomega.Expect(util.DeleteObject(ctx, k8sClient, wl1)).To(gomega.Succeed())
			}()
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(cq, 1)

			ginkgo.By("Delete clusterQueue")
			gomega.Expect(util.DeleteObject(ctx, k8sClient, cq)).To(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				var newCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &newCQ)).To(gomega.Succeed())
				g.Expect(newCQ.GetFinalizers()).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())

			ginkgo.By("New created workloads should be frozen")
			wl2 := testing.MakeWorkload("workload2", ns.Name).Queue(queue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
			defer func() {
				gomega.Expect(util.DeleteObject(ctx, k8sClient, wl2)).To(gomega.Succeed())
			}()
			util.ExpectWorkloadsToBeFrozen(ctx, k8sClient, cq.Name, wl2)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)
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
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		type testParams struct {
			reqCPU           string
			limitCPU         string
			minCPU           string
			maxCPU           string
			limitType        corev1.LimitType
			wantedStatus     string
			shouldBeAdmitted bool
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

			if tp.shouldBeAdmitted {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)
			} else {
				gomega.Eventually(func(g gomega.Gomega) {
					rwl := kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &rwl)).Should(gomega.Succeed())
					cond := meta.FindStatusCondition(rwl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).ShouldNot(gomega.BeNil())
					g.Expect(cond.Message).Should(gomega.ContainSubstring(tp.wantedStatus))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}
			gomega.Expect(util.DeleteObject(ctx, k8sClient, wl)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, lr)).To(gomega.Succeed())
		},
			ginkgo.Entry("request more that limits", testParams{reqCPU: "3", limitCPU: "2", wantedStatus: "resource validation failed:"}),
			ginkgo.Entry("request over container limits", testParams{reqCPU: "2", limitCPU: "3", maxCPU: "1", wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("request under container limits", testParams{reqCPU: "2", limitCPU: "3", minCPU: "3", wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("request over pod limits", testParams{reqCPU: "2", limitCPU: "3", maxCPU: "1", limitType: corev1.LimitTypePod, wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("request under pod limits", testParams{reqCPU: "2", limitCPU: "3", minCPU: "3", limitType: corev1.LimitTypePod, wantedStatus: "didn't satisfy LimitRange constraints:"}),
			ginkgo.Entry("valid", testParams{reqCPU: "2", limitCPU: "3", minCPU: "1", maxCPU: "4", shouldBeAdmitted: true}),
		)
	})

	ginkgo.When("Using clusterQueue stop policy", func() {
		var (
			cq    *kueue.ClusterQueue
			queue *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
			queue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, queue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should evict workloads when stop policy is drain", func() {
			ginkgo.By("Creating first workload")
			wl1 := testing.MakeWorkload("one", ns.Name).Queue(queue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)

			ginkgo.By("Stopping the ClusterQueue")
			var clusterQueue kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &clusterQueue)).To(gomega.Succeed())
				clusterQueue.Spec.StopPolicy = ptr.To(kueue.HoldAndDrain)
				g.Expect(k8sClient.Update(ctx, &clusterQueue)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusPending)
			util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByClusterQueueStopped, 1)

			ginkgo.By("Checking the condition of workload is evicted", func() {
				createdWl := kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &createdWl)).To(gomega.Succeed())
					g.Expect(meta.FindStatusCondition(createdWl.Status.Conditions, kueue.WorkloadEvicted)).Should(
						gomega.BeComparableTo(&metav1.Condition{
							Type:    kueue.WorkloadEvicted,
							Status:  metav1.ConditionTrue,
							Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
							Message: "The ClusterQueue is stopped",
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
					)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)

			ginkgo.By("Creating another workload")
			wl2 := testing.MakeWorkload("two", ns.Name).Queue(queue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(cq, 0, 2)

			ginkgo.By("Restart the ClusterQueue by removing its stopPolicy")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &clusterQueue)).To(gomega.Succeed())
				clusterQueue.Spec.StopPolicy = nil
				g.Expect(k8sClient.Update(ctx, &clusterQueue)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive)

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 2)
			util.FinishWorkloads(ctx, k8sClient, wl1, wl2)
		})
	})

	ginkgo.When("Using localQueue stop policy", func() {
		var (
			cq *kueue.ClusterQueue
			lq *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())

			cq = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

			lq = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should evict workloads when stop policy is drain", func() {
			ginkgo.By("Creating first workload")
			wl1 := testing.MakeWorkload("wl1", ns.Name).Queue(lq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 1)

			createdLq := &kueue.LocalQueue{}

			ginkgo.By("Checking the condition of LocalQueue is active")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLq)).To(gomega.Succeed())
				g.Expect(createdLq.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(createdLq.Status.ReservingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(createdLq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(createdLq.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to clusterQueue",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Stopping the LocalQueue")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLq)).To(gomega.Succeed())
				createdLq.Spec.StopPolicy = ptr.To(kueue.HoldAndDrain)
				g.Expect(k8sClient.Update(ctx, createdLq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByLocalQueueStopped, 1)

			createdWl := kueue.Workload{}
			ginkgo.By("Checking the condition of workload is evicted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &createdWl)).To(gomega.Succeed())
				g.Expect(meta.FindStatusCondition(createdWl.Status.Conditions, kueue.WorkloadEvicted)).Should(gomega.BeComparableTo(
					&metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
						Message: "The LocalQueue is stopped",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Checking the condition of LocalQueue is inactive")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLq)).To(gomega.Succeed())
				g.Expect(createdLq.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(createdLq.Status.ReservingWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(createdLq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(1)))
				g.Expect(createdLq.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  core.StoppedReason,
						Message: "LocalQueue is stopped",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)

			ginkgo.By("Creating second workload")
			wl2 := testing.MakeWorkload("wl2", ns.Name).Queue(lq.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(cq, 0, 0)

			ginkgo.By("Restart the LocalQueue by removing its stopPolicy")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLq)).To(gomega.Succeed())
				createdLq.Spec.StopPolicy = nil
				g.Expect(k8sClient.Update(ctx, createdLq)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Checking the condition of LocalQueue is active")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), createdLq)).To(gomega.Succeed())
				g.Expect(createdLq.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				g.Expect(createdLq.Status.ReservingWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(createdLq.Status.AdmittedWorkloads).Should(gomega.Equal(int32(2)))
				g.Expect(createdLq.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.LocalQueueActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Ready",
						Message: "Can submit new workloads to clusterQueue",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
			util.ExpectPendingWorkloadsMetric(cq, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cq, 2)
			util.FinishWorkloads(ctx, k8sClient, wl1, wl2)
		})
	})

	ginkgo.When("Queueing with StrictFIFO", func() {
		var (
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cq").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*testing.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())

			localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("Should report pending workloads properly when blocked", func() {
			var wl1, wl2, wl3 *kueue.Workload
			ginkgo.By("Create two workloads", func() {
				wl1 = testing.MakeWorkload("wl1", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "2").Priority(100).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				wl2 = testing.MakeWorkload("wl2", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "5").Priority(10).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())

				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Create wl3 which is now also pending", func() {
				wl3 = testing.MakeWorkload("wl3", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "1").Priority(1).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				util.ExpectPendingWorkloadsMetric(clusterQueue, 2, 0)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(2)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Mark wl1 as finished which allows wl2 to be admitted", func() {
				util.FinishWorkloads(ctx, k8sClient, wl1)

				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Mark remaining workloads as finished", func() {
				util.FinishWorkloads(ctx, k8sClient, wl2, wl3)

				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
				util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(0)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should allow mutating the requeueingStrategy", func() {
			var wl1, wl2, wl3 *kueue.Workload
			ginkgo.By("Create initial set of workloads, verify counters", func() {
				wl1 = testing.MakeWorkload("wl1", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "2").Priority(100).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())
				wl2 = testing.MakeWorkload("wl2", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "5").Priority(10).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())
				wl3 = testing.MakeWorkload("wl3", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "1").Priority(1).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl3)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(2)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectPendingWorkloadsMetric(clusterQueue, 2, 0)
				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
			})

			ginkgo.By("Update the ClusterQueue to use the BestEffortFIFO strategy, verify the pending workload is counted as inadmissible", func() {
				updatedCq := &kueue.ClusterQueue{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterQueue.Name}, updatedCq)).Should(gomega.Succeed())
					updatedCq.Spec.QueueingStrategy = kueue.BestEffortFIFO
					g.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
			})

			ginkgo.By("Update the ClusterQueue to use the StrictFIFO strategy again, verify the pending workload is counted as active", func() {
				updatedCq := &kueue.ClusterQueue{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterQueue.Name}, updatedCq)).Should(gomega.Succeed())
					updatedCq.Spec.QueueingStrategy = kueue.StrictFIFO
					g.Expect(k8sClient.Update(ctx, updatedCq)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectPendingWorkloadsMetric(clusterQueue, 1, 0)
				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
			})

			var wl4 *kueue.Workload
			ginkgo.By("Creating workload wl4, verify the counter of pending workloads is incremented", func() {
				wl4 = testing.MakeWorkload("wl4", ns.Name).Queue(localQueue.
					Name).Request(corev1.ResourceCPU, "1").Priority(1).Obj()
				gomega.Expect(k8sClient.Create(ctx, wl4)).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl3)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.PendingWorkloads).Should(gomega.Equal(int32(2)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectPendingWorkloadsMetric(clusterQueue, 2, 0)
				util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
			})

			ginkgo.By("Mark all workloads as finished", func() {
				util.FinishWorkloads(ctx, k8sClient, wl1, wl2, wl3, wl4)
			})
		})
	})

	ginkgo.When("Deleting resources from borrowing clusterQueue", func() {
		var (
			cq1 *kueue.ClusterQueue
			cq2 *kueue.ClusterQueue
			lq1 *kueue.LocalQueue
			lq2 *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(k8sClient.Create(ctx, onDemandFlavor)).To(gomega.Succeed())

			cq1 = testing.MakeClusterQueue("cq1").
				Cohort("cohort").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq1)).Should(gomega.Succeed())

			cq2 = testing.MakeClusterQueue("cq2").
				Cohort("cohort").
				ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cq2)).Should(gomega.Succeed())

			lq1 = testing.MakeLocalQueue("lq1", ns.Name).ClusterQueue(cq1.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq1)).Should(gomega.Succeed())

			lq2 = testing.MakeLocalQueue("lq2", ns.Name).ClusterQueue(cq2.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lq2)).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		})

		ginkgo.It("shouldn't admit second workload on over admission", func() {
			ginkgo.By("creating first workload")
			wl1 := testing.MakeWorkload("wl1", ns.Name).
				Queue(lq1.Name).
				Request(corev1.ResourceCPU, "2").
				Request(corev1.ResourceMemory, "2Gi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).Should(gomega.Succeed())

			ginkgo.By("creating second workload")
			wl2 := testing.MakeWorkload("wl2", ns.Name).
				Queue(lq2.Name).
				Request(corev1.ResourceCPU, "1").
				Request(corev1.ResourceMemory, "1Gi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).Should(gomega.Succeed())

			ginkgo.By("checking the first workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).Should(gomega.Succeed())
				g.Expect(wl1.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadQuotaReserved,
						Message: fmt.Sprintf("Quota reserved in ClusterQueue %s", cq1.Name),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadAdmitted,
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))

				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).Should(gomega.Succeed())
				g.Expect(wl2.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "couldn't assign flavors to pod set main: insufficient unused quota for memory in flavor on-demand, 1Gi more needed",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("removing memory resources from first cluster queue")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq1), cq1)).Should(gomega.Succeed())
				cq1.Spec.ResourceGroups = nil
				cq1Wrapper := &testing.ClusterQueueWrapper{ClusterQueue: *cq1}
				cq1Wrapper.ResourceGroup(*testing.MakeFlavorQuotas(onDemandFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Obj(),
				)
				cq1 = cq1Wrapper.Obj()
				g.Expect(k8sClient.Update(ctx, cq1)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the second workload is not admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).Should(gomega.Succeed())
				g.Expect(wl1.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadQuotaReserved,
						Message: fmt.Sprintf("Quota reserved in ClusterQueue %s", cq1.Name),
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadAdmitted,
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))

				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).Should(gomega.Succeed())
				g.Expect(wl2.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "couldn't assign flavors to pod set main: insufficient unused quota for memory in flavor on-demand, 1Gi more needed",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
