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

package reconcilejobsinmanagednamespace

import (
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job reconciliation with ManagedJobsNamespaceSelectorAlwaysRespected", ginkgo.Ordered, func() {
	const (
		managedNamespace   = "test-managed"
		unmanagedNamespace = "default"
		cqName             = "cluster-queue"
		rfName             = "default"
		lqName             = "user-queue"
	)

	var (
		rf  *kueue.ResourceFlavor
		lq  *kueue.LocalQueue
		cq  *kueue.ClusterQueue
		ctx context.Context
	)

	ginkgo.BeforeAll(func() {
		ctx = context.Background()

		ns := &corev1.Namespace{}
		err := k8sClient.Get(ctx, client.ObjectKey{Name: managedNamespace}, ns)
		if apierrors.IsNotFound(err) {
			ns = &corev1.Namespace{
				ObjectMeta: v1.ObjectMeta{
					Name: managedNamespace,
					Labels: map[string]string{
						"managed-by-kueue": "true",
					},
				},
			}
			err = k8sClient.Create(ctx, ns)
		}
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		rf = testing.MakeResourceFlavor(rfName).Obj()

		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue(cqName).
			ResourceGroup(*testing.MakeFlavorQuotas(rfName).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = testing.MakeLocalQueue(lqName, managedNamespace).ClusterQueue(cqName).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})

	ginkgo.AfterAll(func() {
		_ = k8sClient.DeleteAllOf(ctx, &batchv1.Job{}, client.InNamespace(managedNamespace))
		_ = k8sClient.DeleteAllOf(ctx, &batchv1.Job{}, client.InNamespace(unmanagedNamespace))

		_ = k8sClient.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(managedNamespace))
		_ = k8sClient.DeleteAllOf(ctx, &kueue.Workload{}, client.InNamespace(unmanagedNamespace))

		_ = k8sClient.Delete(ctx, lq)

		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, lq, true, util.LongTimeout)
	})

	ginkgo.It("should not reconcile a job in the default (unmanaged) namespace", func() {
		job := testingjob.MakeJob("unmanaged-job", unmanagedNamespace).
			Queue(kueue.LocalQueueName(lq.Name)).
			Suspend(true).
			Image(util.GetAgnHostImage(), util.BehaviorExitFast).
			Obj()

		gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

		wlList := &kueue.WorkloadList{}
		gomega.Expect(k8sClient.List(ctx, wlList, client.InNamespace(unmanagedNamespace))).To(gomega.Succeed())
		gomega.Expect(wlList.Items).To(gomega.BeEmpty(), "Expected no workload in unmanaged namespace")
	})

	ginkgo.It("should reconcile a job in managed namespace and create a workload", func() {
		job := testingjob.MakeJob("managed-job", managedNamespace).
			Queue(kueue.LocalQueueName(lq.Name)).
			Suspend(true).
			Image(util.GetAgnHostImage(), util.BehaviorExitFast).
			Obj()

		gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

		ginkgo.By("check that only one workload is created and admitted", func() {
			createdWorkloads := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(managedNamespace))).To(gomega.Succeed())
				g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
				g.Expect(createdWorkloads.Items[0].Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Verify existing behavior - should not reconcile a pod in the default (unmanaged) namespace", func() {
		testPod := testingpod.MakePod("test-pod", unmanagedNamespace).
			Queue(lq.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, testPod)).To(gomega.Succeed())

		wlList := &kueue.WorkloadList{}
		gomega.Expect(k8sClient.List(ctx, wlList, client.InNamespace(unmanagedNamespace))).To(gomega.Succeed())
		gomega.Expect(wlList.Items).To(gomega.BeEmpty(), "Expected no workload for pod in unmanaged namespace")
	})
})
