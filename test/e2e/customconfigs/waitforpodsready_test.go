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
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingjobspod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	serviceAccountName           = "kueue-controller-manager"
	metricsReaderClusterRoleName = "kueue-metrics-reader"
)

var _ = ginkgo.Describe("WaitForPodsReady with tiny Timeout and no RecoveryTimeout", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns    *corev1.Namespace
		rf    *kueue.ResourceFlavor
		cq    *kueue.ClusterQueue
		lq    *kueue.LocalQueue
		job   *batchv1.Job
		wl    kueue.Workload
		wlKey types.NamespacedName

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	ginkgo.BeforeAll(func() {
		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader-rolebinding"},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      serviceAccountName,
					Namespace: kueueNS,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     metricsReaderClusterRoleName,
			},
		}
		util.MustCreate(ctx, k8sClient, metricsReaderClusterRoleBinding)

		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				BlockAdmission:  ptr.To(true),
				Timeout:         metav1.Duration{Duration: util.TinyTimeout},
				RecoveryTimeout: nil,
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(10)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}
		})

		curlPod = testingjobspod.MakePod("curl-metrics", configapi.DefaultNamespace).
			ServiceAccountName(serviceAccountName).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, curlPod)

		ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
			util.WaitForPodRunning(ctx, k8sClient, curlPod)
		})

		curlContainerName = curlPod.Spec.Containers[0].Name
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wfpr-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.LongTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
	})

	ginkgo.It("should evict and requeue workload when pods readiness timeout is surpassed", func() {
		ginkgo.By("creating a suspended job so its pods never report Ready", func() {
			job = testingjob.MakeJob("job-timeout", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Request(corev1.ResourceCPU, "2").
				Parallelism(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job)
		})

		wlKey = types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("waiting for the workload to be created and verifying it is not admitted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Admission).To(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the workload to be evicted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(kueue.WorkloadPodsReady, kueue.WorkloadWaitForStart))
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadEvicted, kueue.WorkloadEvictedByPodsReadyTimeout))
				g.Expect(wl.Status.SchedulingStats.Evictions).To(
					gomega.BeComparableTo([]kueue.WorkloadSchedulingStatsEviction{{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					}}),
				)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the metric is updated", func() {
			util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, [][]string{
				{"kueue_evicted_workloads_once_total", cq.Name, kueue.WorkloadEvictedByPodsReadyTimeout, kueue.WorkloadWaitForStart, "1"},
			})
		})

		ginkgo.By("verifying that the job is suspended", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), job)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(job.Spec.Suspend, false)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the workload is requeued", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
				g.Expect(*wl.Status.RequeueState.Count).To(gomega.Equal(int32(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the workload is deactivated after the second eviction", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(wl.Spec.Active, true)).Should(gomega.BeFalse())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("WaitForPodsReady with default Timeout and a tiny RecoveryTimeout", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns    *corev1.Namespace
		rf    *kueue.ResourceFlavor
		cq    *kueue.ClusterQueue
		lq    *kueue.LocalQueue
		job   *batchv1.Job
		wl    kueue.Workload
		wlKey types.NamespacedName

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	ginkgo.BeforeAll(func() {
		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader-rolebinding"},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      serviceAccountName,
					Namespace: configapi.DefaultNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     metricsReaderClusterRoleName,
			},
		}
		util.MustCreate(ctx, k8sClient, metricsReaderClusterRoleBinding)

		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Timeout:         metav1.Duration{Duration: 5 * time.Minute},
				BlockAdmission:  ptr.To(true),
				RecoveryTimeout: &metav1.Duration{Duration: util.TinyTimeout},
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(1)),
					// Allow at least one requeue cycle before deactivation so the test
					// can verify the requeue behavior.
					BackoffLimitCount: ptr.To(int32(2)),
				},
			}
		})

		curlPod = testingjobspod.MakePod("curl-metrics", configapi.DefaultNamespace).
			ServiceAccountName(serviceAccountName).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, curlPod)

		ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
			util.WaitForPodRunning(ctx, k8sClient, curlPod)
		})

		curlContainerName = curlPod.Spec.Containers[0].Name
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wfpr-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.LongTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
	})

	ginkgo.It("should evict and requeue workload when pod failure causes recovery timeout", func() {
		ginkgo.By("creating a job", func() {
			job = testingjob.MakeJob("job-recovery-timeout", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "2").
				Parallelism(1).
				BackoffLimitPerIndex(2).
				CompletionMode(batchv1.IndexedCompletion).
				Completions(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job)
		})

		wlKey = types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("checking workload availability", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("simulating pod failure", func() {
			util.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 1, 1)
		})

		ginkgo.By("verifying that the workload is requeued", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.RequeueState).ShouldNot(gomega.BeNil())
				// With a tiny RecoveryTimeout (10ms), multiple evictions may occur
				// before pods become Ready, so we check for at least 1 eviction.
				g.Expect(*wl.Status.RequeueState.Count).To(gomega.BeNumerically(">=", int32(1)))
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadPodsReady, kueue.WorkloadStarted))
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadEvicted))
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadRequeued))

				g.Expect(wl.Status.SchedulingStats.Evictions).To(gomega.HaveLen(1))
				g.Expect(wl.Status.SchedulingStats.Evictions[0].Reason).To(gomega.Equal(kueue.WorkloadEvictedByPodsReadyTimeout))
				g.Expect(string(wl.Status.SchedulingStats.Evictions[0].UnderlyingCause)).To(gomega.Equal(kueue.WorkloadWaitForRecovery))
				g.Expect(wl.Status.SchedulingStats.Evictions[0].Count).To(gomega.BeNumerically(">=", int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the metric is updated", func() {
			util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, [][]string{
				{"kueue_evicted_workloads_once_total", cq.Name, kueue.WorkloadEvictedByPodsReadyTimeout, kueue.WorkloadWaitForRecovery, "1"},
			})
		})
	})
})

