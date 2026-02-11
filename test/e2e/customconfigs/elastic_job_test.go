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

package customconfigse2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

var gvk = batchv1.SchemeGroupVersion.WithKind("Job")

var _ = ginkgo.Describe("Elastic Job", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates[string(features.ElasticJobsViaWorkloadSlices)] = true
			cfg.Integrations.Frameworks = []string{"batch/job"}
		})
	})

	ginkgo.BeforeEach(func() {
		defaultRf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultRf)
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-job-")
		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
	})

	// This test ensures that a plain job can be scheduled when Elastic Jobs are enabled.
	ginkgo.It("Should schedule a plain job", func() {
		workloadKey := func(job *batchv1.Job) types.NamespacedName {
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVK(job.Name, job.UID, gvk), Namespace: job.Namespace}
		}
		job := testingjob.MakeJob("job1", ns.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "100m").
			Parallelism(1).
			Completions(3).
			Obj()
		wl := &kueue.Workload{}

		ginkgo.By("Creating a plain job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("Waiting for a job's workload to be admitted", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, workloadKey(job), wl)).Should(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for job to be unsuspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(job.Spec.Suspend, false)).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		// Scaling-up a plain job will result in the job's workload deletion and re-creation.
		ginkgo.By("Scaling-up job results in a new workload", func() {
			oldUID := wl.UID
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				job.Spec.Parallelism = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
				g.Expect(wl.Spec.PodSets[0].Count).Should(gomega.Equal(int32(2)))
				g.Expect(workload.IsAdmitted(wl)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// While the original (wl) and newWl share the same name, they have different UIDs.
			gomega.Expect(oldUID).ShouldNot(gomega.Equal(wl.UID))
		})
	})

	// This test ensures that an elastic job can be scheduled when Elastic Jobs are enabled.
	ginkgo.It("Should schedule an elastic job", func() {
		workloadKey := func(job *batchv1.Job) types.NamespacedName {
			return types.NamespacedName{Name: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(job.Name, job.UID, gvk, job.Generation), Namespace: job.Namespace}
		}
		job := testingjob.MakeJob("job1", ns.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Request(corev1.ResourceCPU, "100m").
			Parallelism(1).
			Completions(3).
			Obj()
		originalWl := &kueue.Workload{}
		scaledUpWl := &kueue.Workload{}
		scaledDownWl := &kueue.Workload{}

		ginkgo.By("Creating a job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("Waiting for a job's workload to be admitted", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, workloadKey(job), originalWl)).Should(gomega.Succeed())
				g.Expect(workload.IsAdmitted(originalWl)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for job to be unsuspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(job.Spec.Suspend, false)).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		// Scaling-up a job will result in the job's workload deletion and re-creation.
		ginkgo.By("Scaling-up job results in a new workload slice", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				job.Spec.Parallelism = ptr.To(int32(2))
				g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, workloadKey(job), scaledUpWl)).Should(gomega.Succeed())
				g.Expect(scaledUpWl.Spec.PodSets[0].Count).Should(gomega.Equal(int32(2)))
				g.Expect(workload.IsAdmitted(scaledUpWl)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Old workload should be marked as Finished", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(originalWl), originalWl)).Should(gomega.Succeed())
				g.Expect(workload.IsFinished(originalWl)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		// Scaling-down a job does not result in a new workload.
		ginkgo.By("Scaling-down job does not result in a new workload slice", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				job.Spec.Parallelism = ptr.To(int32(1))
				g.Expect(k8sClient.Update(ctx, job)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(scaledUpWl), scaledDownWl)).Should(gomega.Succeed())
				g.Expect(scaledDownWl.Spec.PodSets[0].Count).Should(gomega.Equal(int32(1)))
				g.Expect(workload.IsAdmitted(scaledDownWl)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Expect(scaledUpWl.UID).Should(gomega.Equal(scaledDownWl.UID))
		})
	})
})
