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

package queuename

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/util/testing"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeploy "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsts "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ManageJobsWithoutQueueName", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		configurationUpdate := time.Now()
		config := defaultKueueCfg.DeepCopy()
		config.ManageJobsWithoutQueueName = true
		util.ApplyKueueConfiguration(ctx, k8sClient, config)
		util.RestartKueueController(ctx, k8sClient)
		ginkgo.GinkgoLogr.Info("Kueue configuration updated", "took", time.Since(configurationUpdate))
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defaultRf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultRf)).Should(gomega.Succeed())
		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(defaultRf.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
		localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})
	ginkgo.When("manageJobsWithoutQueueName=true", func() {
		ginkgo.It("should suspend a job", func() {
			var testJob, createdJob *batchv1.Job
			var jobLookupKey types.NamespacedName
			ginkgo.By("creating an unsuspended job without a queue name", func() {
				testJob = testingjob.MakeJob("test-job", ns.Name).
					Suspend(false).
					Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testJob)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the job gets suspended", func() {
				createdJob = &batchv1.Job{}
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the queue-name label", func() {
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					createdJob.Labels["kueue.x-k8s.io/queue-name"] = "main"
					g.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("verifying that the job is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("verifying that the job has been admitted", func() {
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(testJob.Name, testJob.UID), Namespace: ns.Name}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend child jobs of admitted jobs", func() {
			numPods := 2
			aw := awtesting.MakeAppWrapper("aw-child", ns.Name).
				Component(awtesting.Component{
					Template: testingjob.MakeJob("job-0", ns.Name).
						RequestAndLimit(corev1.ResourceCPU, "200m").
						Parallelism(int32(numPods)).
						Completions(int32(numPods)).
						Suspend(false).
						Image(util.E2eTestAgnHostImage, util.BehaviorExitFast).
						SetTypeMeta().Obj(),
				}).
				Suspend(false).
				Obj()

			ginkgo.By("creating an unsuspended appwrapper without a queue name", func() {
				gomega.Expect(k8sClient.Create(ctx, aw)).To(gomega.Succeed())
			})

			ginkgo.By("verifying that the appwrapper gets suspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the queue-name label", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				awLookupKey := types.NamespacedName{Name: aw.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, awLookupKey, createdAppWrapper)).Should(gomega.Succeed())
					createdAppWrapper.Labels["kueue.x-k8s.io/queue-name"] = "main"
					g.Expect(k8sClient.Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for appwrapper to be unsuspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the wrapped Job to successfully complete", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend grandchildren jobs of admitted jobs", func() {
			aw := awtesting.MakeAppWrapper("aw-grandchild", ns.Name).
				Component(awtesting.Component{
					Template: testingjobset.MakeJobSet("job-set", ns.Name).
						ReplicatedJobs(
							testingjobset.ReplicatedJobRequirements{
								Name:        "replicated-job-1",
								Replicas:    2,
								Parallelism: 2,
								Completions: 2,
								Image:       util.E2eTestAgnHostImage,
								Args:        util.BehaviorExitFast,
							},
						).
						SetTypeMeta().
						Suspend(false).
						RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "200m").
						Obj(),
				}).
				Suspend(false).
				Obj()

			ginkgo.By("creating an unsuspended appwrapper without a queue name", func() {
				gomega.Expect(k8sClient.Create(ctx, aw)).To(gomega.Succeed())
			})

			ginkgo.By("verifying that the appwrapper gets suspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the queue-name label", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				awLookupKey := types.NamespacedName{Name: aw.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, awLookupKey, createdAppWrapper)).Should(gomega.Succeed())
					createdAppWrapper.Labels["kueue.x-k8s.io/queue-name"] = "main"
					g.Expect(k8sClient.Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for appwrapper to be unsuspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the wrapped JobSet to successfully complete", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperSucceeded))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend a pod created in the test namespace", func() {
			var testPod, createdPod *corev1.Pod
			var podLookupKey types.NamespacedName
			ginkgo.By("creating a pod without a queue name", func() {
				testPod = testingpod.MakePod("test-pod", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testPod)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the pod is gated", func() {
				createdPod = &corev1.Pod{}
				podLookupKey = types.NamespacedName{Name: testPod.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).Should(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).ShouldNot(gomega.BeEmpty())
					g.Expect(createdPod.Labels).Should(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
					g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(podcontroller.PodFinalizer))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should NOT suspend a pod created in the kube-system namespace", func() {
			var testPod, createdPod *corev1.Pod
			var podLookupKey types.NamespacedName
			ginkgo.By("creating a pod without a queue name", func() {
				testPod = testingpod.MakePod("test-pod", "kube-system").
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					TerminationGracePeriod(1).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testPod)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the pod is running", func() {
				createdPod = &corev1.Pod{}
				podLookupKey = types.NamespacedName{Name: testPod.Name, Namespace: "kube-system"}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).Should(gomega.Succeed())
					g.Expect(createdPod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("deleting the pod", func() {
				gomega.Expect(k8sClient.Delete(ctx, testPod)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).Should(testing.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend the pods of a deployment created in the test namespace", func() {
			var testDeploy *appsv1.Deployment
			ginkgo.By("creating a deployment without a queue name", func() {
				testDeploy = testingdeploy.MakeDeployment("test-deploy", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Replicas(2).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testDeploy)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the pods of the deployment are gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
						client.MatchingLabels(testDeploy.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(2))
					for _, pod := range pods.Items {
						g.Expect(pod.Spec.SchedulingGates).ShouldNot(gomega.BeEmpty())
						g.Expect(pod.Labels).Should(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
						g.Expect(pod.Finalizers).Should(gomega.ContainElement(podcontroller.PodFinalizer))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend the pods created by a deployment in the kube-system namespace", func() {
			var testDeploy *appsv1.Deployment
			ginkgo.By("creating a deployment without a queue name in the kube-system namespace", func() {
				testDeploy = testingdeploy.MakeDeployment("test-deploy", "kube-system").
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					TerminationGracePeriod(1).
					Replicas(2).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testDeploy)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the pods of the deployment are running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace("kube-system"),
						client.MatchingLabels(testDeploy.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(2))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that deleting the deployment removes the pods", func() {
				pods := &corev1.PodList{}
				gomega.Expect(k8sClient.Delete(ctx, testDeploy)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace("kube-system"),
						client.MatchingLabels(testDeploy.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend the pods of a StatefulSet created in the test namespace", func() {
			var testSts *appsv1.StatefulSet
			ginkgo.By("creating a StatefulSet without a queue name", func() {
				testSts = testingsts.MakeStatefulSet("test-sts", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Replicas(2).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testSts)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the pods of the StatefulSet are gated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name),
						client.MatchingLabels(testSts.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					// If the first pod can't be scheduled, the second won't be created
					g.Expect(pods.Items).Should(gomega.HaveLen(1))
					for _, pod := range pods.Items {
						g.Expect(pod.Spec.SchedulingGates).ShouldNot(gomega.BeEmpty())
						g.Expect(pod.Labels).Should(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
						g.Expect(pod.Finalizers).Should(gomega.ContainElement(podcontroller.PodFinalizer))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend the pods created by a StatefulSet in the kube-system namespace", func() {
			var testSts *appsv1.StatefulSet
			ginkgo.By("creating a StatefulSet without a queue name in the kube-system namespace", func() {
				testSts = testingsts.MakeStatefulSet("test-sts", "kube-system").
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Replicas(2).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, testSts)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the pods of the StatefulSet are running", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace("kube-system"),
						client.MatchingLabels(testSts.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(2))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that deleting the StatefulSet removes the pods", func() {
				pods := &corev1.PodList{}
				gomega.Expect(k8sClient.Delete(ctx, testSts)).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace("kube-system"),
						client.MatchingLabels(testSts.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
