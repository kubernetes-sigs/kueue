/*
Copyright 2023 The Kubernetes Authors.

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

package multikueue

import (
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Multikueue", func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultikueueSecret1 *corev1.Secret
		managerMultikueueSecret2 *corev1.Secret
		workerCluster1           *kueuealpha.MultiKueueCluster
		workerCluster2           *kueuealpha.MultiKueueCluster
		managerMultiKueueConfig  *kueuealpha.MultiKueueConfig
		multikueueAC             *kueue.AdmissionCheck
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)
	ginkgo.BeforeEach(func() {
		managerNs = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "multikueue-",
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerNs)).To(gomega.Succeed())

		worker1Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Ns)).To(gomega.Succeed())

		worker2Ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: managerNs.Name,
			},
		}
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Ns)).To(gomega.Succeed())

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultikueueSecret1 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue1",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueuealpha.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultikueueSecret1)).To(gomega.Succeed())

		managerMultikueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueuealpha.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultikueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueuealpha.SecretLocationType, managerMultikueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueuealpha.SecretLocationType, managerMultikueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multikueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(multikueue.ControllerName).
			Parameters(kueuealpha.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multikueueAC)).Should(gomega.Succeed())

		ginkgo.By("wait for check active", func() {
			updatedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multikueueAC)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(updatedAc.Status.Conditions, kueue.AdmissionCheckActive)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionTrue), "Reason: %s, Message: %q", cond.Reason, cond.Message)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		managerCq = utiltesting.MakeClusterQueue("q1").
			AdmissionChecks(multikueueAC.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerCq)).Should(gomega.Succeed())

		managerLq = utiltesting.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerLq)).Should(gomega.Succeed())

		worker1Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Cq)).Should(gomega.Succeed())
		worker1Lq = utiltesting.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		gomega.Expect(worker1TestCluster.client.Create(worker1TestCluster.ctx, worker1Lq)).Should(gomega.Succeed())

		worker2Cq = utiltesting.MakeClusterQueue("q1").Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Cq)).Should(gomega.Succeed())
		worker2Lq = utiltesting.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		gomega.Expect(worker2TestCluster.client.Create(worker2TestCluster.ctx, worker2Lq)).Should(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, managerNs)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker1TestCluster.ctx, worker1TestCluster.client, worker1Ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(worker2TestCluster.ctx, worker2TestCluster.client, worker2Ns)).To(gomega.Succeed())
		util.ExpectClusterQueueToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectClusterQueueToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectClusterQueueToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
		util.ExpectAdmissionCheckToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multikueueAC, true)
		gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, managerMultiKueueConfig)).To(gomega.Succeed())
		gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())
		gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())
		gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, managerMultikueueSecret1)).To(gomega.Succeed())
		gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, managerMultikueueSecret2)).To(gomega.Succeed())
	})
	ginkgo.It("Should properly manage the active condition of AdmissionChecks and MultiKueueClusters", func() {
		ac := utiltesting.MakeAdmissionCheck("testing-ac").
			ControllerName(multikueue.ControllerName).
			Parameters(kueuealpha.GroupVersion.Group, "MultiKueueConfig", "testing-config").
			Obj()
		ginkgo.By("creating the admission check with missing config, it's set inactive", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, ac)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, ac) })

			ginkgo.By("wait for the check's active state update", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(ac)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(gomega.ContainElements(
						gomega.BeComparableTo(metav1.Condition{
							Type:    kueue.AdmissionCheckActive,
							Status:  metav1.ConditionFalse,
							Reason:  "BadConfig",
							Message: `Cannot load the AdmissionChecks parameters: MultiKueueConfig.kueue.x-k8s.io "testing-config" not found`,
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
						gomega.BeComparableTo(metav1.Condition{
							Type:    kueue.AdmissionChecksSingleInstanceInClusterQueue,
							Status:  metav1.ConditionTrue,
							Reason:  multikueue.SingleInstanceReason,
							Message: multikueue.SingleInstanceMessage,
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.By("creating a config with duplicate clusters should fail", func() {
			badConfig := utiltesting.MakeMultiKueueConfig("bad-config").Clusters("c1", "c2", "c1").Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, badConfig).Error()).Should(gomega.Equal(
				`MultiKueueConfig.kueue.x-k8s.io "bad-config" is invalid: spec.clusters[2]: Duplicate value: "c1"`))
		})

		config := utiltesting.MakeMultiKueueConfig("testing-config").Clusters("testing-cluster").Obj()
		ginkgo.By("creating the config, the admission check's state is updated", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, config)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, config) })

			ginkgo.By("wait for the check's active state update", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(ac)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionFalse,
						Reason:  "NoUsableClusters",
						Message: `Missing clusters: [testing-cluster]`,
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		cluster := utiltesting.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueuealpha.SecretLocationType, "testing-secret").Obj()
		ginkgo.By("creating the cluster, its Active state is updated, the admission check's state is updated", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cluster)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, cluster) })

			ginkgo.By("wait for the cluster's active state update", func() {
				updatedCluster := kueuealpha.MultiKueueCluster{}
				clusterKey := client.ObjectKeyFromObject(cluster)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueuealpha.MultiKueueClusterActive,
						Status:  metav1.ConditionFalse,
						Reason:  "BadConfig",
						Message: `Secret "testing-secret" not found`,
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the check's active state update", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(ac)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionFalse,
						Reason:  "NoUsableClusters",
						Message: `Inactive clusters: [testing-cluster]`,
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "testing-secret",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueuealpha.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}

		ginkgo.By("creating the secret, the cluster and admission check become active", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, secret)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, secret) })

			ginkgo.By("wait for the cluster's active state update", func() {
				updatedCluster := kueuealpha.MultiKueueCluster{}
				clusterKey := client.ObjectKeyFromObject(cluster)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueuealpha.MultiKueueClusterActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Active",
						Message: "Connected",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("wait for the check's active state update", func() {
				updatedAc := kueue.AdmissionCheck{}
				acKey := client.ObjectKeyFromObject(ac)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
					g.Expect(updatedAc.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Active",
						Message: "The admission check is active",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.It("Should run a job on worker if admitted", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(managerLq.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, AC state is updated in manager and worker2 wl is removed", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, multikueueAC.Name)
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStatePending))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker1"`))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker job", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions, batchv1.JobCondition{
					Type:               batchv1.JobComplete,
					Status:             corev1.ConditionTrue,
					LastProbeTime:      metav1.Now(),
					LastTransitionTime: metav1.Now(),
				})
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "JobFinished",
					Message: `Job finished successfully`,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		})
	})

	ginkgo.It("Should run a jobSet on worker if admitted", func() {
		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(multikueue.ControllerName).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}, testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, jobSet)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "replicated-job-1",
			}, kueue.PodSetAssignment{
				Name: "replicated-job-2",
			},
		).Obj()

		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker2, the workload is admitted in manager amd worker1 wl is removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, multikueueAC.Name)
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker2"`))

				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The workload is admitted",
				}, util.IgnoreConditionTimestampsAndObservedGeneration))

			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("changing the status of the jobset in the worker, updates the manager's jobset status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				createdJobSet.Status.Restarts = 10
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				g.Expect(createdJobSet.Status.Restarts).To(gomega.Equal(int32(10)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker jobSet, the manager's wl is marked as finished and the worker2 wl removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdJobSet.Status.Conditions, metav1.Condition{
					Type:    string(jobset.JobSetCompleted),
					Status:  metav1.ConditionTrue,
					Reason:  "ByTest",
					Message: "by test",
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())

				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "JobSetFinished",
					Message: `JobSet finished successfully`,
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

		})
	})

	ginkgo.It("Should remove the worker's workload and job when managers job is deleted", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(managerLq.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

		ginkgo.By("setting workload reservation in the management cluster", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, the job is created in worker1", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("removing the managers job and workload, the workload and job in worker1 are removed", func() {
			gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, createdWorkload)).To(gomega.Succeed())

			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should requeue the workload with a delay when the connection to the admitting worker is lost", func() {
		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(multikueue.ControllerName).
			ReplicatedJobs(
				testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-1",
					Replicas:    1,
					Parallelism: 1,
					Completions: 1,
				}, testingjobset.ReplicatedJobRequirements{
					Name:        "replicated-job-2",
					Replicas:    3,
					Parallelism: 1,
					Completions: 1,
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, jobSet)).Should(gomega.Succeed())

		createdWorkload := &kueue.Workload{}
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "replicated-job-1",
			}, kueue.PodSetAssignment{
				Name: "replicated-job-2",
			},
		).Obj()

		ginkgo.By("setting workload reservation in the management cluster", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the workload creation in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker2, the workload is admitted in manager and worker1 wl is removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, multikueueAC.Name)
				g.Expect(acs).NotTo(gomega.BeNil())
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker2"`))

				g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeComparableTo(&metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionTrue,
					Reason:  "Admitted",
					Message: "The workload is admitted",
				}, util.IgnoreConditionTimestampsAndObservedGeneration))

			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		var disconnectedTime time.Time
		ginkgo.By("breaking the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueuealpha.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueuealpha.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueuealpha.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueuealpha.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				disconnectedTime = activeCondition.LastTransitionTime.Time
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the local workload admission check state to be set to pending and quotaReservatio removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, multikueueAC.Name)
				g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:  multikueueAC.Name,
					State: kueue.CheckStatePending,
				}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))

				// The transition interval should be close to testingKeepReadyTimeout (taking into account the resolution of the LastTransitionTime field)
				g.Expect(acs.LastTransitionTime.Time).To(gomega.BeComparableTo(disconnectedTime.Add(testingWorkerLostTimeout), cmpopts.EquateApproxTime(2*time.Second)))

				g.Expect(apimeta.IsStatusConditionTrue(createdWorkload.Status.Conditions, kueue.WorkloadQuotaReserved)).To(gomega.BeFalse())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restoring the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueuealpha.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultikueueSecret2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueuealpha.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueuealpha.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueuealpha.MultiKueueClusterActive,
					Status: metav1.ConditionTrue,
					Reason: "Active",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				disconnectedTime = activeCondition.LastTransitionTime.Time
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker2 wl is removed since the local one no longer has a reservation", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
