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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeploy "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AppWrapper", func() {
	var (
		ns                 *corev1.Namespace
		rf                 *kueue.ResourceFlavor
		cq                 *kueue.ClusterQueue
		lq                 *kueue.LocalQueue
		resourceFlavorName string
		clusterQueueName   string
		localQueueName     string
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "appwrapper-e2e-")
		resourceFlavorName = "appwrapper-rf-" + ns.Name
		clusterQueueName = "appwrapper-cq-" + ns.Name
		localQueueName = "appwrapper-lq-" + ns.Name

		rf = utiltestingapi.MakeResourceFlavor(resourceFlavorName).
			NodeLabel("instance-type", "on-demand").
			Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue(clusterQueueName).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavorName).
					Resource(corev1.ResourceCPU, "5").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
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
					RequestAndLimit(corev1.ResourceCPU, "100m").
					Parallelism(int32(numPods)).
					Completions(int32(numPods)).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					SetTypeMeta().Obj(),
			}).
			Queue(localQueueName).
			Obj()

		ginkgo.By("Create an appwrapper", func() {
			util.MustCreate(ctx, k8sClient, aw)
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
		deploymentKey := types.NamespacedName{Name: "deployment", Namespace: ns.Name}
		replicas := int32(3)
		aw := awtesting.MakeAppWrapper("aw", ns.Name).
			Queue(lq.Name).
			Suspend(true).
			Component(awtesting.Component{
				Template: testingdeploy.MakeDeployment(deploymentKey.Name, deploymentKey.Namespace).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
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
			util.MustCreate(ctx, k8sClient, aw)
		})

		ginkgo.By("Wait for AppWrapper to be unsuspended", func() {
			createdAppWrapper := &awv1beta2.AppWrapper{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Check that only one workload is created and admitted", func() {
			createdWorkloads := &kueue.WorkloadList{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(aw.Namespace))).To(gomega.Succeed())
				g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
				g.Expect(createdWorkloads.Items[0].Name).To(gomega.Equal(appwrapper.GetWorkloadNameForAppWrapper(aw.Name, aw.UID)))
				g.Expect(createdWorkloads.Items[0].Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the Deployment ready", func() {
			createdDeployment := &appsv1.Deployment{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, deploymentKey, createdDeployment)).To(gomega.Succeed())
				g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
