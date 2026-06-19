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
	"k8s.io/apimachinery/pkg/types"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("WorkloadPriorityClassDefaulting", ginkgo.Label("feature:workloadpriorityclassdefaulting", util.Shard0), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns         *corev1.Namespace
		rf         *kueue.ResourceFlavor
		cq         *kueue.ClusterQueue
		lq         *kueue.LocalQueue
		defaultWPC *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.FeatureGates = map[string]bool{string(features.WorkloadPriorityClassDefaulting): true}
		})

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.MediumTimeout)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wpc-defaulting-")
		lq = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

		defaultWPC = utiltestingapi.MakeWorkloadPriorityClass(controllerconstants.DefaultWorkloadPriorityClassName).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sClient, defaultWPC)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultWPC, true, util.MediumTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("should default the WorkloadPriorityClass and admit a job with the defaulted priority", func() {
		var job *batchv1.Job

		ginkgo.By("creating a job without a WorkloadPriorityClass label", func() {
			job = testingjob.MakeJob("job-no-wpc", ns.Name).
				Queue("main").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				Obj()
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("verifying the default WorkloadPriorityClass label was set on the job", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(
					controllerconstants.WorkloadPriorityClassLabel,
					controllerconstants.DefaultWorkloadPriorityClassName,
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the workload has the default WorkloadPriorityClass priority", func() {
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, controllerconstants.DefaultWorkloadPriorityClassName, 100, wlLookupKey)
		})

		ginkgo.By("verifying the workload is admitted and the job completes", func() {
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, util.MediumTimeout)
		})
	})

	ginkgo.It("should not override an existing WorkloadPriorityClass label", func() {
		highWPC := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj()
		util.MustCreate(ctx, k8sClient, highWPC)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, highWPC, true, util.MediumTimeout)
		})

		var job *batchv1.Job

		ginkgo.By("creating a job with an explicit WorkloadPriorityClass label", func() {
			job = testingjob.MakeJob("job-with-wpc", ns.Name).
				Queue("main").
				WorkloadPriorityClass("high").
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				RequestAndLimit(corev1.ResourceCPU, "100m").
				Obj()
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("verifying the existing WorkloadPriorityClass label is preserved", func() {
			createdJob := &batchv1.Job{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, createdJob)).Should(gomega.Succeed())
				g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(
					controllerconstants.WorkloadPriorityClassLabel, "high",
				))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the workload has the explicit WorkloadPriorityClass priority", func() {
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: ns.Name}
			util.ExpectWorkloadsWithWorkloadPriority(ctx, k8sClient, "high", 1000, wlLookupKey)
		})
	})
})
