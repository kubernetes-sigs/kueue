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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job reconciliation with ManagedJobsNamespaceSelectorAlwaysRespected", ginkgo.Ordered, func() {
	const (
		cqName = "cluster-queue"
		rfName = "default"
		lqName = "user-queue"
	)

	var (
		rf *kueue.ResourceFlavor
		lq *kueue.LocalQueue
		cq *kueue.ClusterQueue
		ns *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.FeatureGates = map[string]bool{string(features.ManagedJobsNamespaceSelectorAlwaysRespected): true}
			cfg.ManagedJobsNamespaceSelector = &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "managed-by-kueue",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"true"},
					},
				},
			}
		})

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "managed-ns-",
				Labels: map[string]string{
					"managed-by-kueue": "true",
				},
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		rf = testing.MakeResourceFlavor(rfName).Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue(cqName).
			ResourceGroup(*testing.MakeFlavorQuotas(rfName).Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = testing.MakeLocalQueue(lqName, ns.Name).ClusterQueue(cqName).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})

	ginkgo.AfterAll(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.LongTimeout)
	})

	ginkgo.It("should not reconcile a job in the default (unmanaged) namespace", func() {
		job := testingjob.MakeJob("unmanaged-job", metav1.NamespaceDefault).
			Queue(kueue.LocalQueueName(lq.Name)).
			Suspend(true).
			Image(util.GetAgnHostImage(), util.BehaviorExitFast).
			Obj()

		gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

		wls := &kueue.WorkloadList{}
		gomega.Expect(k8sClient.List(ctx, wls, client.InNamespace(metav1.NamespaceDefault))).To(gomega.Succeed())
		gomega.Expect(wls.Items).To(gomega.BeEmpty(), "Expected no workload in unmanaged namespace")
	})

	ginkgo.It("should reconcile a job in managed namespace and create a workload", func() {
		job := testingjob.MakeJob("managed-job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Suspend(true).
			Image(util.GetAgnHostImage(), util.BehaviorExitFast).
			Obj()

		gomega.Expect(k8sClient.Create(ctx, job)).To(gomega.Succeed())

		ginkgo.By("check that only one workload is created", func() {
			createdWorkloads := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Verify existing behavior - should not reconcile a pod in the default (unmanaged) namespace", func() {
		testPod := testingpod.MakePod("test-pod", metav1.NamespaceDefault).
			Queue(lq.Name).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, testPod)).To(gomega.Succeed())

		wls := &kueue.WorkloadList{}
		gomega.Expect(k8sClient.List(ctx, wls, client.InNamespace(metav1.NamespaceDefault))).To(gomega.Succeed())
		gomega.Expect(wls.Items).To(gomega.BeEmpty(), "Expected no workload for pod in unmanaged namespace")
	})
})
