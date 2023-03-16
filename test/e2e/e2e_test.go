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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

var _ = ginkgo.Describe("Kueue", func() {
	var ns *corev1.Namespace
	var sampleJob *batchv1.Job

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		sampleJob = testingjob.MakeJob("test-job", ns.Name).Queue("main").Request("cpu", "1").Request("memory", "20Mi").
			Image("sleep", "gcr.io/k8s-staging-perf-tests/sleep:v0.0.3", []string{"5s"}).Obj()

		gomega.Expect(k8sClient.Create(ctx, sampleJob)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.When("Creating a Job without a matching LocalQueue", func() {
		ginkgo.It("Should stay in suspended", func() {
			lookupKey := types.NamespacedName{Name: "test-job", Namespace: ns.Name}
			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
					return false
				}
				return *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lookupKey.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)

			}, util.Timeout, util.Interval).Should(gomega.BeFalse())
			gomega.Expect(k8sClient.Delete(ctx, sampleJob)).Should(gomega.Succeed())
		})
	})
	ginkgo.When("Creating a Job With Queueing", func() {
		var (
			resourceKueue *kueue.ResourceFlavor
			localQueue    *kueue.LocalQueue
			clusterQueue  *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			resourceKueue = testing.MakeResourceFlavor("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceKueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).Obj()
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "36Gi").
					Obj(),
				).
				Obj()
			localQueue.Spec.ClusterQueue = "cluster-queue"
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteLocalQueue(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectClusterQueueToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectResourceFlavorToBeDeleted(ctx, k8sClient, resourceKueue, true)
		})
		ginkgo.It("Should unsuspend a job", func() {
			createdJob := &batchv1.Job{}
			createdWorkload := &kueue.Workload{}
			lookupKey := types.NamespacedName{Name: "test-job", Namespace: ns.Name}

			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
					return false
				}
				return !*createdJob.Spec.Suspend && createdJob.Status.Succeeded > 0
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(lookupKey.Name), Namespace: ns.Name}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, wlLookupKey, createdWorkload); err != nil {
					return false
				}
				return apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted) &&
					apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadFinished)

			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})
	})
})
