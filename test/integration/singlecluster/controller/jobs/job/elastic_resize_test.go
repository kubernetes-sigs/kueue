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

package job

import (
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	pkgconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Job with elastic jobs via workload-resize support", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadResize, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadResizeWithPartialReplicaScaleUp, true)
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-resize-")

		resourceFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		// Start with room for two pods; the scale-up delta is admitted only
		// after the quota is expanded below.
		clusterQueue = utiltestingapi.MakeClusterQueue("default").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
	})

	ginkgo.It("Should keep scale-up pods gated until the delta quota is admitted", framework.SlowSpec, func() {
		testJob := testingjob.MakeJob("resize-job", ns.Name).
			SetAnnotation(pkgconstants.ResizeElasticJobAnnotation, "true").
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "1").
			Parallelism(2).
			Completions(4).
			Obj()

		var (
			wl     *kueue.Workload
			podSet kueue.PodSetReference
		)

		ginkgo.By("creating the resize job and admitting its workload at two pods", func() {
			behavioral.MustCreate(ctx, k8sClient, testJob)
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(workloads.Items).Should(gomega.HaveLen(1))
				wl = &workloads.Items[0]
				g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
				g.Expect(wl.Spec.PodSets[0].Count).Should(gomega.BeEquivalentTo(int32(2)))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(int32(2))))
				podSet = wl.Spec.PodSets[0].Name
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		// Resize mutates the single admitted Workload in place: no slice annotation,
		// so the pod only carries the workload-name annotation that the shared
		// ungater falls back to.
		mintGatedPod := func(name string) {
			ginkgo.GinkgoHelper()
			pod := testingpod.MakePod(name, ns.Name).
				Annotation(kueue.WorkloadAnnotation, wl.Name).
				Label(pkgconstants.PodSetLabel, string(podSet)).
				Gate(kueue.ElasticJobSchedulingGate).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, pod)
		}

		ginkgo.By("ungating the two pods that fit under the admitted count", func() {
			mintGatedPod("pod-0")
			mintGatedPod("pod-1")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(sets.List(ungatedPodNames(g, ns.Name, kueue.ElasticJobSchedulingGate))).Should(gomega.HaveLen(2))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("scaling the job up to four pods", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(testJob), testJob)).Should(gomega.Succeed())
				testJob.Spec.Parallelism = new(int32(4))
				g.Expect(k8sClient.Update(ctx, testJob)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the workload spec count grows to four while the admission stays at two", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets[0].Count).Should(gomega.BeEquivalentTo(int32(4)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(int32(2))))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("the two scale-up pods stay gated while only two replicas are admitted", func() {
			mintGatedPod("pod-2")
			mintGatedPod("pod-3")
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(sets.List(ungatedPodNames(g, ns.Name, kueue.ElasticJobSchedulingGate))).Should(gomega.HaveLen(2))
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("expanding the ClusterQueue quota to four CPU", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				cq := &kueue.ClusterQueue{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), cq)).Should(gomega.Succeed())
				cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("4")
				g.Expect(k8sClient.Update(ctx, cq)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the delta is admitted, the count becomes four and all four pods are ungated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(int32(4))))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(sets.List(ungatedPodNames(g, ns.Name, kueue.ElasticJobSchedulingGate))).Should(gomega.HaveLen(4))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})

