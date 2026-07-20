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

package extended

import (
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/jobset/api/jobset/v1alpha2"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	awtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingdeploy "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	leaderworkersettesting "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ManageJobsWithoutQueueName", ginkgo.Label("feature:managejobswithoutqueuename"), ginkgo.Ordered, func() {
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
								Parallelism: 1,
								Completions: 1,
								Image:       util.GetAgnHostImage(),
								Args:        util.BehaviorExitFast,
							},
						).
						SetTypeMeta().
						Suspend(false).
						RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "300m").
						RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "300Mi").
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
								Parallelism: 1,
								Completions: 1,
								Image:       util.GetAgnHostImage(),
								Args:        util.BehaviorExitFast,
								Labels: map[string]string{
									controllerconstants.QueueLabel: localQueue.Name,
								},
							},
						).
						SetTypeMeta().
						Suspend(false).
						RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "300m").
						RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "300Mi").
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdJobSet := &v1alpha2.JobSet{}

			ginkgo.By("Checking that the JobSet is created and unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(new(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "300m").
				RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "300Mi").
				Obj()

			ginkgo.By("Creating an unsuspended JobSet without a queue-name", func() {
				util.MustCreate(ctx, k8sClient, jobSet)
			})

			createdJobSet := &v1alpha2.JobSet{}

			ginkgo.By("Verifying that the JobSet gets suspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).To(gomega.Equal(new(true)))
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
					g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(new(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the JobSet is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Status.TerminalState).Should(gomega.Equal(string(v1alpha2.JobSetCompleted)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(k8sClient.Get(ctx, deploymentKey, createdDeployment)).To(utiltesting.BeNotFoundError())
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
					g.Expect(createdWorkloads.Items[0].Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the Deployment ready", func() {
				createdDeployment := &appsv1.Deployment{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, deploymentKey, createdDeployment)).To(gomega.Succeed())
					g.Expect(createdDeployment.Status.ReadyReplicas).To(gomega.Equal(int32(3)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(createdWorkloads.Items[0].Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadAdmitted))
					util.MustHaveOwnerReference(g, createdWorkloads.Items[0].OwnerReferences, lws, k8sClient.Scheme())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for replicas is ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
					g.Expect(createdLeaderWorkerSet.Status.Conditions).To(utiltesting.HaveConditionStatusTrue("Available"))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

				ginkgo.By("Delete the LeaderWorkerSet", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
				})

				ginkgo.By("Check workload is deleted", func() {
					createdWorkload := &kueue.Workload{}
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload)).To(utiltesting.BeNotFoundError())
					}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
					g.Expect(createdWorkloads.Items[0].Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadQuotaReserved))
					util.MustHaveOwnerReference(g, createdWorkloads.Items[0].OwnerReferences, lws, k8sClient.Scheme())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verify that replicas is not ready", func() {
				createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
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
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).Should(gomega.BeTrue())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the LeaderWorkerSet", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lws, true)
			})

			ginkgo.By("Check workload is deleted", func() {
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, util.WorkloadKeyForLeaderWorkerSet(lws, "0"), createdWorkload)).To(utiltesting.BeNotFoundError())
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should suspend the pods created by a LeaderWorkerSet without queue-name label and unsuspend after set queue-name", func() {
			lws := leaderworkersettesting.MakeLeaderWorkerSet("lws", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Size(1).
				Replicas(1).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				TerminationGracePeriod(1).
				Obj()
			ginkgo.By("creating a LeaderWorkerSet", func() {
				util.MustCreate(ctx, k8sClient, lws)
			})

			wlName := leaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0")

			ginkgo.By("checking that only one workload is created", func() {
				createdWorkloads := &kueue.WorkloadList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, createdWorkloads, client.InNamespace(lws.Namespace))).To(gomega.Succeed())
					g.Expect(createdWorkloads.Items).To(gomega.HaveLen(1))
					g.Expect(createdWorkloads.Items[0].Name).To(gomega.Equal(wlName))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			wlLookupKey := types.NamespacedName{Name: wlName, Namespace: ns.Name}

			ginkgo.By("checking that workload is inadmissible", func() {
				util.ExpectWorkloadsToBeInadmissibleByKeys(ctx, k8sClient, wlLookupKey)
			})

			createdLeaderWorkerSet := &leaderworkersetv1.LeaderWorkerSet{}

			ginkgo.By("checking that replicas is not ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(0)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that pods are gated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(1))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).Should(gomega.Equal(corev1.PodPending))
						g.Expect(utilpod.HasGate(&pod, podconstants.SchedulingGateName)).Should(gomega.BeTrue())
					}
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the queue-name label", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					if createdLeaderWorkerSet.Labels == nil {
						createdLeaderWorkerSet.Labels = make(map[string]string, 1)
					}
					createdLeaderWorkerSet.Labels[controllerconstants.QueueLabel] = localQueue.Name
					g.Expect(k8sClient.Update(ctx, createdLeaderWorkerSet)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that workload is updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec.QueueName).To(gomega.Equal(kueue.LocalQueueName(localQueue.Name)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking that workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlLookupKey)
			})

			ginkgo.By("checking that replicas is ready", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLeaderWorkerSet)).To(gomega.Succeed())
					g.Expect(createdLeaderWorkerSet.Status.ReadyReplicas).To(gomega.Equal(int32(1)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that pods are running and ungated", func() {
				pods := &corev1.PodList{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.List(ctx, pods, client.MatchingLabels{
						leaderworkersetv1.SetNameLabelKey: lws.Name,
					}, client.InNamespace(lws.Namespace))).Should(gomega.Succeed())
					g.Expect(pods.Items).To(gomega.HaveLen(1))
					for _, pod := range pods.Items {
						g.Expect(pod.Status.Phase).Should(gomega.Equal(corev1.PodRunning))
					}
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("ManageJobsWithoutQueueName with namespace selector excluding test namespace", ginkgo.Label("feature:managejobswithoutqueuename"), func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

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

		// Unfortunately, we can't move it to BeforeAll, due to we need to know the namespace name.
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.ManageJobsWithoutQueueName = true
			cfg.ManagedJobsNamespaceSelector = &metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "kubernetes.io/metadata.name",
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{ns.Name, "kueue-system", "kube-system"},
				}},
			}
		})
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.MediumTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.MediumTimeout)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("should not suspend Jobs from unmanaged JobSet", func() {
		jobSet := testingjobset.MakeJobSet("job-set", ns.Name).
			Suspend(false).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "test-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					Image:       util.GetAgnHostImage(),
					Args:        util.BehaviorExitFast,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:        "test-job-2",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					Image:       util.GetAgnHostImage(),
					Args:        util.BehaviorExitFast,
				},
			).Obj()

		ginkgo.By("creating a JobSet", func() {
			util.MustCreate(ctx, k8sClient, jobSet)
		})

		jobs := &batchv1.JobList{}

		ginkgo.By("verifying that the jobs are not suspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, jobs, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(jobs.Items).To(gomega.HaveLen(2))
				for _, job := range jobs.Items {
					g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeFalse()))
				}
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the jobs are completed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.List(ctx, jobs, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(jobs.Items).To(gomega.HaveLen(2))
				for _, job := range jobs.Items {
					g.Expect(job.Spec.Suspend).To(gomega.HaveValue(gomega.BeFalse()))
					g.Expect(job.Status.Succeeded).To(gomega.Equal(int32(1)))
				}
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the JobSet is completed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := &v1alpha2.JobSet{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(jobSet), createdJobSet)).To(gomega.Succeed())
				g.Expect(createdJobSet.Status.TerminalState).To(gomega.Equal(string(v1alpha2.JobSetCompleted)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("ManageJobsWithoutQueueName without JobSet integration", ginkgo.Label("feature:managejobswithoutqueuename"), ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		defaultRf    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
			cfg.ManageJobsWithoutQueueName = true
			cfg.Integrations.Frameworks = slices.DeleteFunc(cfg.Integrations.Frameworks, func(framework string) bool {
				return framework == jobset.FrameworkName
			})
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
								Parallelism: 1,
								Completions: 1,
								Image:       util.GetAgnHostImage(),
								Args:        util.BehaviorExitFast,
							},
						).
						SetTypeMeta().
						Suspend(false).
						RequestAndLimit("replicated-job-1", corev1.ResourceCPU, "300m").
						RequestAndLimit("replicated-job-1", corev1.ResourceMemory, "300Mi").
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
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			createdJobSet := &v1alpha2.JobSet{}

			ginkgo.By("Checking that the JobSet is created and unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Spec.Suspend).Should(gomega.Equal(new(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the JobSet is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobSetKey, createdJobSet)).Should(gomega.Succeed())
					g.Expect(createdJobSet.Status.TerminalState).Should(gomega.Equal(string(v1alpha2.JobSetCompleted)))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
