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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Kueue", func() {
	var ns *corev1.Namespace
	var sampleJob *batchv1.Job
	var jobKey types.NamespacedName

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		sampleJob = testingjob.MakeJob("test-job", ns.Name).
			Queue("main").
			Request("cpu", "1").
			Request("memory", "20Mi").
			Obj()
		jobKey = client.ObjectKeyFromObject(sampleJob)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Creating a Job without a matching LocalQueue", func() {
		ginkgo.It("Should stay in suspended", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, jobKey, createdJob); err != nil {
					return false
				}
				return *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.IsAdmitted(createdWorkload)

			}, util.Timeout, util.Interval).Should(gomega.BeFalse())
			gomega.Expect(k8sClient.Delete(ctx, sampleJob)).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a Job With Queueing", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			onDemandRF = testing.MakeResourceFlavor("on-demand").
				Label("instance-type", "on-demand").Obj()
			gomega.Expect(k8sClient.Create(ctx, onDemandRF)).Should(gomega.Succeed())
			spotRF = testing.MakeResourceFlavor("spot").
				Label("instance-type", "spot").Obj()
			gomega.Expect(k8sClient.Create(ctx, spotRF)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas("on-demand").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*testing.MakeFlavorQuotas("spot").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, spotRF, true)
		})

		ginkgo.It("Should unsuspend a job and set nodeSelectors", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image("sleep", "gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			createdWorkload := &kueue.Workload{}
			expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(jobKey.Name), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return workload.IsAdmitted(createdWorkload) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should readmit preempted job into a separate flavor", func() {
			gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())

			highPriorityClass := testing.MakePriorityClass("high").PriorityValue(100).Obj()
			gomega.Expect(k8sClient.Create(ctx, highPriorityClass))
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, highPriorityClass)).To(gomega.Succeed())
			})

			ginkgo.By("Job is admitted using the first flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is preempted by higher priority job", func() {
				job := testingjob.MakeJob("high", ns.Name).
					Queue("main").
					PriorityClass("high").
					Request(corev1.ResourceCPU, "1").
					NodeSelector("instance-type", "on-demand"). // target the same flavor to cause preemption
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

				expectJobUnsuspendedWithNodeSelectors(client.ObjectKeyFromObject(job), map[string]string{
					"instance-type": "on-demand",
				})
			})

			ginkgo.By("Job is re-admitted using the second flavor", func() {
				expectJobUnsuspendedWithNodeSelectors(jobKey, map[string]string{
					"instance-type": "spot",
				})
			})
		})
	})
})

func expectJobUnsuspendedWithNodeSelectors(key types.NamespacedName, ns map[string]string) {
	job := &batchv1.Job{}
	gomega.EventuallyWithOffset(1, func() []any {
		gomega.Expect(k8sClient.Get(ctx, key, job)).To(gomega.Succeed())
		return []any{*job.Spec.Suspend, job.Spec.Template.Spec.NodeSelector}
	}, util.Timeout, util.Interval).Should(gomega.Equal([]any{false, ns}))
}
