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

package baseline

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	jobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("Configurable Preemption", ginkgo.Label("feature:configurablepreemption", e2e.Shard1), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns               *corev1.Namespace
		rf               *kueue.ResourceFlavor
		cq               *kueue.ClusterQueue
		lq               *kueue.LocalQueue
		preemptionConfig *kueuealpha.PreemptionConfig
	)

	priorityLabel := "test-priority-label"

	ginkgo.BeforeAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{
				string(features.ConfigurablePreemptions):      true,
				string(features.ConfigurablePreemptionLimits): true,
				string(features.TopologyAwareScheduling):      true,
				string(features.PrioritizePreemptorWorkloads): true,
			}
			cfg.Integrations = &configapi.Integrations{
				LabelKeysToCopy: []string{priorityLabel},
			}
		})
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")

		preemptionConfig = utiltestingalpha.MakePreemptionConfig("preemption-config-"+ns.Name).
			Rule("preempt-within-cq-lower-priority", kueuealpha.Always,
				kueuealpha.PreemptionConfigPreemptionCandidateSelector{
					Scope: kueuealpha.WithinClusterQueue,
					NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
						{
							Key:           priorityLabel,
							FallbackValue: new(int32(0)),
							Comparison:    new(kueuealpha.LessThan),
						},
					},
				},
			).Obj()
		behavioral.MustCreate(ctx, k8sClient, preemptionConfig)

		rf = utiltestingapi.MakeResourceFlavor("rf-" + ns.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, rf)

		cohort := kueue.CohortReference("cohort-" + ns.Name)

		cq = utiltestingapi.MakeClusterQueue("cq-"+ns.Name).
			Cohort(cohort).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").
				Obj()).
			Annotation(kueuealpha.PreemptionConfigNameAnnotation, preemptionConfig.Name).
			// Disable basic/previous preemption mechanism to ensure testing of PreemptionConfig.
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyNever,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy: kueue.BorrowWithinCohortPolicyNever,
				},
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, preemptionConfig, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Configurable preemption enabled", func() {
		ginkgo.It("Should preempt in the same LQ with lower priority", func() {
			ginkgo.By("Create jobs for admission")
			lowPriorityJob := jobtesting.MakeJob("low-priority-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "1").
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, lowPriorityJob)

			highPriorityJob := jobtesting.MakeJob("high-priority-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "9").
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, highPriorityJob)

			ginkgo.By("Waiting for workloads to be admitted")
			behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(lowPriorityJob))
			behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob))

			ginkgo.By("Create preempting job")
			preemptingJob := jobtesting.MakeJob("preempting-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "5").
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, preemptingJob)

			ginkgo.By("Verify preemption")
			behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(preemptingJob))
			behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob))
			e2e.ExpectJobToBePreempted(ctx, k8sClient, lowPriorityJob, kueue.ConfigurablePreemptionReason)
		})
	})

	ginkgo.When("PreemptionLimits are configured", ginkgo.Label("feature:configurablepreemptionlimits"), func() {
		var preemptionLimit *kueuealpha.PreemptionLimit

		ginkgo.BeforeEach(func() {
			preemptionLimit = utiltestingalpha.MakePreemptionLimit("limit-"+ns.Name, kueuealpha.GlobalLimitAggregation).
				Limit(1).
				WindowSeconds(300).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, preemptionLimit)

			gomega.Eventually(func(g gomega.Gomega) {
				var got kueuealpha.PreemptionLimit
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(preemptionLimit), &got)).To(gomega.Succeed())
				g.Expect(got.Status.Window).NotTo(gomega.BeNil())
				g.Expect(got.Status.Window.LastUpdateTime.IsZero()).To(gomega.BeFalse())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, preemptionLimit, true, behavioral.MediumTimeout)
		})

		ginkgo.It("Should enforce PreemptionLimits on Jobs", func() {
			baseJob := jobtesting.MakeJob("", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				TerminationGracePeriod(1)

			var lowJob, midJob1 *batchv1.Job

			ginkgo.By("Running two jobs that fill the ClusterQueue", func() {
				lowJob = baseJob.Clone().GeneratedName("low-job-").Label(priorityLabel, "1").Obj()
				highJob := baseJob.Clone().GeneratedName("high-job-").Label(priorityLabel, "9").Obj()
				behavioral.MustCreate(ctx, k8sClient, lowJob)
				behavioral.MustCreate(ctx, k8sClient, highJob)

				e2e.ExpectJobToBeRunning(ctx, k8sClient, lowJob)
				e2e.ExpectJobToBeRunning(ctx, k8sClient, highJob)
			})

			ginkgo.By("Allowing the first preemption within the limit", func() {
				midJob1 = baseJob.Clone().GeneratedName("mid-job-").Label(priorityLabel, "5").Obj()
				behavioral.MustCreate(ctx, k8sClient, midJob1)

				e2e.ExpectJobToBeRunning(ctx, k8sClient, midJob1)
				e2e.ExpectJobToBePreempted(ctx, k8sClient, lowJob, kueue.ConfigurablePreemptionReason)

				// Remove lowJob so its equal-priority Workload does not bulk-skip midJob2.
				gomega.Expect(k8sClient.Delete(ctx, lowJob, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowJob, false)
				lowWl := utiltestingapi.MakeWorkload(
					workloadjob.GetWorkloadNameForJob(lowJob.Name, lowJob.UID),
					lowJob.Namespace,
				).Obj()
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowWl, false)
			})

			ginkgo.By("Blocking a second preemption once the limit is reached", func() {
				midJob2 := baseJob.Clone().GeneratedName("mid-job-").Label(priorityLabel, "7").Obj()
				behavioral.MustCreate(ctx, k8sClient, midJob2)

				midWl2Key := types.NamespacedName{
					Name:      workloadjob.GetWorkloadNameForJob(midJob2.Name, midJob2.UID),
					Namespace: ns.Name,
				}
				behavioral.ExpectWorkloadsToBePendingByKeys(ctx, k8sClient, midWl2Key)
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, midWl2Key, &wl)).To(gomega.Succeed())
					g.Expect(wl.Status.Conditions).To(gomega.ContainElement(gomega.And(
						gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
						gomega.HaveField("Status", metav1.ConditionFalse),
						gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonWaitingForQuota),
						gomega.HaveField("Message", gomega.ContainSubstring("preemptions blocked by PreemptionLimit "+preemptionLimit.Name)),
					)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				midJob2Key := client.ObjectKeyFromObject(midJob2)
				midJob1Key := client.ObjectKeyFromObject(midJob1)
				gomega.Consistently(func(g gomega.Gomega) {
					var gotMid2 batchv1.Job
					g.Expect(k8sClient.Get(ctx, midJob2Key, &gotMid2)).To(gomega.Succeed())
					g.Expect(gotMid2.Spec.Suspend).To(gomega.Equal(new(true)))

					var gotMid1 batchv1.Job
					g.Expect(k8sClient.Get(ctx, midJob1Key, &gotMid1)).To(gomega.Succeed())
					g.Expect(gotMid1.Spec.Suspend).To(gomega.Equal(new(false)))
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})
	})
})