// These tests exercise the scheduler's in-place elastic resize quota accounting
// (ElasticJobsViaWorkloadResize) directly at the Workload level. The runtime
// component (e.g. a Spark driver, or batch/Job's reconcileWorkloadResize) is
// simulated by editing spec.podSets[].count on the admitted Workload; the
// scheduler then admits the delta on the same Workload or releases quota,
// without ever exceeding the ClusterQueue nominal quota. Pods and scheduling
// gates are intentionally out of scope here — the Job-level Describe above
// covers the gating end to end.
var _ = ginkgo.Describe("Scheduler in-place elastic resize", func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	// setResizeCount updates spec.podSets[0].count of the given workload, retrying on conflict.
	setResizeCount := func(wl *kueue.Workload, count int32) {
		gomega.Eventually(func(g gomega.Gomega) {
			updated := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updated)).Should(gomega.Succeed())
			updated.Spec.PodSets[0].Count = count
			g.Expect(k8sClient.Update(ctx, updated)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// expectCQUsage asserts the (single) flavor/resource usage of the ClusterQueue equals want.
	expectCQUsage := func(want string) {
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			got := &kueue.ClusterQueue{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), got)).Should(gomega.Succeed())
			g.Expect(got.Status.FlavorsUsage).Should(gomega.HaveLen(1))
			g.Expect(got.Status.FlavorsUsage[0].Resources).Should(gomega.HaveLen(1))
			g.Expect(got.Status.FlavorsUsage[0].Resources[0].Total).Should(gomega.BeEquivalentTo(resource.MustParse(want)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	// expectAdmittedCount asserts the admitted count recorded in status.admission for podSet[0].
	expectAdmittedCount := func(wl *kueue.Workload, want int32) {
		gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
			g.Expect(got.Status.Admission).ShouldNot(gomega.BeNil())
			g.Expect(got.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
			g.Expect(ptr.Deref(got.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(want))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	elasticWorkload := func(name string, count int32) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, ns.Name).
			Annotation(pkgconstants.ResizeElasticJobAnnotation, "true").
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", int(count)).Request(corev1.ResourceCPU, "1").Obj())
	}

	ginkgo.BeforeEach(func() {
		// ElasticJobsViaWorkloadResize uses its own resize-elastic annotation, orthogonal to the
		// slicing annotation, so slicing does not need to be disabled here.
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadResize, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadResizeWithPartialReplicaScaleUp, true)
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "resize-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("resize-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("resize-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("should admit the delta in place on scale-up and account quota exactly", func() {
		ginkgo.By("Creating and admitting an elastic workload at count 2 (usage 2 CPU)")
		wl := elasticWorkload("resize", 2).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		expectAdmittedCount(wl, 2)
		expectCQUsage("2")

		ginkgo.By("Scaling up in place from 2 to 6 by editing spec.podSets[0].count")
		setResizeCount(wl, 6)

		ginkgo.By("Kueue admits the delta on the same workload: admitted count 6, usage exactly 6 CPU")
		expectAdmittedCount(wl, 6)
		expectCQUsage("6")

		// Apply the resize cases cumulatively so each case starts from the state left by the
		// previous one. The chain walks one workload through the interesting transitions:
		//   6 -> 10 -> 8  (retarget down while a scale-up is queued)
		//   8 -> 5 -> 10  (scale down, then back up to admission)
		//   10 -> 20 -> 5 (switch from scale-up to scale-down)
		//   5 -> 10       (scale up to the full quota)
		rapidResizeCases := []struct {
			name    string
			targets []int32
			want    int32
		}{
			{name: "6 -> 10 -> 8 keeps the latest scale-up target", targets: []int32{10, 8}, want: 8},
			{name: "8 -> 5 -> 10 reaches admission", targets: []int32{5, 10}, want: 10},
			{name: "10 -> 20 -> 5 switches from scale-up to scale-down", targets: []int32{20, 5}, want: 5},
			{name: "5 -> 10 scales up to the full quota", targets: []int32{10}, want: 10},
		}
		for _, tc := range rapidResizeCases {
			ginkgo.By(tc.name)
			for _, target := range tc.targets {
				setResizeCount(wl, target)
			}
			expectAdmittedCount(wl, tc.want)
			expectCQUsage(fmt.Sprintf("%d", tc.want))
		}

		ginkgo.By("A competing workload that would exceed remaining quota stays pending (10+5 > 10)")
		competitor := utiltestingapi.MakeWorkload("competitor", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 5).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, competitor)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, competitor)
		expectCQUsage("10")

		ginkgo.By("Scaling the resize workload from 10 to 12 must stay quota-checked, not over-committed")
		setResizeCount(wl, 12)
		gomega.Consistently(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
			g.Expect(ptr.Deref(got.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(10)))
		}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		expectCQUsage("10")
	})

	ginkgo.It("should admit the delta only for the scaled PodSet in a multi-PodSet workload", func() {
		ginkgo.By("Admitting a 2-PodSet workload (driver: 1, executor: 2 -> 3 CPU)")
		wl := utiltestingapi.MakeWorkload("resize", ns.Name).
			Annotation(pkgconstants.ResizeElasticJobAnnotation, "true").
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(
				*utiltestingapi.MakePodSet("driver", 1).Request(corev1.ResourceCPU, "1").Obj(),
				*utiltestingapi.MakePodSet("executor", 2).Request(corev1.ResourceCPU, "1").Obj(),
			).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		expectCQUsage("3")

		ginkgo.By("Scaling only the executor PodSet from 2 to 6")
		gomega.Eventually(func(g gomega.Gomega) {
			updated := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updated)).Should(gomega.Succeed())
			for i := range updated.Spec.PodSets {
				if updated.Spec.PodSets[i].Name == "executor" {
					updated.Spec.PodSets[i].Count = 6
				}
			}
			g.Expect(k8sClient.Update(ctx, updated)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("Driver stays at 1, executor grows to 6, usage 7")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
			counts := map[kueue.PodSetReference]int32{}
			for _, psa := range got.Status.Admission.PodSetAssignments {
				counts[psa.Name] = ptr.Deref(psa.Count, -1)
			}
			g.Expect(counts["driver"]).Should(gomega.BeEquivalentTo(int32(1)))
			g.Expect(counts["executor"]).Should(gomega.BeEquivalentTo(int32(6)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectCQUsage("7")
	})

	ginkgo.It("should release quota on in-place scale-down", func() {
		ginkgo.By("Creating and admitting an elastic workload at count 6 (usage 6 CPU)")
		wl := elasticWorkload("resize", 6).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		expectCQUsage("6")

		ginkgo.By("Scaling down in place from 6 to 3 by editing spec.podSets[0].count")
		setResizeCount(wl, 3)

		ginkgo.By("Usage drops to 3 CPU (U = min(spec, admitted))")
		expectCQUsage("3")

		ginkgo.By("The admitted counts converge to spec and the ScaleDown condition is set")
		gomega.Eventually(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(ptr.Deref(got.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(3)))
			g.Expect(apimeta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadScaleDown)).Should(gomega.BeTrue())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("The released quota becomes usable: a 7-CPU workload is admitted (3+7 = 10)")
		other := utiltestingapi.MakeWorkload("other", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 7).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, other)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other)
		expectCQUsage("10")
	})

	ginkgo.It("should not partially admit a scale-up without a user-set minCount (all-or-nothing)", func() {
		ginkgo.By("Admitting an elastic workload (no minCount) at count 2 and a blocker at count 5 (usage 7)")
		wl := elasticWorkload("resize", 2).Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		blocker := utiltestingapi.MakeWorkload("blocker", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 5).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, blocker)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl, blocker)
		expectCQUsage("7")

		ginkgo.By("Scaling up from 2 to 8 with only 3 CPU free: without minCount there is no partial admission")
		setResizeCount(wl, 8)
		gomega.Consistently(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
			g.Expect(ptr.Deref(got.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(2)))
		}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		expectCQUsage("7")

		ginkgo.By("Deleting the blocker frees enough quota for the full target: resize jumps to 8 (usage 8)")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, blocker, true)
		expectAdmittedCount(wl, 8)
		expectCQUsage("8")
	})

	ginkgo.It("should partially admit a scale-up and keep admitting it as quota is released", func() {
		ginkgo.By("Admitting an elastic (partial-eligible) workload at count 2 and a blocker at count 5 (usage 7)")
		wl := utiltestingapi.MakeWorkload("resize", ns.Name).
			Annotation(pkgconstants.ResizeElasticJobAnnotation, "true").
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 2).SetMinimumCount(1).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		blocker := utiltestingapi.MakeWorkload("blocker", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 5).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, blocker)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl, blocker)

		ginkgo.By("Scaling up from 2 to 8 with only 3 CPU free: partial admit to 5")
		setResizeCount(wl, 8)
		expectAdmittedCount(wl, 5)
		expectCQUsage("10")

		ginkgo.By("Retargeting the partially-satisfied workload to 10 before the blocker is removed")
		setResizeCount(wl, 10)
		gomega.Consistently(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
			g.Expect(ptr.Deref(got.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(5)))
		}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		expectCQUsage("10")

		ginkgo.By("Releasing quota by deleting the blocker (frees 5 CPU)")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, blocker, true)

		ginkgo.By("The still-under-target resize workload resumes and reaches its full target of 10 (usage 10)")
		expectAdmittedCount(wl, 10)
		expectCQUsage("10")
	})

	ginkgo.It("should partially admit a scale-up up to the ClusterQueue cap when the target is unreachable", func() {
		ginkgo.By("Admitting an elastic (partial-eligible) workload at count 2 (usage 2)")
		wl := utiltestingapi.MakeWorkload("resize", ns.Name).
			Annotation(pkgconstants.ResizeElasticJobAnnotation, "true").
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 2).SetMinimumCount(1).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		expectAdmittedCount(wl, 2)
		expectCQUsage("2")

		ginkgo.By("Scaling up from 2 to 12, beyond the 10-CPU ClusterQueue cap: partial admit fills the cap at 10")
		setResizeCount(wl, 12)
		expectAdmittedCount(wl, 10)
		expectCQUsage("10")

		ginkgo.By("The admitted count stays at the cap of 10: it neither regresses to the floor nor over-commits")
		gomega.Consistently(func(g gomega.Gomega) {
			got := &kueue.Workload{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).Should(gomega.Succeed())
			g.Expect(workload.IsAdmitted(got)).Should(gomega.BeTrue())
			g.Expect(ptr.Deref(got.Status.Admission.PodSetAssignments[0].Count, -1)).Should(gomega.BeEquivalentTo(int32(10)))
		}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		expectCQUsage("10")
	})
})
