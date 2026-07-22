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
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdeploy "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	testingsts "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ManageJobsWithoutQueueName", ginkgo.Label("feature:managejobswithoutqueuename", util.Shard1), ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.ManageJobsWithoutQueueName = true
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defaultRf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultRf)
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(defaultRf.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.MediumTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("manageJobsWithoutQueueName=true", func() {
		ginkgo.It("should not suspend a job", func() {
			var testJob, createdJob *batchv1.Job
			var jobLookupKey types.NamespacedName

			ginkgo.By("creating a default LocalQueue", func() {
				localQueue = utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue("cluster-queue").Obj()
				util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
			})

			ginkgo.By("creating an unsuspended job without a queue name", func() {
				testJob = testingjob.MakeJob("test-job", ns.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, testJob)
			})

			ginkgo.By("verifying that the job does not get suspended", func() {
				createdJob = &batchv1.Job{}
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying existence of kueue labels", func() {
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, "default"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the job has been admitted", func() {
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(testJob.Name, testJob.UID), Namespace: ns.Name}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, createdWorkload)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend a job", func() {
			var testJob, createdJob *batchv1.Job
			var jobLookupKey types.NamespacedName
			ginkgo.By("creating an unsuspended job without a queue name", func() {
				testJob = testingjob.MakeJob("test-job", ns.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, testJob)
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
					createdJob.Labels[controllerconstants.QueueLabel] = localQueue.Name
					g.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("verifying that the job is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("verifying that the job has been admitted", func() {
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(testJob.Name, testJob.UID), Namespace: ns.Name}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend a pod created in the test namespace", func() {
			var testPod, createdPod *corev1.Pod
			var podLookupKey types.NamespacedName
			ginkgo.By("creating a pod without a queue name", func() {
				testPod = testingpod.MakePod("test-pod", ns.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					TerminationGracePeriod(1).
					Obj()
				util.MustCreate(ctx, k8sClient, testPod)
			})

			ginkgo.By("verifying that the pod is gated", func() {
				createdPod = &corev1.Pod{}
				podLookupKey = types.NamespacedName{Name: testPod.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).Should(gomega.Succeed())
					g.Expect(createdPod.Spec.SchedulingGates).ShouldNot(gomega.BeEmpty())
					g.Expect(createdPod.Labels).Should(gomega.HaveKeyWithValue(constants.ManagedByKueueLabelKey, constants.ManagedByKueueLabelValue))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should NOT suspend a pod created in the kube-system namespace", func() {
			var testPod, createdPod *corev1.Pod
			var podLookupKey types.NamespacedName
			ginkgo.By("creating a pod without a queue name", func() {
				testPod = testingpod.MakePod("test-pod", "kube-system").
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					TerminationGracePeriod(1).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Obj()
				util.MustCreate(ctx, k8sClient, testPod)
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
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).Should(utiltesting.BeNotFoundError())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend the pods of a deployment created in the test namespace", func() {
			var testDeploy *appsv1.Deployment
			ginkgo.By("creating a deployment without a queue name", func() {
				testDeploy = testingdeploy.MakeDeployment("test-deploy", ns.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Replicas(2).
					TerminationGracePeriod(1).
					Obj()
				util.MustCreate(ctx, k8sClient, testDeploy)
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
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend the pods created by a deployment in the kube-system namespace", func() {
			var testDeploy *appsv1.Deployment
			ginkgo.By("creating a deployment without a queue name in the kube-system namespace", func() {
				testDeploy = testingdeploy.MakeDeployment("test-deploy", "kube-system").
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					TerminationGracePeriod(1).
					Replicas(2).
					Obj()
				util.MustCreate(ctx, k8sClient, testDeploy)
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

		ginkgo.It("should suspend the pods created by a StatefulSet in the test namespace without queue-name label", func() {
			sts := testingsts.MakeStatefulSet("sts", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Replicas(3).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Obj()
			ginkgo.By("Create a StatefulSet", func() {
				util.MustCreate(ctx, k8sClient, sts)
			})

			wlKey := types.NamespacedName{Name: statefulset.GetWorkloadName(sts.UID, sts.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By("check that workload is created and not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdStatefulSet := &appsv1.StatefulSet{}

			ginkgo.By("verify that replicas is not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that only one pod is created, pending and gated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(createdStatefulSet.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(1))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodPending))
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).Should(gomega.BeTrue())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the queue-name label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).Should(gomega.Succeed())
					if createdStatefulSet.Labels == nil {
						createdStatefulSet.Labels = map[string]string{}
					}
					createdStatefulSet.Labels[controllerconstants.QueueLabel] = localQueue.Name
					g.Expect(k8sClient.Update(ctx, createdStatefulSet)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("check that workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify that replicas are ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that all pod are running and ungated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(createdStatefulSet.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).Should(gomega.BeFalse())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend the pods created by a StatefulSet in the kube-system namespace", func() {
			var testSts *appsv1.StatefulSet
			ginkgo.By("creating a StatefulSet without a queue name in the kube-system namespace", func() {
				testSts = testingsts.MakeStatefulSet("test-sts", "kube-system").
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					RequestAndLimit(corev1.ResourceMemory, "2Gi").
					Replicas(2).
					TerminationGracePeriod(1).
					Obj()
				util.MustCreate(ctx, k8sClient, testSts)
			})

			ginkgo.By("verifying that the pods of the StatefulSet are ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					pods := &corev1.PodList{}
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace("kube-system"),
						client.MatchingLabels(testSts.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).Should(gomega.HaveLen(2))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Conditions).Should(gomega.ContainElement(gomega.BeComparableTo(
							corev1.PodCondition{
								Type:   corev1.PodReady,
								Status: corev1.ConditionTrue,
							},
							util.IgnorePodConditionTimestampsMessageAndObservedGeneration,
						)))
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
