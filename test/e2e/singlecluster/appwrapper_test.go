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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/util/testing"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeploy "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AppWrapper", func() {
	const (
		resourceFlavorName = "appwrapper-rf"
		clusterQueueName   = "appwrapper-cq"
		localQueueName     = "appwrapper-lq"
	)

	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "appwrapper-e2e-")

		rf = testing.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).To(gomega.Succeed())

		cq = testing.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*testing.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("Should admit Workload for Job", func() {
		numPods := 2
		aw := awtesting.MakeAppWrapper("appwrapper", ns.Name).
			Component(awtesting.Component{
				Template: utiltestingjob.MakeJob("job-0", ns.Name).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					Parallelism(int32(numPods)).
					Completions(int32(numPods)).
					Suspend(false).
					Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
					SetTypeMeta().Obj(),
			}).
			Queue(localQueueName).
			Obj()

		ginkgo.By("Create an appwrapper", func() {
			gomega.Expect(k8sClient.Create(ctx, aw)).To(gomega.Succeed())
		})

		ginkgo.By("Wait for appwrapper to be unsuspended", func() {
			createdAppWrapper := &awv1beta2.AppWrapper{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Wait for the wrapped Job to successfully complete", func() {
			createdAppWrapper := &awv1beta2.AppWrapper{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Delete the appwrapper", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, aw, true)
		})
	})

	ginkgo.It("Should admit Workload for Deployment Pods", func() {
		replicas := int32(3)
		aw := awtesting.MakeAppWrapper("aw", ns.Name).
			Queue(lq.Name).
			Suspend(true).
			Component(awtesting.Component{
				Template: testingdeploy.MakeDeployment("deployment", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "200m").
					TerminationGracePeriod(1).
					Replicas(3).
					SetTypeMeta().
					Obj(),
				DeclaredPodSets: []awv1beta2.AppWrapperPodSet{{
					Replicas: &replicas,
					Path:     "template.spec.template",
				}},
			}).
			Obj()

		ginkgo.By("Creating an AppWrapper", func() {
			gomega.Expect(k8sClient.Create(ctx, aw)).To(gomega.Succeed())
		})

		ginkgo.By("Wait for AppWrapper to be unsuspended", func() {
			createdAppWrapper := &awv1beta2.AppWrapper{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Wait for Workload to be admitted", func() {
			wlKey := client.ObjectKey{
				Name:      appwrapper.GetWorkloadNameForAppWrapper(aw.Name, aw.UID),
				Namespace: aw.Namespace,
			}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