var _ = ginkgo.Describe("WaitForPodsReady with default Timeout and a long RecoveryTimeout", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns    *corev1.Namespace
		rf    *kueue.ResourceFlavor
		cq    *kueue.ClusterQueue
		lq    *kueue.LocalQueue
		job   *batchv1.Job
		wl    kueue.Workload
		wlKey types.NamespacedName

		metricsReaderClusterRoleBinding *rbacv1.ClusterRoleBinding

		curlContainerName string
		curlPod           *corev1.Pod
	)

	ginkgo.BeforeAll(func() {
		metricsReaderClusterRoleBinding = &rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "metrics-reader-rolebinding"},
			Subjects: []rbacv1.Subject{
				{
					Kind:      "ServiceAccount",
					Name:      serviceAccountName,
					Namespace: configapi.DefaultNamespace,
				},
			},
			RoleRef: rbacv1.RoleRef{
				APIGroup: rbacv1.GroupName,
				Kind:     "ClusterRole",
				Name:     metricsReaderClusterRoleName,
			},
		}
		util.MustCreate(ctx, k8sClient, metricsReaderClusterRoleBinding)

		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Timeout:         metav1.Duration{Duration: 5 * time.Minute},
				BlockAdmission:  ptr.To(true),
				RecoveryTimeout: &metav1.Duration{Duration: util.LongTimeout},
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(1)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}
		})

		curlPod = testingjobspod.MakePod("curl-metrics", configapi.DefaultNamespace).
			ServiceAccountName(serviceAccountName).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			Obj()
		util.MustCreate(ctx, k8sClient, curlPod)

		ginkgo.By("Waiting for the curl-metrics pod to run.", func() {
			util.WaitForPodRunning(ctx, k8sClient, curlPod)
		})

		curlContainerName = curlPod.Spec.Containers[0].Name
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wfpr-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, curlPod, true, util.LongTimeout)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, metricsReaderClusterRoleBinding, true)
	})

	ginkgo.It("should continue running workload if pod recovers before recoveryTimeout", func() {
		ginkgo.By("creating a job", func() {
			job = testingjob.MakeJob("job-recovery-timeout", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "2").
				Parallelism(1).
				BackoffLimitPerIndex(2).
				CompletionMode(batchv1.IndexedCompletion).
				Completions(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job)
		})

		wlKey = types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("checking workload availability", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the metric is updated", func() {
			util.ExpectMetricsToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, [][]string{
				{"kueue_ready_wait_time_seconds_count", cq.Name, ""},
				{"kueue_admitted_until_ready_wait_time_seconds_count", cq.Name, ""},
				{"kueue_local_queue_ready_wait_time_seconds", ns.Name, lq.Name, ""},
				{"kueue_local_queue_admitted_until_ready_wait_time_seconds", ns.Name, lq.Name, ""}})
		})

		ginkgo.By("simulating pod failure", func() {
			util.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 1, 1)
		})

		ginkgo.By("verifying the pod is recovered before recoveryTimeout", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadPodsReady, kueue.WorkloadRecovered))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying that the metric is not updated", func() {
			util.ExpectMetricsNotToBeAvailable(ctx, cfg, restClient, curlPod.Name, curlContainerName, [][]string{
				{"kueue_evicted_workloads_once_total", ns.Name},
			})
		})
	})
})

