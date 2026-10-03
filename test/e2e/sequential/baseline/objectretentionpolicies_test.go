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
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("ObjectRetentionPolicies", ginkgo.Label("feature:objectretentionpolicies", behavioral.Shard1), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "orp-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, rf, true, behavioral.MediumTimeout)
	})

	ginkgo.It("should delete the Workload after enabling the ObjectRetentionPolicies feature gate", func() {
		waitForPodsReady := &configapi.WaitForPodsReady{
			BlockAdmission:  new(true),
			Timeout:         metav1.Duration{Duration: behavioral.TinyTimeout},
			RecoveryTimeout: nil,
			RequeuingStrategy: &configapi.RequeuingStrategy{
				Timestamp:          new(configapi.EvictionTimestamp),
				BackoffBaseSeconds: new(int32(1)),
				BackoffLimitCount:  new(int32(1)),
			},
		}

		behavioral.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.FeatureGates = nil
			cfg.ObjectRetentionPolicies = nil
			cfg.WaitForPodsReady = waitForPodsReady.DeepCopy()
		})

		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			RequestAndLimit(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("Creating a Job", func() {
			behavioral.MustCreate(ctx, k8sClient, job)
		})

		wlKey := types.NamespacedName{
			Namespace: job.Namespace,
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
		}
		wl := &kueue.Workload{}

		ginkgo.By("Waiting for the Workload to be deactivated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(wl.Spec.Active).To(gomega.Equal(new(false)))
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Enable ObjectRetentionPolicies feature gate", func() {
			behavioral.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
				cfg.ObjectRetentionPolicies = &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterDeactivatedByKueue: &metav1.Duration{Duration: behavioral.TinyTimeout},
					},
				}
				cfg.WaitForPodsReady = waitForPodsReady.DeepCopy()
			})
		})

		ginkgo.By("Checking that the Job is deleted", func() {
			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(utiltesting.BeNotFoundError())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("Checking that the Workload is deleted", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(utiltesting.BeNotFoundError())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("ObjectRetentionPolicies with TinyTimeout", ginkgo.Label(behavioral.Shard1), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		behavioral.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.ObjectRetentionPolicies = &configapi.ObjectRetentionPolicies{
				Workloads: &configapi.WorkloadRetentionPolicy{
					AfterFinished:           &metav1.Duration{Duration: behavioral.TinyTimeout},
					AfterDeactivatedByKueue: &metav1.Duration{Duration: behavioral.TinyTimeout},
				},
			}
		})
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "orp-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("workload has finished", func() {
		ginkgo.It("should delete the Workload", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Queue(kueue.LocalQueueName(lq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("Creating a Job", func() {
				behavioral.MustCreate(ctx, k8sClient, job)
			})

			wlKey := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}

			ginkgo.By("Waiting for the Workload to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Terminating pods to make Workload finished", func() {
				behavioral.WaitForActivePodsAndTerminate(ctx, k8sClient, restClient, cfg, ns.Name, 1, 0)
			})

			ginkgo.By("Checking that the Workload is deleted after it is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(utiltesting.BeNotFoundError())
				}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Job is not deleted", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("manually deactivating a Workload", func() {
		ginkgo.It("shouldn't delete the Job or the Workload", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
				TerminationGracePeriod(1).
				Queue(kueue.LocalQueueName(lq.Name)).
				RequestAndLimit(corev1.ResourceCPU, "1").
				Obj()
			ginkgo.By("Creating a Job", func() {
				behavioral.MustCreate(ctx, k8sClient, job)
			})

			wlKey := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}

			ginkgo.By("Waiting for the Workload to be created", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Deactivating the Workload", func() {
				behavioral.DeactivateWorkload(ctx, k8sClient, wlKey)
			})

			ginkgo.By("Waiting for the Workload to be deactivated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Job is not deleted", func() {
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job), createdJob)).To(gomega.Succeed())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking that the Workload is not deleted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("ObjectRetentionPolicies with TinyTimeout and RequeuingLimitExceeded", ginkgo.Label(behavioral.Shard1), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
		cq *kueue.ClusterQueue
		lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		behavioral.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.ObjectRetentionPolicies = &configapi.ObjectRetentionPolicies{
				Workloads: &configapi.WorkloadRetentionPolicy{
					AfterDeactivatedByKueue: &metav1.Duration{Duration: behavioral.TinyTimeout},
				},
			}
			cfg.WaitForPodsReady = &configapi.WaitForPodsReady{
				BlockAdmission:  new(true),
				Timeout:         metav1.Duration{Duration: behavioral.TinyTimeout},
				RecoveryTimeout: nil,
				RequeuingStrategy: &configapi.RequeuingStrategy{
					Timestamp:          new(configapi.EvictionTimestamp),
					BackoffBaseSeconds: new(int32(1)),
					BackoffLimitCount:  new(int32(1)),
				},
			}
		})
	})

	ginkgo.JustBeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "orp-")

		rf = utiltestingapi.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, rf)).Should(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.It("should delete Job", func() {
		job := testingjob.MakeJob("job", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Image(behavioral.GetAgnHostImage(), behavioral.BehaviorWaitForDeletion).
			TerminationGracePeriod(1).
			RequestAndLimit(corev1.ResourceCPU, "1").
			Obj()
		ginkgo.By("Creating a Job", func() {
			behavioral.MustCreate(ctx, k8sClient, job)
		})

		ginkgo.By("Checking that the Job is deleted", func() {
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, job, false, behavioral.MediumTimeout)
		})

		ginkgo.By("Checking that the Workload is deleted", func() {
			wlKey := types.NamespacedName{
				Namespace: job.Namespace,
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			}
			wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(utiltesting.BeNotFoundError())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
