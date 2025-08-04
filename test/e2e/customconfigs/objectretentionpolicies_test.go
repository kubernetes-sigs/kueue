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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ObjectRetentionPolicies", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "orp-")

		rf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, util.LongTimeout)
		util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, util.LongTimeout)
	})

	ginkgo.It("should delete the Workload after enabling the ObjectRetentionPolicies feature gate", func() {
		waitForPodsReady := &configapi.WaitForPodsReady{
			Enable:          true,
			BlockAdmission:  ptr.To(true),
			Timeout:         &metav1.Duration{Duration: util.TinyTimeout},
			RecoveryTimeout: nil,
			RequeuingStrategy: &configapi.RequeuingStrategy{
				Timestamp:          ptr.To(configapi.EvictionTimestamp),
				BackoffBaseSeconds: ptr.To(int32(1)),
				BackoffLimitCount:  ptr.To(int32(1)),
			},
		}

		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = nil
			cfg.ObjectRetentionPolicies = nil
			cfg.WaitForPodsReady = waitForPodsReady.DeepCopy()
		})

		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("Creating a Job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		wlKey := types.NamespacedName{
			Namespace: job.Namespace,
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
		}
		wl := &kueue.Workload{}

		ginkgo.By("Waiting for the Workload to be deactivated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.Active).To(gomega.Equal(ptr.To(false)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Enable ObjectRetentionPolicies feature gate", func() {
			util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
				cfg.FeatureGates = map[string]bool{string(features.ObjectRetentionPolicies): true}
				cfg.ObjectRetentionPolicies = &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: util.TinyTimeout},
					},
				}
				cfg.WaitForPodsReady = waitForPodsReady.DeepCopy()
			})
		})

		ginkgo.By("Checking that the Job is deleted", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(testing.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the Workload is deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(testing.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("ObjectRetentionPolicies with TinyTimeout", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{string(features.ObjectRetentionPolicies): true}
			cfg.ObjectRetentionPolicies = &configapi.ObjectRetentionPolicies{
				Workloads: &configapi.WorkloadRetentionPolicy{
					AfterFinished:           &metav1.Duration{Duration: util.TinyTimeout},
					AfterDeactivatedByKueue: &metav1.Duration{Duration: util.TinyTimeout},
				},
			}
		})
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "orp-")

		rf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("workload has finished", func() {
		ginkgo.It("should delete the Workload", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorExitFast).
				Queue(kueue.LocalQueueName(lq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("Creating a Job", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			wlKey := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}

			ginkgo.By("Waiting for the Workload to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Workload is deleted after it is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(testing.BeNotFoundError())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Job is not deleted", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("manually deactivating a Workload", func() {
		ginkgo.It("shouldn't delete the Job or the Workload", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				Queue(kueue.LocalQueueName(lq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("Creating a Job", func() {
				util.MustCreate(ctx, k8sClient, job)
			})

			wlKey := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}

			ginkgo.By("Waiting for the Workload to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deactivating the Workload", func() {
				util.DeactivateWorkload(ctx, k8sClient, wlKey)
			})

			ginkgo.By("Waiting for the Workload to be deactivated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Job is not deleted", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Workload is not deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("ObjectRetentionPolicies with TinyTimeout and RequeuingLimitExceeded", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfiguration(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = map[string]bool{string(features.ObjectRetentionPolicies): true}
			cfg.ObjectRetentionPolicies = &configapi.ObjectRetentionPolicies{
				Workloads: &configapi.WorkloadRetentionPolicy{
					AfterDeactivatedByKueue: &metav1.Duration{Duration: util.TinyTimeout},
				},
			}
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				Enable:          true,
				BlockAdmission:  ptr.To(true),
				Timeout:         &metav1.Duration{Duration: util.TinyTimeout},
				RecoveryTimeout: nil,
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          ptr.To(configapi.EvictionTimestamp),
					BackoffBaseSeconds: ptr.To(int32(1)),
					BackoffLimitCount:  ptr.To(int32(1)),
				},
			}
		})
	})

	ginkgo.JustBeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "orp-")

		rf = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = testing.MakeClusterQueue("cq").
			ResourceGroup(*testing.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).Should(gomega.Succeed())

		lq = testing.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).Should(gomega.Succeed())
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.It("should delete Job", func() {
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			RequestAndLimit(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("Creating a Job", func() {
			util.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("Checking that the Job is deleted", func() {
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, job, false, util.LongTimeout)
		})

		ginkgo.By("Checking that the Workload is deleted", func() {
			wlKey := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(testing.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
