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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("WaitForPodsReady Job Controller E2E", func() {
	var (
		ns          *corev1.Namespace
		rf          *kueue.ResourceFlavor
		cq          *kueue.ClusterQueue
		lq          *kueue.LocalQueue
		job         *batchv1.Job
		wlKey       types.NamespacedName
		originalCfg *configapi.Configuration
	)

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "wfr-"}}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		rf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())

		originalCfg = util.GetKueueConfiguration(ctx, k8sClient)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)

		util.ApplyKueueConfiguration(ctx, k8sClient, originalCfg)
		util.RestartKueueController(ctx, k8sClient)
	})

	ginkgo.It("should evict and requeue workload when pods readiness timeout is surpassed", func() {
		ginkgo.By("configuring WaitForPodsReady with a very short Timeout and no RecoveryTimeout", func() {
			cfg := originalCfg.DeepCopy()
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
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

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
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, updatedJob)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(updatedJob.Spec.Suspend, false)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the workload is requeued", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var wl kueue.Workload
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
				g.Expect(*wl.Status.RequeueState.Count).Should(gomega.Equal(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should evict and requeue workload when pod failure causes recovery timeout", func() {
		ginkgo.By("configuring WaitForPodsReady with a long Timeout and a short RecoveryTimeout", func() {
			cfg := originalCfg.DeepCopy()
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Enable:          true,
				BlockAdmission:  ptr.To(true),
				Timeout:         &metav1.Duration{Duration: 5 * time.Minute},
				RecoveryTimeout: &metav1.Duration{Duration: util.Timeout},
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(10)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}
			util.ApplyKueueConfiguration(ctx, k8sClient, cfg)
			util.RestartKueueController(ctx, k8sClient)
		})

		ginkgo.By("creating a job", func() {
			job = testingjob.MakeJob("job-recovery-timeout", ns.Name).
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
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		var updatedJob *batchv1.Job
		jobKey := types.NamespacedName{Name: job.Name, Namespace: ns.Name}

		ginkgo.By("simulating the job reporting that its pod is ready", func() {
			gomega.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
			updatedJob = job.DeepCopy()

			updatedJob.Status.Active = 1
			updatedJob.Status.Ready = ptr.To[int32](1)
			gomega.Expect(k8sClient.Status().Update(ctx, updatedJob)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("simulating a pod failure", func() {
			gomega.Expect(k8sClient.Get(ctx, jobKey, job)).Should(gomega.Succeed())
			updatedJob = job.DeepCopy()

			updatedJob.Status.Active = 0
			updatedJob.Status.Ready = ptr.To[int32](0)
			updatedJob.Status.Failed = 1
			gomega.Expect(k8sClient.Status().Update(ctx, updatedJob)).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for pod eviction", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
				evictedCond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(evictedCond).ShouldNot(gomega.BeNil())
				g.Expect(evictedCond.Status).To(gomega.Equal(metav1.ConditionTrue))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the job is suspended", func() {
			updatedJob = &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, updatedJob)).Should(gomega.Succeed())
			gomega.Expect(ptr.Deref(updatedJob.Spec.Suspend, false)).To(gomega.BeTrue())
		})

		ginkgo.By("verifying that the workload is requeued", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var wl kueue.Workload
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
				g.Expect(*wl.Status.RequeueState.Count).Should(gomega.Equal(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should continue running workload if pod recovers before recoveryTimeout", func() {
		ginkgo.By("configuring WaitForPodsReady with a long Timeout and RecoveryTimeout", func() {
			cfg := originalCfg.DeepCopy()
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Enable:          true,
				BlockAdmission:  ptr.To(true),
				Timeout:         &metav1.Duration{Duration: 5 * time.Minute},
				RecoveryTimeout: &metav1.Duration{Duration: util.LongTimeout},
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(10)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}
			util.ApplyKueueConfiguration(ctx, k8sClient, cfg)
			util.RestartKueueController(ctx, k8sClient)
		})

		ginkgo.By("creating a job", func() {
			job = testingjob.MakeJob("job-recovery-success", ns.Name).
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
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("simulating the initial pod readiness", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var latestJob batchv1.Job

				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, &latestJob)).Should(gomega.Succeed())

				latestJob.Status.Active = 1
				latestJob.Status.Ready = ptr.To[int32](1)
				g.Expect(k8sClient.Status().Update(ctx, &latestJob)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadStarted))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("simulating a pod failure", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var latestJob batchv1.Job

				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, &latestJob)).Should(gomega.Succeed())

				latestJob.Status.Active = 0
				latestJob.Status.Ready = ptr.To[int32](0)
				latestJob.Status.Failed = 1

				g.Expect(k8sClient.Status().Update(ctx, &latestJob)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("simulating pod recovery", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var latestJob batchv1.Job

				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, &latestJob)).Should(gomega.Succeed())

				latestJob.Status.Active = 1
				latestJob.Status.Ready = ptr.To[int32](1)
				latestJob.Status.Succeeded = 1

				g.Expect(k8sClient.Status().Update(ctx, &latestJob)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying the pod is recovered", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, workload)).Should(gomega.Succeed())
				cond := apimeta.FindStatusCondition(workload.Status.Conditions, kueue.WorkloadPodsReady)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadRecovered))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			var latestJob batchv1.Job
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: ns.Name}, &latestJob)).Should(gomega.Succeed())
			gomega.Expect(ptr.Deref(latestJob.Spec.Suspend, false)).To(gomega.BeFalse())
		})
	})
})
