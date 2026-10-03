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
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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
)

var _ = ginkgo.Describe("Configurable Preemption", ginkgo.Label("feature:configurablepreemption"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	preemptionConfigName := "preemption-config"
	priorityLabel := "test-priority-label"
	preemptionConfig := utiltestingalpha.MakePreemptionConfig(preemptionConfigName).
		Rule("preempt-within-cq-lower-priority", kueuealpha.Always,
			kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
					{
						Key:           priorityLabel,
						FallbackValue: ptr.To[int32](0),
						Comparison:    ptr.To(kueuealpha.LessThan),
					},
				},
			},
		).Obj()

	ginkgo.BeforeAll(func() {
		behavioral.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{
				string(features.ConfigurablePreemptions):      true,
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
			Annotation(kueuealpha.PreemptionConfigNameAnnotation, preemptionConfigName).
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
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "1").
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, lowPriorityJob)

			highPriorityJob := jobtesting.MakeJob("high-priority-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "9").
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, highPriorityJob)

			ginkgo.By("Waiting for workloads to be admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(lowPriorityJob))
				behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("Create preempting job")
			preemptingJob := jobtesting.MakeJob("preempting-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "5").
				TerminationGracePeriod(1).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, preemptingJob)

			ginkgo.By("Verify preemption")
			gomega.Eventually(func(g gomega.Gomega) {
				behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(preemptingJob))
				behavioral.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob))

				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPriorityJob), lowPriorityJob)).Should(gomega.Succeed())
				g.Expect(lowPriorityJob.Spec.Suspend).Should(gomega.Equal(new(true)))

				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lowPriorityJob.Name, lowPriorityJob.UID), Namespace: ns.Name}
				lowPriorityWorkload := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, wlLookupKey, lowPriorityWorkload)).Should(gomega.Succeed())
				g.Expect(lowPriorityWorkload.Status.Conditions).Should(gomega.ContainElement(gomega.BeComparableTo(
					metav1.Condition{
						Type:   kueue.WorkloadPreempted,
						Status: metav1.ConditionTrue,
						Reason: "ConfigurablePreemption",
					},
					cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message", "ObservedGeneration"),
				)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
