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
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	pkgconstants "sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// mintPod simulates a completion index; draining removes the highest indexes first.
func mintPod(ctx context.Context, namespace, podNamePrefix, workloadName string, podSet kueue.PodSetReference, index int32) {
	ginkgo.GinkgoHelper()
	pod := testingpod.MakePod(fmt.Sprintf("%s-%d", podNamePrefix, index), namespace).
		Annotation(kueue.WorkloadAnnotation, workloadName).
		Annotation(kueue.WorkloadSliceNameAnnotation, workloadName).
		Annotation(batchv1.JobCompletionIndexAnnotation, strconv.Itoa(int(index))).
		Label(pkgconstants.PodSetLabel, string(podSet)).
		Label(batchv1.JobCompletionIndexAnnotation, strconv.Itoa(int(index))).
		Gate(kueue.ElasticJobSchedulingGate).
		Obj()
	behavioral.MustCreate(ctx, k8sClient, pod)
}

func expectPodCounts(ctx context.Context, namespace, podNamePrefix string, podSet kueue.PodSetReference, wantUngated, wantGated int) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(namespace),
			client.MatchingLabels{pkgconstants.PodSetLabel: string(podSet)})).Should(gomega.Succeed())
		ungated, gated := 0, 0
		for i := range pods.Items {
			if !strings.HasPrefix(pods.Items[i].Name, podNamePrefix+"-") {
				continue
			}
			if utilpod.HasGate(&pods.Items[i], kueue.ElasticJobSchedulingGate) {
				gated++
			} else {
				ungated++
			}
		}
		g.Expect(ungated).Should(gomega.Equal(wantUngated))
		g.Expect(gated).Should(gomega.Equal(wantGated))
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

