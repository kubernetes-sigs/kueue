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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("WaitForPodsReady Job Controller E2E", func() {
	var (
		ns    *corev1.Namespace
		rf    *kueue.ResourceFlavor
		cq    *kueue.ClusterQueue
		lq    *kueue.LocalQueue
		job   *batchv1.Job
		wlKey types.NamespacedName
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wfpr-")

		rf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("WaitForPodsReady has a tiny Timeout and no RecoveryTimeout", func() {
		ginkgo.BeforeEach(func() {
			cfg := defaultKueueCfg.DeepCopy()
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Enable:          true,
				BlockAdmission:  ptr.To(true),
				Timeout:         &metav1.Duration{Duration: util.TinyTimeout},
				RecoveryTimeout: nil,
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(10)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}

			util.ApplyKueueConfiguration(ctx, k8sClient, cfg)
			util.RestartKueueController(ctx, k8sClient)
		})

		ginkgo.It("should evict and requeue workload when pods readiness timeout is surpassed", func() {
			ginkgo.By("creating a job that will never report its pods as ready", func() {
				job = testingjob.MakeJob("job-timeout", ns.Name).
					Queue(lq.Name).
					Request(corev1.ResourceCPU, "2").
					Parallelism(1).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			wlKey = types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			workload := &kueue.Workload{}
			ginkgo.By("waiting for the workload to be created and verifying it is not admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())

					g.Expect(workload.Status.Admission).To(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for the workload to be evicted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())

					cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(cond).ShouldNot(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadWaitForStart))

					evictedCond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadEvicted)
					g.Expect(evictedCond).ShouldNot(gomega.BeNil())
					g.Expect(evictedCond.Status).To(gomega.Equal(metav1.ConditionTrue))
					g.Expect(evictedCond.Reason).To(gomega.Equal(kueue.WorkloadEvictedByPodsReadyTimeout))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the job is suspended", func() {
				updatedJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), updatedJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(updatedJob.Spec.Suspend, false)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the workload is requeued", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
					g.Expect(workload.Status.RequeueState).ShouldNot(gomega.BeNil())
					g.Expect(*workload.Status.RequeueState.Count).To(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("simulating a second eviction cycle to check workload deactivation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
					g.Expect(workload.Status.RequeueState).Should(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("WaitForPodsReady has default Timeout and a short RecoveryTimeout", func() {
		ginkgo.BeforeEach(func() {
			cfg := defaultKueueCfg.DeepCopy()
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Enable:          true,
				BlockAdmission:  ptr.To(true),
				RecoveryTimeout: &metav1.Duration{Duration: util.ShortTimeout},
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(1)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}

			util.ApplyKueueConfiguration(ctx, k8sClient, cfg)
			util.RestartKueueController(ctx, k8sClient)
		})

		ginkgo.It("should evict and requeue workload when pod failure causes recovery timeout", func() {
			ginkgo.By("creating a job", func() {
				job = testingjob.MakeJob("job-recovery-timeout", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					Queue(lq.Name).
					Request(corev1.ResourceCPU, "2").
					Parallelism(1).
					BackoffLimitPerIndex(2).
					CompletionMode(batchv1.IndexedCompletion).
					Completions(1).
					Obj()

				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			wlKey = types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("checking workload availability", func() {
				workload := &kueue.Workload{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
					cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(cond).ShouldNot(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("simulating pod failure", func() {
				util.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 1, 1)
			})

			ginkgo.By("verifying that the workload is requeued", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload

					g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())

					g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
					g.Expect(*wl.Status.RequeueState.Count).To(gomega.Equal(int32(1)))

					recoveredCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(recoveredCond).ShouldNot(gomega.BeNil())
					g.Expect(recoveredCond.Status).To(gomega.Equal(metav1.ConditionTrue))

					evictedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
					g.Expect(evictedCond).ShouldNot(gomega.BeNil())
					g.Expect(evictedCond.Status).To(gomega.Equal(metav1.ConditionFalse))

					requeuedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadRequeued)
					g.Expect(requeuedCond).ShouldNot(gomega.BeNil())
					g.Expect(requeuedCond.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("WaitForPodsReady has default Timeout and a long RecoveryTimeout", func() {
		ginkgo.BeforeEach(func() {
			cfg := defaultKueueCfg.DeepCopy()
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Enable:          true,
				BlockAdmission:  ptr.To(true),
				RecoveryTimeout: &metav1.Duration{Duration: util.LongTimeout},
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(1)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}

			util.ApplyKueueConfiguration(ctx, k8sClient, cfg)
			util.RestartKueueController(ctx, k8sClient)
		})

		ginkgo.It("should continue running workload if pod recovers before recoveryTimeout", func() {
			ginkgo.By("creating a job", func() {
				job = testingjob.MakeJob("job-recovery-timeout", ns.Name).
					Image(util.E2eTestAgnHostImage, util.BehaviorWaitForDeletion).
					Queue(lq.Name).
					Request(corev1.ResourceCPU, "2").
					Parallelism(1).
					BackoffLimitPerIndex(2).
					CompletionMode(batchv1.IndexedCompletion).
					Completions(1).
					Obj()

				gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())
			})

			wlKey = types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("checking workload availability", func() {
				workload := &kueue.Workload{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
					cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(cond).ShouldNot(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("simulating pod failure", func() {
				util.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 1, 1)
			})

			ginkgo.By("verifying the pod is recovered before recoveryTimeout", func() {
				workload := &kueue.Workload{}

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())

					recoveredCond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
					g.Expect(recoveredCond).ShouldNot(gomega.BeNil())
					g.Expect(recoveredCond.Status).To(gomega.Equal(metav1.ConditionTrue))
					g.Expect(recoveredCond.Reason).To(gomega.Equal(kueue.WorkloadRecovered))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
