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
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"k8s.io/utils/strings/slices"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	"sigs.k8s.io/kueue/pkg/features"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	"sigs.k8s.io/kueue/pkg/util/testing"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeploy "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
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
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.ManageJobsWithoutQueueName = true
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defaultRf = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultRf)
		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(defaultRf.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("manageJobsWithoutQueueName=true and LocalQueueDefaulting=false", func() {
		ginkgo.It("should suspend a job", func() {
			var testJob, createdJob *batchv1.Job
			var jobLookupKey types.NamespacedName

			ginkgo.By("setting local queue defulting as false", func() {
				util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
					cfg.FeatureGates = map[string]bool{string(features.LocalQueueDefaulting): false}
					cfg.ManageJobsWithoutQueueName = true
				})
			})

			ginkgo.By("creating a default LocalQueue", func() {
				localQueue = testing.MakeLocalQueue("default", ns.Name).ClusterQueue("cluster-queue").Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.By("creating an unsuspended job without a queue name", func() {
				testJob = testingjob.MakeJob("test-job", ns.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, testJob)
			})

			ginkgo.By("verifying that the job does get suspended", func() {
				createdJob = &batchv1.Job{}
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying no existence of kueue labels", func() {
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(createdJob.Labels).ShouldNot(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, "default"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("setting feature gates back to its original state", func() {
				util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
					cfg.ManageJobsWithoutQueueName = true
				})
			})
		})
	})

	ginkgo.When("manageJobsWithoutQueueName=true and LocalQueueDefaulting=true", func() {
		ginkgo.It("should not suspend a job", func() {
			var testJob, createdJob *batchv1.Job
			var jobLookupKey types.NamespacedName

			ginkgo.By("creating a default LocalQueue", func() {
				localQueue = testing.MakeLocalQueue("default", ns.Name).ClusterQueue("cluster-queue").Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
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
	})

	ginkgo.When("manageJobsWithoutQueueName=true", func() {
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
						Image(util.GetAgnHostImage(), util.BehaviorExitFast).
						SetTypeMeta().Obj(),
				}).
				Suspend(false).
				Obj()

			ginkgo.By("creating an unsuspended appwrapper without a queue name", func() {
				util.MustCreate(ctx, k8sClient, aw)
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
					createdAppWrapper.Labels[controllerconstants.QueueLabel] = localQueue.Name
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
								Image:       util.GetAgnHostImage(),
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
				util.MustCreate(ctx, k8sClient, aw)
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
					createdAppWrapper.Labels[controllerconstants.QueueLabel] = localQueue.Name
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

		ginkgo.It("should not admit child jobs and jobset even if the child job and jobset has a queue-name label", func() {
			jobSetKey := client.ObjectKey{Name: "job-set", Namespace: ns.Name}

			aw := awtesting.MakeAppWrapper("aw", ns.Name).
				Component(awtesting.Component{
					Template: testingjobset.MakeJobSet(jobSetKey.Name, jobSetKey.Namespace).
						ReplicatedJobs(
							testingjobset.ReplicatedJobRequirements{
								Name:        "replicated-job-1",
								Replicas:    2,
								Parallelism: 2,
								Completions: 2,
								Image:       util.GetAgnHostImage(),
								Args:        util.BehaviorExitFast,
								Labels: map[string]string{
									controllerconstants.QueueLabel: localQueue.Name,
								},
							},
						).
						SetTypeMeta().
						Suspend(false).
						RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "200m").
						Queue(localQueue.Name).
						Obj(),
				}).
				Suspend(false).
				Obj()

			ginkgo.By("Creating an unsuspended AppWrapper without a queue-name", func() {
				util.MustCreate(ctx, k8sClient, aw)
			})

			createdAppWrapper := &awv1beta2.AppWrapper{}

			ginkgo.By("Verifying that the AppWrapper gets suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the queue-name label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).Should(gomega.Succeed())
					createdAppWrapper.Labels[controllerconstants.QueueLabel] = localQueue.Name
					g.Expect(k8sClient.Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the AppWrapper is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdJobSet := &v1alpha2.JobSet{}

			ginkgo.By("Checking that the JobSet is created and unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the JobSet is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Status.TerminalState).Should(gomega.Equal(string(v1alpha2.JobSetCompleted)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not admit child jobs even if the child job has a queue-name label", func() {
			jobSet := testingjobset.MakeJobSet("job-set", ns.Name).
				ReplicatedJobs(
					testingjobset.ReplicatedJobRequirements{
						Name:        "replicated-job-1",
						Replicas:    2,
						Parallelism: 2,
						Completions: 2,
						Image:       util.GetAgnHostImage(),
						Args:        util.BehaviorExitFast,
						Labels: map[string]string{
							controllerconstants.QueueLabel: localQueue.Name,
						},
					},
				).
				SetTypeMeta().
				Suspend(false).
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "200m").
				Obj()

			ginkgo.By("Creating an unsuspended JobSet without a queue-name", func() {
				util.MustCreate(ctx, k8sClient, jobSet)
			})

			createdJobSet := &v1alpha2.JobSet{}

			ginkgo.By("Verifying that the JobSet gets suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).To(gomega.Equal(ptr.To(true)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Setting the queue-name label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).Should(gomega.Succeed())
					if createdJobSet.Labels == nil {
						createdJobSet.Labels = map[string]string{}
					}
					createdJobSet.Labels[controllerconstants.QueueLabel] = localQueue.Name
					g.Expect(k8sClient.Update(ctx, createdJobSet)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the JobSet is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the JobSet is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Status.TerminalState).Should(gomega.Equal(string(v1alpha2.JobSetCompleted)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend a pod created in the test namespace", func() {
			var testPod, createdPod *corev1.Pod
			var podLookupKey types.NamespacedName
			ginkgo.By("creating a pod without a queue name", func() {
				testPod = testingpod.MakePod("test-pod", ns.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
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
					g.Expect(createdPod.Finalizers).Should(gomega.ContainElement(podcontroller.PodFinalizer))
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
					g.Expect(k8sClient.Get(ctx, podLookupKey, createdPod)).Should(testing.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
						g.Expect(pod.Finalizers).Should(gomega.ContainElement(podcontroller.PodFinalizer))
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

			wlKey := types.NamespacedName{Name: statefulset.GetWorkloadName(sts.Name), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}

			ginkgo.By("check that workload is created and not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdStatefulSet := &appsv1.StatefulSet{}

			ginkgo.By("verify that replicas is not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that only one pod is created, pending and gated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(createdStatefulSet.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(1))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodPending))
						g.Expect(utilpod.HasGate(&pod, podcontroller.SchedulingGateName)).Should(gomega.BeTrue())
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(createdWorkload.Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify that replicas are ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(sts), createdStatefulSet)).To(gomega.Succeed())
					g.Expect(createdStatefulSet.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that all pod are running and ungated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels(createdStatefulSet.Spec.Selector.MatchLabels))).To(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
						g.Expect(utilpod.HasGate(&pod, podcontroller.SchedulingGateName)).Should(gomega.BeFalse())
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
					Obj()
				util.MustCreate(ctx, k8sClient, testSts)
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

		ginkgo.It("Should suspend Pods of a created Deployment in the test namespace", func() {
			deploymentKey := types.NamespacedName{Name: "deployment", Namespace: ns.Name}
			replicas := int32(3)

			aw := awtesting.MakeAppWrapper("aw", ns.Name).
				Suspend(false).
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

			ginkgo.By("creating an AppWrapper", func() {
				util.MustCreate(ctx, k8sClient, aw)
			})

			ginkgo.By("verifying that the AppWrapper gets suspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the Deployment doesn't create", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, deploymentKey, createdDeployment)).To(testing.BeNotFoundError())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the queue-name label", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).Should(gomega.Succeed())
					createdAppWrapper.Labels[controllerconstants.QueueLabel] = localQueue.Name
					g.Expect(k8sClient.Update(ctx, createdAppWrapper)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for AppWrapper to be unsuspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Check that only one workload is created and admitted", func() {
				createdWorkloads := &kueue.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(aw.Namespace))).To(gomega.Succeed())
					g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
					g.Expect(createdWorkloads.Items[0].Name).To(gomega.Equal(appwrapper.GetWorkloadNameForAppWrapper(aw.Name, aw.UID)))
					g.Expect(createdWorkloads.Items[0].Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the Deployment ready", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, deploymentKey, createdDeployment)).To(gomega.Succeed())
					g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not suspend the pods created by a LeaderWorkerSet in the test namespace with queue-name label", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Queue(localQueue.Name).
				Obj()
			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("check that only one workload is created and admitted", func() {
				createdWorkloads := &kueue.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(lws.Namespace))).To(gomega.Succeed())
					g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
					g.Expect(createdWorkloads.Items[0].Name).To(gomega.Equal(leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0")))
					g.Expect(createdWorkloads.Items[0].Status.Conditions).To(testing.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					util.MustHaveOwnerReference(g, createdWorkloads.Items[0].OwnerReferences, lws, k8sClient.Scheme())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for replicas is ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(testing.HaveConditionStatusTrue("Available"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that pods are running", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Delete the LeaderWorkerSet", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
				})

				ginkgo.By("Check workload is deleted", func() {
					wlLookupKey := types.NamespacedName{
						Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
						Namespace: ns.Name,
					}
					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(testing.BeNotFoundError())
					}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
				})
			})
		})

		ginkgo.It("should suspend the pods created by a LeaderWorkerSet in the test namespace without queue-name label", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(3).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Obj()
			ginkgo.By("Create a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			ginkgo.By("check that only one workload is created and is not admitted", func() {
				createdWorkloads := &kueue.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(lws.Namespace))).To(gomega.Succeed())
					g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
					g.Expect(createdWorkloads.Items[0].Name).To(gomega.Equal(leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0")))
					g.Expect(createdWorkloads.Items[0].Status.Conditions).To(testing.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
					util.MustHaveOwnerReference(g, createdWorkloads.Items[0].OwnerReferences, lws, k8sClient.Scheme())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify that replicas is not ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that pods are running and gated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(3))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).Should(gomega.Equal(corev1.PodPending))
						g.Expect(utilpod.HasGate(&pod, podcontroller.SchedulingGateName)).Should(gomega.BeTrue())
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})

			ginkgo.By("Check workload is deleted", func() {
				wlLookupKey := types.NamespacedName{
					Name:      leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"),
					Namespace: ns.Name,
				}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(testing.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("ManageJobsWithoutQueueName without JobSet integration", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.ManageJobsWithoutQueueName = true
			cfg.Integrations.Frameworks = slices.Filter(nil, cfg.Integrations.Frameworks, func(framework string) bool {
				return framework != jobset.FrameworkName
			})
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defaultRf = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultRf)
		clusterQueue = testing.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*testing.MakeFlavorQuotas(defaultRf.Name).
					Resource(corev1.ResourceCPU, "2").
					Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
		util.MustCreate(ctx, k8sClient, clusterQueue)
		localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		util.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("manageJobsWithoutQueueName=true", func() {
		ginkgo.It("should create only one workload for parent job", func() {
			jobSetKey := client.ObjectKey{Name: "job-set", Namespace: ns.Name}

			aw := awtesting.MakeAppWrapper("aw", ns.Name).
				Queue(localQueue.Name).
				Component(awtesting.Component{
					Template: testingjobset.MakeJobSet(jobSetKey.Name, jobSetKey.Namespace).
						ReplicatedJobs(
							testingjobset.ReplicatedJobRequirements{
								Name:        "replicated-job-1",
								Replicas:    2,
								Parallelism: 2,
								Completions: 2,
								Image:       util.GetAgnHostImage(),
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

			ginkgo.By("Creating an unsuspended AppWrapper without a queue-name", func() {
				util.MustCreate(ctx, k8sClient, aw)
			})

			ginkgo.By("Checking that the AppWrapper is unsuspended", func() {
				createdAppWrapper := &awv1beta2.AppWrapper{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(aw), createdAppWrapper)).To(gomega.Succeed())
					g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdJobSet := &v1alpha2.JobSet{}

			ginkgo.By("Checking that the JobSet is created and unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the JobSet is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Status.TerminalState).Should(gomega.Equal(string(v1alpha2.JobSetCompleted)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