var _ = ginkgo.Describe("WaitForPodsReady with for JobSet", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{Timeout: metav1.Duration{Duration: util.LongTimeout}}
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wfpr-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, rf)

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.It("should update podReady condition correctly as jobs run", func() {
		jobSet := testingjobset.MakeJobSet("job-set", ns.Name).
			Queue(lq.Name).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Image:       util.GetAgnHostImage(),
					Args:        util.BehaviorWaitForDeletion,
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				},
				testingjobset.ReplicatedJobRequirements{
					Name:  "replicated-job-2",
					Image: util.GetAgnHostImage(),
					Args:  util.BehaviorWaitForDeletion,
					StartupProbe: &corev1.Probe{
						InitialDelaySeconds: 5,
						ProbeHandler: corev1.ProbeHandler{
							Exec: &corev1.ExecAction{
								Command: []string{"/bin/sh", "-c", "echo 'Hello, World!'"},
							},
						},
					},
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
					DependsOn: []jobsetapi.DependsOn{
						{
							Name:   "replicated-job-1",
							Status: jobsetapi.DependencyReady,
						},
					},
				},
			).
			Obj()

		ginkgo.By("Creating the jobSet", func() {
			util.MustCreate(ctx, k8sClient, jobSet)
		})

		ginkgo.By("Waiting for podready condition to be true", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
				g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady))
			}, util.LongTimeout, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for podready condition to be false due to initialDelaySeconds on second job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
				// check that the workload is not ready as the initialDelaySeconds is set on the second job
				g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusFalse(kueue.WorkloadPodsReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for the second job to be ready", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				jobList := &batchv1.JobList{}
				g.Expect(k8sClient.List(ctx, jobList, client.InNamespace(ns.Name), client.MatchingLabels{jobsetapi.ReplicatedJobNameKey: "replicated-job-2"})).To(gomega.Succeed())
				g.Expect(jobList.Items).To(gomega.HaveLen(1))
				g.Expect(jobList.Items[0].Status).ToNot(gomega.BeNil())
				g.Expect(*jobList.Items[0].Status.Ready).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Waiting for podready condition to be true", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdLeaderWorkload := &kueue.Workload{}
				wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: ns.Name}
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdLeaderWorkload)).To(gomega.Succeed())
				// check that the workload is ready when initialDelaySeconds has passed
				g.Expect(createdLeaderWorkload.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadPodsReady))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
