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
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	jobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Configuration Preemptions", ginkgo.Label("feature:configurablepreemption"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	preemptionConfigName := "preemption-config"
	priorityLabel := "test-priority-label"
	preemptionConfig := kueuealpha.PreemptionConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: preemptionConfigName,
		},
		Spec: kueuealpha.PreemptionConfigSpec{
			Rules: []kueuealpha.PreemptionConfigPreemptionRule{
				{
					Name:             "test-rule-one",
					ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{Trigger: kueuealpha.Always},
					CandidateSelectors: []kueuealpha.PreemptionConfigPreemptionCandidateSelector{
						{
							Scope: kueuealpha.WithinClusterQueue,
							NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
								{
									Key:           priorityLabel,
									FallbackValue: ptr.To[int32](0),
									Comparison:    ptr.To(kueuealpha.LessThan),
								},
							},
						},
					},
				},
			},
		},
	}

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
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
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ns-")

		util.MustCreate(ctx, k8sClient, &preemptionConfig)

		rf = utiltestingapi.MakeResourceFlavor("rf-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cohort := kueue.CohortReference("cohort-" + ns.Name)

		cq = utiltestingapi.MakeClusterQueue("cq-"+ns.Name).
			Cohort(cohort).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(corev1.ResourceMemory, "2G").
				Obj()).
			Annotation(kueuealpha.PreemptionConfigNameAnnotation, preemptionConfigName).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &preemptionConfig, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Configurable preemption enabled", func() {
		ginkgo.It("Should preempt in the same LQ with lower priority", func() {
			ginkgo.By("Create jobs for admission")
			lowPriorityJob := jobtesting.MakeJob("low-priority-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "1").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, lowPriorityJob)

			highPriorityJob := jobtesting.MakeJob("high-priority-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "9").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, highPriorityJob)

			ginkgo.By("Waiting for workloads to be admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(lowPriorityJob))
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Create preempting job")
			preemptingJob := jobtesting.MakeJob("preempting-job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "1").
				RequestAndLimit(corev1.ResourceMemory, "200Mi").
				Label(priorityLabel, "5").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, preemptingJob)

			ginkgo.By("Verify preemption")
			gomega.Eventually(func(g gomega.Gomega) {
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(preemptingJob))
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(highPriorityJob))

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
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
