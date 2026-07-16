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

package extended

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ClusterQueue Max Execution Time", ginkgo.Label("feature:clusterqueuemaxexecutiontime"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns         *corev1.Namespace
		rf         *kueue.ResourceFlavor
		cqWithTime *kueue.ClusterQueue
		cqNoTime   *kueue.ClusterQueue
		lqWithTime *kueue.LocalQueue
		lqNoTime   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		util.WaitForJobSetAvailability(ctx, k8sClient)
		util.WaitForLeaderWorkerSetAvailability(ctx, k8sClient)

		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			if cfg.FeatureGates == nil {
				cfg.FeatureGates = make(map[string]bool)
			}
			cfg.FeatureGates[string(features.ClusterQueueMaxExecutionTime)] = true
		})

		rf = utiltestingapi.MakeResourceFlavor("cq-exec-time-rf").Obj()
		util.MustCreate(ctx, k8sClient, rf)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-cq-exec-time-")

		cqWithTime = utiltestingapi.MakeClusterQueue("cq-with-time-" + ns.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			MaximumExecutionTimeSeconds(120).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqWithTime)

		cqNoTime = utiltestingapi.MakeClusterQueue("cq-no-time-" + ns.Name).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqNoTime)

		lqWithTime = utiltestingapi.MakeLocalQueue("lq-with-timeout", ns.Name).
			ClusterQueue(cqWithTime.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqWithTime)

		lqNoTime = utiltestingapi.MakeLocalQueue("lq-without-timeout", ns.Name).
			ClusterQueue(cqNoTime.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqNoTime)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cqWithTime, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cqNoTime, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Job", func() {
		ginkgo.It("should set workload MaximumExecutionTimeSeconds from ClusterQueue default", func() {
			job := testingjob.MakeJob("job-cq-timeout", ns.Name).
				Queue(kueue.LocalQueueName(lqWithTime.Name)).
				Suspend(true).
				Parallelism(1).
				Request(corev1.ResourceCPU, "100m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).NotTo(gomega.BeNil())
				g.Expect(*createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.Equal(int32(120)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should use job label over ClusterQueue default for MaximumExecutionTimeSeconds", func() {
			job := testingjob.MakeJob("job-label-timeout", ns.Name).
				Queue(kueue.LocalQueueName(lqWithTime.Name)).
				Suspend(true).
				Parallelism(1).
				Request(corev1.ResourceCPU, "100m").
				Label(constants.MaxExecTimeSecondsLabel, "30").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).NotTo(gomega.BeNil())
				g.Expect(*createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.Equal(int32(30)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should not set MaximumExecutionTimeSeconds when ClusterQueue has no default", func() {
			job := testingjob.MakeJob("job-no-timeout", ns.Name).
				Queue(kueue.LocalQueueName(lqNoTime.Name)).
				Suspend(true).
				Parallelism(1).
				Request(corev1.ResourceCPU, "100m").
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a JobSet", func() {
		ginkgo.It("should set workload MaximumExecutionTimeSeconds from ClusterQueue default", func() {
			jobSet := testingjobset.MakeJobSet("jobset-cq-timeout", ns.Name).
				Queue(lqWithTime.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
					},
				).
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "100m").
				Obj()
			util.MustCreate(ctx, k8sClient, jobSet)

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{
				Name:      workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).NotTo(gomega.BeNil())
				g.Expect(*createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.Equal(int32(120)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should not set MaximumExecutionTimeSeconds when ClusterQueue has no default", func() {
			jobSet := testingjobset.MakeJobSet("jobset-no-timeout", ns.Name).
				Queue(lqNoTime.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorWaitForDeletion,
						Replicas:    1,
						Parallelism: 1,
						Completions: 1,
					},
				).
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "100m").
				Obj()
			util.MustCreate(ctx, k8sClient, jobSet)

			createdWorkload := &kueue.Workload{}
			wlLookupKey := types.NamespacedName{
				Name:      workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID),
				Namespace: ns.Name,
			}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a LeaderWorkerSet", func() {
		ginkgo.It("should set workload MaximumExecutionTimeSeconds from ClusterQueue default", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws-cq-timeout", ns.Name).
				Queue(lqWithTime.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).NotTo(gomega.BeNil())
				g.Expect(*createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.Equal(int32(120)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should use job label over ClusterQueue default for MaximumExecutionTimeSeconds", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws-label-timeout", ns.Name).
				Queue(lqWithTime.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				Label(constants.MaxExecTimeSecondsLabel, "30").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).NotTo(gomega.BeNil())
				g.Expect(*createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.Equal(int32(30)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should not set MaximumExecutionTimeSeconds when ClusterQueue has no default", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws-no-timeout", ns.Name).
				Queue(lqNoTime.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				Request(corev1.ResourceCPU, "100m").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), lws)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			wlLookupKey := util.WorkloadKeyForLeaderWorkerSet(lws, "0")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec.MaximumExecutionTimeSeconds).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