// This exercises the full reactive partial-preemption loop for batch/Job, end to end:
//
//	request  - the scheduler sets PreemptionTargetCount when the preemptor arrives,
//	drain    - the Job reconciler evicts the victim's excess Pods down to the target,
//	release  - the ungater lowers the admitted count and flips PartiallyPreempted to QuotaReleased,
//	admit    - the freed quota admits the preemptor,
//	re-grow  - once the preemptor leaves, the victim scales back up to its original parallelism.
var _ = ginkgo.Describe("Job partial preemption", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ReclaimablePods, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PartialPreemption, true)
		fwk.StartManager(ctx, cfg, managerAndControllersSetup(false, true, nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-partial-preemption-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("job-partial-preemption-cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "4").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("job-partial-preemption-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.It("shrinks the victim to admit a preemptor, then re-grows it once the preemptor leaves", framework.SlowSpec, func() {
		const parallelism int32 = 4
		// The preemptor needs 2 CPU from the 4 CPU ClusterQueue the victim fully occupies, so the
		// reclaim target is deterministic: parallelism (4) minus the preemptor's 2 CPU = 2.
		const target int32 = 2

		victim := testingjob.MakeJob("victim", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(pkgconstants.PartialPreemptionAnnotation, "true").
			SetAnnotation(pkgconstants.PodSetPartialPreemptionMinCountAnnotation, "1").
			Indexed(true).
			Request(corev1.ResourceCPU, "1").
			Parallelism(parallelism).
			Completions(parallelism).
			Obj()
		victim.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{
			Rules: []batchv1.PodFailurePolicyRule{{
				Action: batchv1.PodFailurePolicyActionIgnore,
				OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{
					Type:   corev1.DisruptionTarget,
					Status: corev1.ConditionTrue,
				}},
			}},
		}

		var (
			victimWlKey client.ObjectKey
			podSet      kueue.PodSetReference
		)
		ginkgo.By("admitting the victim that occupies the whole ClusterQueue", func() {
			behavioral.MustCreate(ctx, k8sClient, victim)
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				g.Expect(workloads.Items).Should(gomega.HaveLen(1))
				wl := &workloads.Items[0]
				g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(parallelism)))
				victimWlKey = client.ObjectKeyFromObject(wl)
				podSet = wl.Spec.PodSets[0].Name
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("simulating the Job's running Pods and letting the ungater ungate them", func() {
			for i := range parallelism {
				mintPod(ctx, ns.Name, "victim", victimWlKey.Name, podSet, i)
			}
			expectPodCounts(ctx, ns.Name, "victim", podSet, int(parallelism), 0)
		})

		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Priority(10).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 2).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()

		ginkgo.By("requesting a partial preemption when the preemptor arrives", func() {
			behavioral.MustCreate(ctx, k8sClient, preemptor)
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, victimWlKey, wl)).Should(gomega.Succeed())
				got, ok := workload.PreemptionTargetCount(wl, podSet)
				g.Expect(ok).Should(gomega.BeTrue())
				g.Expect(got).Should(gomega.Equal(target))
				cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPartiallyPreempted)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionTrue))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("draining the victim's excess Pods down to the target", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
					client.MatchingLabels{pkgconstants.PodSetLabel: string(podSet)})).Should(gomega.Succeed())
				g.Expect(pods.Items).Should(gomega.HaveLen(int(target)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("releasing the quota by lowering the admitted count once drained", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, victimWlKey, wl)).Should(gomega.Succeed())
				cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPartiallyPreempted)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).Should(gomega.Equal(metav1.ConditionFalse))
				g.Expect(cond.Reason).Should(gomega.Equal(kueue.WorkloadPartiallyPreemptedReasonQuotaReleased))
				// Reactive downscaling releases quota by lowering the admitted count to the target
				// and clearing the reclaim target (not by reporting reclaimablePods).
				g.Expect(wl.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(target)))
				_, hasTarget := workload.PreemptionTargetCount(wl, podSet)
				g.Expect(hasTarget).Should(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("admitting the preemptor with the released quota", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})

		ginkgo.By("keeping the re-created Pods gated while the preemptor still holds the quota", func() {
			// The drain deleted the highest indexes (target..parallelism-1). Re-create them: with the
			// admitted count still at the target, the ungater caps ungating at the granted count, so
			// the re-created Pods stay gated until the slice actually re-grows.
			for i := target; i < parallelism; i++ {
				mintPod(ctx, ns.Name, "victim", victimWlKey.Name, podSet, i)
			}
			// The low indexes keep running; the re-created high indexes stay gated.
			expectPodCounts(ctx, ns.Name, "victim", podSet, int(target), int(parallelism-target))
		})

		ginkgo.By("re-growing the victim back to its original parallelism once the preemptor leaves", func() {
			// Deleting the preemptor returns its quota to the ClusterQueue. The Job reconciler sees
			// spec.parallelism (4) above the admitted count (target) and creates a scale-up Workload
			// slice, which the scheduler admits against the freed quota, replacing the shrunk slice.
			gomega.Expect(k8sClient.Delete(ctx, preemptor)).Should(gomega.Succeed())

			// The scale-up slice replaces the shrunk one: the old slice is left Finished (and lingers
			// briefly), so select the victim's live admitted slice by skipping Finished Workloads.
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
				var admitted *kueue.Workload
				for i := range workloads.Items {
					wl := &workloads.Items[i]
					if metav1.IsControlledBy(wl, victim) && workload.IsAdmitted(wl) && !workloadfinish.IsFinished(wl) {
						admitted = wl
					}
				}
				g.Expect(admitted).ShouldNot(gomega.BeNil())
				g.Expect(admitted.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(admitted.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(parallelism)))
				g.Expect(workload.ExtractGrantedPodSetCounts(admitted)[podSet]).Should(gomega.Equal(parallelism))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())

			expectPodCounts(ctx, ns.Name, "victim", podSet, int(parallelism), 0)
		})
	})

	// This reproduces the band-wise over-shedding scenario from a real cluster (KEP-975 Case 1,
	// step 2), reusing the exact same environment and reactive steps as the single-victim test
	// above, only with two equal-priority elastic victims instead of one.
	//
	// low-01 and low-02 (parallelism 2, minCount 1, created in that order) fill the 4 CPU
	// ClusterQueue. A higher-priority preemptor needs 3 CPU. Band-wise reclaim must:
	//
	//	shrink both low victims to their floor (low-01 2->1, low-02 2->1) which frees 2, still 1
	//	short, so the newest victim is evicted whole (low-02 1->0) which frees the last 1.
	//
	// Expected end state: preemptor admitted, low-01 running at 1 (its floor), low-02 fully
	// evicted. The bug evicts BOTH low victims instead.
	ginkgo.It("reclaims band-wise: shrinks both low victims to the floor, then evicts only the newest", framework.SlowSpec, func() {
		const parallelism int32 = 2
		const minCount int32 = 1
		const floor int32 = 1

		makeLowJob := func(name string) *batchv1.Job {
			job := testingjob.MakeJob(name, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				SetAnnotation(pkgconstants.PartialPreemptionAnnotation, "true").
				SetAnnotation(pkgconstants.PodSetPartialPreemptionMinCountAnnotation, strconv.Itoa(int(minCount))).
				Indexed(true).
				Request(corev1.ResourceCPU, "1").
				Parallelism(parallelism).
				Completions(parallelism).
				Obj()
			job.Spec.PodFailurePolicy = &batchv1.PodFailurePolicy{
				Rules: []batchv1.PodFailurePolicyRule{{
					Action: batchv1.PodFailurePolicyActionIgnore,
					OnPodConditions: []batchv1.PodFailurePolicyOnPodConditionsPattern{{
						Type:   corev1.DisruptionTarget,
						Status: corev1.ConditionTrue,
					}},
				}},
			}
			return job
		}

		// liveAdmittedSlice returns the job's current (non-finished) admitted Workload slice.
		liveAdmittedSlice := func(g gomega.Gomega, jobName string) *kueue.Workload {
			workloads := &kueue.WorkloadList{}
			g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).Should(gomega.Succeed())
			var live *kueue.Workload
			for i := range workloads.Items {
				wl := &workloads.Items[i]
				if len(wl.OwnerReferences) == 0 || wl.OwnerReferences[0].Name != jobName {
					continue
				}
				if workload.IsAdmitted(wl) && !workloadfinish.IsFinished(wl) {
					live = wl
				}
			}
			return live
		}

		admittedSliceKey := func(jobName string) (client.ObjectKey, kueue.PodSetReference) {
			ginkgo.GinkgoHelper()
			var key client.ObjectKey
			var podSet kueue.PodSetReference
			gomega.Eventually(func(g gomega.Gomega) {
				wl := liveAdmittedSlice(g, jobName)
				g.Expect(wl).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(wl.Status.Admission.PodSetAssignments[0].Count).Should(gomega.HaveValue(gomega.Equal(parallelism)))
				key = client.ObjectKeyFromObject(wl)
				podSet = wl.Spec.PodSets[0].Name
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			return key, podSet
		}

		var (
			low01Key, low02Key       client.ObjectKey
			low01PodSet, low02PodSet kueue.PodSetReference
		)

		ginkgo.By("admitting low-01 at full parallelism and letting the ungater ungate its Pods", func() {
			behavioral.MustCreate(ctx, k8sClient, makeLowJob("low-01"))
			low01Key, low01PodSet = admittedSliceKey("low-01")
			for i := range parallelism {
				mintPod(ctx, ns.Name, "low-01", low01Key.Name, low01PodSet, i)
			}
			expectPodCounts(ctx, ns.Name, "low-01", low01PodSet, int(parallelism), 0)
		})

		ginkgo.By("admitting low-02 (newer) at full parallelism, filling the ClusterQueue", func() {
			behavioral.MustCreate(ctx, k8sClient, makeLowJob("low-02"))
			low02Key, low02PodSet = admittedSliceKey("low-02")
			for i := range parallelism {
				mintPod(ctx, ns.Name, "low-02", low02Key.Name, low02PodSet, i)
			}
			expectPodCounts(ctx, ns.Name, "low-02", low02PodSet, int(parallelism), 0)
		})

		preemptor := utiltestingapi.MakeWorkload("preemptor", ns.Name).
			Priority(10).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 3).Request(corev1.ResourceCPU, "1").Obj()).
			Obj()

		ginkgo.By("requesting reclaim when the preemptor arrives", func() {
			behavioral.MustCreate(ctx, k8sClient, preemptor)
			// Wait for the preemptor to be admitted; band-wise correctness is checked below.
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
		})

		ginkgo.By("keeping low-01 running at its floor and never fully evicting it", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				wl := liveAdmittedSlice(g, "low-01")
				g.Expect(wl).ShouldNot(gomega.BeNil())
				g.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
				g.Expect(workload.ExtractGrantedPodSetCounts(wl)[low01PodSet]).Should(gomega.BeEquivalentTo(floor))
				job := &batchv1.Job{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "low-01"}, job)).Should(gomega.Succeed())
				g.Expect(job.Spec.Suspend).Should(gomega.HaveValue(gomega.BeFalse()))
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("fully evicting only the newest victim (low-02)", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				job := &batchv1.Job{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "low-02"}, job)).Should(gomega.Succeed())
				g.Expect(job.Spec.Suspend).Should(gomega.HaveValue(gomega.BeTrue()))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
