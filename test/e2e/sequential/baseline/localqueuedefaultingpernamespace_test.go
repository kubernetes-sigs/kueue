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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe(
	"LocalQueueDefaultingPerNamespace",
	ginkgo.Label("feature:localqueuedefaultingpernamespace", util.Shard0),
	ginkgo.Ordered,
	func() {
		var (
			managedDefaultingNs   *corev1.Namespace
			managedNoDefaultingNs *corev1.Namespace
			unmanagedNs           *corev1.Namespace
			rf                    *kueue.ResourceFlavor
			cq                    *kueue.ClusterQueue
		)

		ginkgo.BeforeAll(func() {
			util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
				cfg.ManageJobsWithoutQueueName = true
				cfg.ManagedJobsNamespaceSelector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"kueue-managed": "true"},
				}
				cfg.LocalQueueDefaultingNamespaceSelector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"local-queue-defaulting": "true"},
				}
			})

			managedDefaultingNs = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "managed-defaulting-",
					Labels: map[string]string{
						"kueue-managed":          "true",
						"local-queue-defaulting": "true",
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, managedDefaultingNs)).To(gomega.Succeed())

			managedNoDefaultingNs = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "managed-no-defaulting-",
					Labels: map[string]string{
						"kueue-managed": "true",
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, managedNoDefaultingNs)).To(gomega.Succeed())

			unmanagedNs = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "unmanaged-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, unmanagedNs)).To(gomega.Succeed())

			rf = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(rf.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			for _, ns := range []*corev1.Namespace{managedDefaultingNs, managedNoDefaultingNs} {
				lq := utiltestingapi.MakeLocalQueue("default", ns.Name).ClusterQueue("cluster-queue").Obj()
				util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
			}
			unmanagedLq := utiltestingapi.MakeLocalQueue("default", unmanagedNs.Name).ClusterQueue("cluster-queue").Obj()
			util.MustCreate(ctx, k8sClient, unmanagedLq)
		})

		ginkgo.AfterAll(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, managedDefaultingNs)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, managedNoDefaultingNs)).To(gomega.Succeed())
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, unmanagedNs)).To(gomega.Succeed())
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.MediumTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.MediumTimeout)
		})

		ginkgo.It("should inject default queue label in managed namespace with defaulting label", func() {
			var job *batchv1.Job
			var createdJob *batchv1.Job

			ginkgo.By("creating an unsuspended job without a queue name", func() {
				job = testingjob.MakeJob("job-defaulting", managedDefaultingNs.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, job)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true)
				})
			})

			ginkgo.By("verifying the job gets the default queue label", func() {
				createdJob = &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, "default"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the job has been admitted", func() {
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not inject default queue label in managed namespace without defaulting label", func() {
			var job *batchv1.Job

			ginkgo.By("creating an unsuspended job without a queue name", func() {
				job = testingjob.MakeJob("job-no-defaulting", managedNoDefaultingNs.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, job)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true)
				})
			})

			ginkgo.By("verifying the job does not get the default queue label", func() {
				createdJob := &batchv1.Job{}
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).To(gomega.Succeed())
				gomega.Expect(createdJob.Labels).ShouldNot(gomega.HaveKey(controllerconstants.QueueLabel))
			})
		})

		ginkgo.It("should not inject default queue label in unmanaged namespace", func() {
			var job *batchv1.Job

			ginkgo.By("creating an unsuspended job without a queue name", func() {
				job = testingjob.MakeJob("job-unmanaged", unmanagedNs.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, job)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true)
				})
			})

			ginkgo.By("verifying the job does not get the default queue label", func() {
				createdJob := &batchv1.Job{}
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).To(gomega.Succeed())
				gomega.Expect(createdJob.Labels).ShouldNot(gomega.HaveKey(controllerconstants.QueueLabel))
			})
		})

		ginkgo.It("should inject default queue label for a Pod in managed namespace with defaulting label", func() {
			var pod *corev1.Pod

			ginkgo.By("creating a pod without a queue name", func() {
				pod = testingpod.MakePod("pod-defaulting", managedDefaultingNs.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					Obj()
				util.MustCreate(ctx, k8sClient, pod)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
				})
			})

			ginkgo.By("verifying the pod gets the default queue label", func() {
				createdPod := &corev1.Pod{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).To(gomega.Succeed())
					g.Expect(createdPod.Labels).Should(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, "default"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not inject default queue label for a Pod in managed namespace without defaulting label", func() {
			var pod *corev1.Pod

			ginkgo.By("creating a pod without a queue name", func() {
				pod = testingpod.MakePod("pod-no-defaulting", managedNoDefaultingNs.Name).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					Obj()
				util.MustCreate(ctx, k8sClient, pod)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
				})
			})

			ginkgo.By("verifying the pod does not get the default queue label", func() {
				createdPod := &corev1.Pod{}
				gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: pod.Name, Namespace: pod.Namespace}, createdPod)).To(gomega.Succeed())
				gomega.Expect(createdPod.Labels).ShouldNot(gomega.HaveKey(controllerconstants.QueueLabel))
			})
		})
	},
)

var _ = ginkgo.Describe(
	"LocalQueueDefaultingPerNamespace disabled",
	ginkgo.Label("feature:localqueuedefaultingpernamespace", util.Shard0),
	ginkgo.Ordered,
	func() {
		var (
			managedNoDefaultingNs *corev1.Namespace
			rf                    *kueue.ResourceFlavor
			cq                    *kueue.ClusterQueue
		)

		ginkgo.BeforeAll(func() {
			util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *config.Configuration) {
				cfg.ManageJobsWithoutQueueName = true
				cfg.ManagedJobsNamespaceSelector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"kueue-managed": "true"},
				}
				cfg.LocalQueueDefaultingNamespaceSelector = &metav1.LabelSelector{
					MatchLabels: map[string]string{"local-queue-defaulting": "true"},
				}
				cfg.FeatureGates = map[string]bool{
					string(features.LocalQueueDefaultingPerNamespace): false,
				}
			})

			managedNoDefaultingNs = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "managed-no-defaulting-",
					Labels: map[string]string{
						"kueue-managed": "true",
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, managedNoDefaultingNs)).To(gomega.Succeed())

			rf = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, rf)

			cq = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(rf.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("default", managedNoDefaultingNs.Name).ClusterQueue("cluster-queue").Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterAll(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, managedNoDefaultingNs)).To(gomega.Succeed())
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.MediumTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.MediumTimeout)
		})

		ginkgo.It("should inject default queue label when feature gate is disabled", func() {
			var job *batchv1.Job
			var createdJob *batchv1.Job

			ginkgo.By("creating an unsuspended job without a queue name", func() {
				job = testingjob.MakeJob("job-gate-disabled", managedNoDefaultingNs.Name).
					Suspend(false).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					Obj()
				util.MustCreate(ctx, k8sClient, job)
				ginkgo.DeferCleanup(func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, job, true)
				})
			})

			ginkgo.By("verifying the job gets the default queue label", func() {
				createdJob = &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, createdJob)).To(gomega.Succeed())
					g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(controllerconstants.QueueLabel, "default"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying the job has been admitted", func() {
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: job.Namespace}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	},
)
