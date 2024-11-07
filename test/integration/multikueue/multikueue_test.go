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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	kftraining "github.com/kubeflow/training-operator/pkg/apis/kubeflow.org/v1"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	jobset "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadtfjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/tfjob"
	workloadxgboostjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/xgboostjob"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingjobset "sigs.k8s.io/kueue/pkg/util/testingjobs/jobset"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	testingpaddlejob "sigs.k8s.io/kueue/pkg/util/testingjobs/paddlejob"
	testingpytorchjob "sigs.k8s.io/kueue/pkg/util/testingjobs/pytorchjob"
	testingtfjob "sigs.k8s.io/kueue/pkg/util/testingjobs/tfjob"
	testingxgboostjob "sigs.k8s.io/kueue/pkg/util/testingjobs/xgboostjob"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Multikueue", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultikueueSecret1 *corev1.Secret
		managerMultikueueSecret2 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		workerCluster2           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig
		multikueueAC             *kueue.AdmissionCheck
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

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
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultikueueSecret1)).To(gomega.Succeed())

		managerMultikueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultikueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultikueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultikueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multikueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
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
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multikueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultikueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultikueueSecret2, true)
	})
	ginkgo.It("Should properly manage the active condition of AdmissionChecks and MultiKueueClusters, kubeconfig provided by secret, AdmissionCheckValidationRules disabled", func() {
		ac := utiltesting.MakeAdmissionCheck("testing-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "testing-config").
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

		cluster := utiltesting.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.SecretLocationType, "testing-secret").Obj()
		ginkgo.By("creating the cluster, its Active state is updated, the admission check's state is updated", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cluster)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, cluster) })

			ginkgo.By("wait for the cluster's active state update", func() {
				updatedCluster := kueue.MultiKueueCluster{}
				clusterKey := client.ObjectKeyFromObject(cluster)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
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
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}

		ginkgo.By("creating the secret, the cluster and admission check become active", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, secret)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, secret) })

			ginkgo.By("wait for the cluster's active state update", func() {
				updatedCluster := kueue.MultiKueueCluster{}
				clusterKey := client.ObjectKeyFromObject(cluster)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
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

	ginkgo.It("Should properly manage the active condition of AdmissionChecks and MultiKueueClusters, kubeconfig provided by file", func() {
		ginkgo.By("Enabling AdmissionCheckValidationRules feature", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AdmissionCheckValidationRules, true)
		})
		ac := utiltesting.MakeAdmissionCheck("testing-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "testing-config").
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
						gomega.BeComparableTo(metav1.Condition{
							Type:    kueue.FlavorIndependentAdmissionCheck,
							Status:  metav1.ConditionTrue,
							Reason:  multikueue.FlavorIndependentCheckReason,
							Message: multikueue.FlavorIndependentCheckMessage,
						}, util.IgnoreConditionTimestampsAndObservedGeneration),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		tempDir := ginkgo.GinkgoT().TempDir()
		fsKubeConfig := filepath.Join(tempDir, "testing.kubeconfig")

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

		cluster := utiltesting.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.PathLocationType, fsKubeConfig).Obj()
		ginkgo.By("creating the cluster, its Active state is updated, the admission check's state is updated", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cluster)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, cluster) })

			ginkgo.By("wait for the cluster's active state update", func() {
				updatedCluster := kueue.MultiKueueCluster{}
				clusterKey := client.ObjectKeyFromObject(cluster)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
						Status:  metav1.ConditionFalse,
						Reason:  "BadConfig",
						Message: fmt.Sprintf("open %s: no such file or directory", fsKubeConfig),
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

		ginkgo.By("creating the kubeconfig file, the cluster and admission check become active", func() {
			gomega.Expect(os.WriteFile(fsKubeConfig, w1Kubeconfig, 0666)).Should(gomega.Succeed())
			ginkgo.By("wait for the cluster's active state update", func() {
				updatedCluster := kueue.MultiKueueCluster{}
				clusterKey := client.ObjectKeyFromObject(cluster)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
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
			reachedPodsReason := "Reached expected number of succeeded pods"
			finishJobReason := "Job finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Message:            reachedPodsReason,
					},
					batchv1.JobCondition{
						Type:               batchv1.JobComplete,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Message:            finishJobReason,
					},
				)
				createdJob.Status.Succeeded = 1
				createdJob.Status.StartTime = ptr.To(metav1.Now())
				createdJob.Status.CompletionTime = ptr.To(metav1.Now())
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a job on worker if admitted (ManagedBy)", func() {
		if managerK8sVersion.LessThan(versionutil.MustParseSemantic("1.30.0")) {
			ginkgo.Skip("the managers kubernetes version is less then 1.30")
		}
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueBatchJobWithManagedBy, true)
		job := testingjob.MakeJob("job", managerNs.Name).
			ManagedBy(kueue.MultiKueueControllerName).
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
				g.Expect(acs.State).To(gomega.Equal(kueue.CheckStateReady))
				g.Expect(acs.Message).To(gomega.Equal(`The workload got reservation on "worker1"`))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("updating the worker job status", func() {
			startTime := metav1.NewTime(time.Now().Truncate(time.Second))
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.StartTime = &startTime
				createdJob.Status.Active = 1
				createdJob.Status.Ready = ptr.To[int32](1)
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				g.Expect(ptr.Deref(createdJob.Status.StartTime, metav1.Time{})).To(gomega.Equal(startTime))
				g.Expect(ptr.Deref(createdJob.Status.Ready, 0)).To(gomega.Equal(int32(1)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker job", func() {
			reachedPodsReason := "Reached expected number of succeeded pods"
			finishJobReason := "Job finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
				createdJob.Status.Conditions = append(createdJob.Status.Conditions,
					batchv1.JobCondition{
						Type:               batchv1.JobSuccessCriteriaMet,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Message:            reachedPodsReason,
					},
					batchv1.JobCondition{
						Type:               batchv1.JobComplete,
						Status:             corev1.ConditionTrue,
						LastProbeTime:      metav1.Now(),
						LastTransitionTime: metav1.Now(),
						Message:            finishJobReason,
					},
				)
				createdJob.Status.Active = 0
				createdJob.Status.Ready = ptr.To[int32](0)
				createdJob.Status.Succeeded = 1
				createdJob.Status.CompletionTime = ptr.To(metav1.Now())
				g.Expect(worker1TestCluster.client.Status().Update(worker1TestCluster.ctx, &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a jobSet on worker if admitted", func() {
		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
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
		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "replicated-job-1",
			}, kueue.PodSetAssignment{
				Name: "replicated-job-2",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

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
			finishJobReason := "JobSet finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdJobSet := jobset.JobSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(jobSet), &createdJobSet)).To(gomega.Succeed())
				apimeta.SetStatusCondition(&createdJobSet.Status.Conditions, metav1.Condition{
					Type:    string(jobset.JobSetCompleted),
					Status:  metav1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdJobSet)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a TFJob on worker if admitted", framework.RedundantSpec, func() {
		tfJob := testingtfjob.MakeTFJob("tfjob1", managerNs.Name).
			Queue(managerLq.Name).
			TFReplicaSpecs(
				testingtfjob.TFReplicaSpecRequirement{
					ReplicaType:   kftraining.TFJobReplicaTypeChief,
					ReplicaCount:  1,
					Name:          "tfjob-chief",
					RestartPolicy: "OnFailure",
				},
				testingtfjob.TFReplicaSpecRequirement{
					ReplicaType:   kftraining.TFJobReplicaTypePS,
					ReplicaCount:  1,
					Name:          "tfjob-ps",
					RestartPolicy: "Never",
				},
				testingtfjob.TFReplicaSpecRequirement{
					ReplicaType:   kftraining.TFJobReplicaTypeWorker,
					ReplicaCount:  3,
					Name:          "tfjob-worker",
					RestartPolicy: "OnFailure",
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, tfJob)).Should(gomega.Succeed())
		wlLookupKey := types.NamespacedName{Name: workloadtfjob.GetWorkloadNameForTFJob(tfJob.Name, tfJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "chief",
			}, kueue.PodSetAssignment{
				Name: "ps",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the TFJob in the worker, updates the manager's TFJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdTfJob := kftraining.TFJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(tfJob), &createdTfJob)).To(gomega.Succeed())
				createdTfJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.TFJobReplicaTypeChief: {
						Active: 1,
					},
					kftraining.TFJobReplicaTypePS: {
						Active: 1,
					},
					kftraining.TFJobReplicaTypeWorker: {
						Active:    2,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTfJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdTfJob := kftraining.TFJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(tfJob), &createdTfJob)).To(gomega.Succeed())
				g.Expect(createdTfJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.TFJobReplicaTypeChief: {
							Active: 1,
						},
						kftraining.TFJobReplicaTypePS: {
							Active: 1,
						},
						kftraining.TFJobReplicaTypeWorker: {
							Active:    2,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker TFJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "TFJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdTfJob := kftraining.TFJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(tfJob), &createdTfJob)).To(gomega.Succeed())
				createdTfJob.Status.Conditions = append(createdTfJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdTfJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a PaddleJob on worker if admitted", framework.RedundantSpec, func() {
		paddleJob := testingpaddlejob.MakePaddleJob("paddlejob1", managerNs.Name).
			Queue(managerLq.Name).
			PaddleReplicaSpecs(
				testingpaddlejob.PaddleReplicaSpecRequirement{
					ReplicaType:   kftraining.PaddleJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "paddlejob-master",
					RestartPolicy: "OnFailure",
				},
				testingpaddlejob.PaddleReplicaSpecRequirement{
					ReplicaType:   kftraining.PaddleJobReplicaTypeWorker,
					ReplicaCount:  3,
					Name:          "paddlejob-worker",
					RestartPolicy: "OnFailure",
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, paddleJob)).Should(gomega.Succeed())

		wlLookupKey := types.NamespacedName{Name: workloadpaddlejob.GetWorkloadNameForPaddleJob(paddleJob.Name, paddleJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the PaddleJob in the worker, updates the manager's PaddleJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPaddleJob := kftraining.PaddleJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(paddleJob), &createdPaddleJob)).To(gomega.Succeed())
				createdPaddleJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.PaddleJobReplicaTypeMaster: {
						Active: 1,
					},
					kftraining.PaddleJobReplicaTypeWorker: {
						Active: 3,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPaddleJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdPaddleJob := kftraining.PaddleJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(paddleJob), &createdPaddleJob)).To(gomega.Succeed())
				g.Expect(createdPaddleJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.PaddleJobReplicaTypeMaster: {
							Active: 1,
						},
						kftraining.PaddleJobReplicaTypeWorker: {
							Active: 3,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker PaddleJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "PaddleJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdPaddleJob := kftraining.PaddleJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(paddleJob), &createdPaddleJob)).To(gomega.Succeed())
				createdPaddleJob.Status.Conditions = append(createdPaddleJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPaddleJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a PyTorchJob on worker if admitted", func() {
		pyTorchJob := testingpytorchjob.MakePyTorchJob("pytorchjob1", managerNs.Name).
			Queue(managerLq.Name).
			PyTorchReplicaSpecs(
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "pytorchjob-master",
					RestartPolicy: "OnFailure",
				},
				testingpytorchjob.PyTorchReplicaSpecRequirement{
					ReplicaType:   kftraining.PyTorchJobReplicaTypeWorker,
					ReplicaCount:  1,
					Name:          "pytorchjob-worker",
					RestartPolicy: "Never",
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, pyTorchJob)).Should(gomega.Succeed())

		wlLookupKey := types.NamespacedName{Name: workloadpytorchjob.GetWorkloadNameForPyTorchJob(pyTorchJob.Name, pyTorchJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the PyTorchJob in the worker, updates the manager's PyTorchJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := kftraining.PyTorchJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(pyTorchJob), &createdPyTorchJob)).To(gomega.Succeed())
				createdPyTorchJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.PyTorchJobReplicaTypeMaster: {
						Active: 1,
					},
					kftraining.PyTorchJobReplicaTypeWorker: {
						Active:    2,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPyTorchJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := kftraining.PyTorchJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(pyTorchJob), &createdPyTorchJob)).To(gomega.Succeed())
				g.Expect(createdPyTorchJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.PyTorchJobReplicaTypeMaster: {
							Active: 1,
						},
						kftraining.PyTorchJobReplicaTypeWorker: {
							Active:    2,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker PyTorchJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "PyTorchJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdPyTorchJob := kftraining.PyTorchJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(pyTorchJob), &createdPyTorchJob)).To(gomega.Succeed())
				createdPyTorchJob.Status.Conditions = append(createdPyTorchJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdPyTorchJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should run a XGBoostJob on worker if admitted", framework.RedundantSpec, func() {
		xgBoostJob := testingxgboostjob.MakeXGBoostJob("xgboostjob1", managerNs.Name).
			Queue(managerLq.Name).
			XGBReplicaSpecs(
				testingxgboostjob.XGBReplicaSpecRequirement{
					ReplicaType:   kftraining.XGBoostJobReplicaTypeMaster,
					ReplicaCount:  1,
					Name:          "master",
					RestartPolicy: "OnFailure",
				},
				testingxgboostjob.XGBReplicaSpecRequirement{
					ReplicaType:   kftraining.XGBoostJobReplicaTypeWorker,
					ReplicaCount:  2,
					Name:          "worker",
					RestartPolicy: "Never",
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, xgBoostJob)).Should(gomega.Succeed())

		wlLookupKey := types.NamespacedName{Name: workloadxgboostjob.GetWorkloadNameForXGBoostJob(xgBoostJob.Name, xgBoostJob.UID), Namespace: managerNs.Name}
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "master",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the XGBoostJob in the worker, updates the manager's XGBoostJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdXGBoostJob := kftraining.XGBoostJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(xgBoostJob), &createdXGBoostJob)).To(gomega.Succeed())
				createdXGBoostJob.Status.ReplicaStatuses = map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
					kftraining.XGBoostJobReplicaTypeMaster: {
						Active: 1,
					},
					kftraining.XGBoostJobReplicaTypeWorker: {
						Active:    2,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdXGBoostJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdXGBoostJob := kftraining.XGBoostJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(xgBoostJob), &createdXGBoostJob)).To(gomega.Succeed())
				g.Expect(createdXGBoostJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kftraining.ReplicaType]*kftraining.ReplicaStatus{
						kftraining.XGBoostJobReplicaTypeMaster: {
							Active: 1,
						},
						kftraining.XGBoostJobReplicaTypeWorker: {
							Active:    2,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker XGBoostJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "XGBoostJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdXGBoostJob := kftraining.XGBoostJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(xgBoostJob), &createdXGBoostJob)).To(gomega.Succeed())
				createdXGBoostJob.Status.Conditions = append(createdXGBoostJob.Status.Conditions, kftraining.JobCondition{
					Type:    kftraining.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdXGBoostJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should not run a MPIJob on worker if set to be managed by external controller", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "launcher",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)
		mpijobNoManagedBy := testingmpijob.MakeMPIJob("mpijob2", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy("example.com/other-controller-not-mpi-operator").
			MPIJobReplicaSpecs(
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeWorker,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyNever,
				},
			).
			Obj()
		ginkgo.By("create a mpijob with external managedBy", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mpijobNoManagedBy)).Should(gomega.Succeed())
		})

		wlLookupKeyNoManagedBy := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijobNoManagedBy.Name, mpijobNoManagedBy.UID), Namespace: managerNs.Name}
		ginkgo.By("setting workload reservation in the management cluster", func() {
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKeyNoManagedBy, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("workload was not created in the worker clusters", func() {
			managerWl := &kueue.Workload{}
			createdWorkload := &kueue.Workload{}
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKeyNoManagedBy, managerWl)).To(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKeyNoManagedBy, createdWorkload)).ToNot(gomega.Succeed())
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKeyNoManagedBy, createdWorkload)).ToNot(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should run a MPIJob on worker if admitted", func() {
		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "launcher",
			}, kueue.PodSetAssignment{
				Name: "worker",
			},
		)
		mpijob := testingmpijob.MakeMPIJob("mpijob1", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			MPIJobReplicaSpecs(
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeLauncher,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyOnFailure,
				},
				testingmpijob.MPIJobReplicaSpecRequirement{
					ReplicaType:   kfmpi.MPIReplicaTypeWorker,
					ReplicaCount:  1,
					RestartPolicy: corev1.RestartPolicyNever,
				},
			).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mpijob)).Should(gomega.Succeed())
		wlLookupKey := types.NamespacedName{Name: workloadmpijob.GetWorkloadNameForMPIJob(mpijob.Name, mpijob.UID), Namespace: managerNs.Name}
		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the MPIJob in the worker, updates the manager's MPIJob status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := kfmpi.MPIJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(mpijob), &createdMPIJob)).To(gomega.Succeed())
				createdMPIJob.Status.ReplicaStatuses = map[kfmpi.MPIReplicaType]*kfmpi.ReplicaStatus{
					kfmpi.MPIReplicaTypeLauncher: {
						Active: 1,
					},
					kfmpi.MPIReplicaTypeWorker: {
						Active:    1,
						Succeeded: 1,
					},
				}
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdMPIJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := kfmpi.MPIJob{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(mpijob), &createdMPIJob)).To(gomega.Succeed())
				g.Expect(createdMPIJob.Status.ReplicaStatuses).To(gomega.Equal(
					map[kfmpi.MPIReplicaType]*kfmpi.ReplicaStatus{
						kfmpi.MPIReplicaTypeLauncher: {
							Active: 1,
						},
						kfmpi.MPIReplicaTypeWorker: {
							Active:    1,
							Succeeded: 1,
						},
					}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker MPIJob, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "MPIJob finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdMPIJob := kfmpi.MPIJob{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(mpijob), &createdMPIJob)).To(gomega.Succeed())
				createdMPIJob.Status.Conditions = append(createdMPIJob.Status.Conditions, kfmpi.JobCondition{
					Type:    kfmpi.JobSucceeded,
					Status:  corev1.ConditionTrue,
					Reason:  "ByTest",
					Message: finishJobReason,
				})
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdMPIJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})

	ginkgo.It("Should remove the worker's workload and job after reconnect when the managers job and workload are deleted", func() {
		job := testingjob.MakeJob("job", managerNs.Name).
			Queue(managerLq.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, job)).Should(gomega.Succeed())
		jobLookupKey := client.ObjectKeyFromObject(job)
		createdJob := &batchv1.Job{}

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

		ginkgo.By("breaking the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting workload reservation in worker1, the job is created in worker1", func() {
			admission := utiltesting.MakeAdmission(managerCq.Name).Obj()

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(util.SetQuotaReservation(worker1TestCluster.ctx, worker1TestCluster.client, createdWorkload, admission)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("breaking the connection to worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("removing the managers job and workload", func() {
			gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, job)).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				g.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError(), "workload not deleted")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker objects are still present", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restoring the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultikueueSecret2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionTrue,
					Reason: "Active",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker2 wl is removed by the garbage collector", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restoring the connection to worker1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultikueueSecret1.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster1), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionTrue,
					Reason: "Active",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("the worker1 wl and job are removed by the garbage collector", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, jobLookupKey, createdJob)).To(gomega.Succeed())
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should requeue the workload with a delay when the connection to the admitting worker is lost", framework.SlowSpec, func() {
		jobSet := testingjobset.MakeJobSet("job-set", managerNs.Name).
			Queue(managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
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

		wlLookupKey := types.NamespacedName{Name: workloadjobset.GetWorkloadNameForJobSet(jobSet.Name, jobSet.UID), Namespace: managerNs.Name}

		admission := utiltesting.MakeAdmission(managerCq.Name).PodSets(
			kueue.PodSetAssignment{
				Name: "replicated-job-1",
			}, kueue.PodSetAssignment{
				Name: "replicated-job-2",
			},
		)

		admitWorkloadAndCheckWorkerCopies(multikueueAC.Name, wlLookupKey, admission)

		var disconnectedTime time.Time
		ginkgo.By("breaking the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = "bad-secret"
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
					Status: metav1.ConditionFalse,
					Reason: "BadConfig",
				}, util.IgnoreConditionMessage, util.IgnoreConditionTimestampsAndObservedGeneration))
				disconnectedTime = activeCondition.LastTransitionTime.Time
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("waiting for the local workload admission check state to be set to pending and quotaReservatio removed", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdWorkload := &kueue.Workload{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
				acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, multikueueAC.Name)
				g.Expect(acs).To(gomega.BeComparableTo(&kueue.AdmissionCheckState{
					Name:  multikueueAC.Name,
					State: kueue.CheckStatePending,
				}, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime", "Message")))

				// The transition interval should be close to testingKeepReadyTimeout (taking into account the resolution of the LastTransitionTime field)
				g.Expect(acs.LastTransitionTime.Time).To(gomega.BeComparableTo(disconnectedTime.Add(testingWorkerLostTimeout), cmpopts.EquateApproxTime(2*time.Second)))

				g.Expect(createdWorkload.Status.Conditions).ToNot(utiltesting.HaveConditionStatusTrue(kueue.WorkloadQuotaReserved))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restoring the connection to worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				createdCluster.Spec.KubeConfig.Location = managerMultikueueSecret2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, createdCluster)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdCluster := &kueue.MultiKueueCluster{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(workerCluster2), createdCluster)).To(gomega.Succeed())
				activeCondition := apimeta.FindStatusCondition(createdCluster.Status.Conditions, kueue.MultiKueueClusterActive)
				g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.MultiKueueClusterActive,
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

var _ = ginkgo.Describe("Multikueue no GC", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultikueueSecret1 *corev1.Secret
		managerMultikueueSecret2 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		workerCluster2           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig
		multikueueAC             *kueue.AdmissionCheck
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 0)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

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
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultikueueSecret1)).To(gomega.Succeed())

		managerMultikueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultikueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultikueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultikueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multikueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
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
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerCq, true)
		util.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq, true)
		util.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multikueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultikueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultikueueSecret2, true)
	})

	ginkgo.It("Should remove the worker's workload and job when managers job is deleted", framework.SlowSpec, func() {
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
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				createdJob := batchv1.Job{}
				g.Expect(worker1TestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
				g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(job), &createdJob)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

func admitWorkloadAndCheckWorkerCopies(acName string, wlLookupKey types.NamespacedName, admission *utiltesting.AdmissionWrapper) {
	ginkgo.By("setting workload reservation in the management cluster", func() {
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("checking the workload creation in the worker clusters", func() {
		managerWl := &kueue.Workload{}
		createdWorkload := &kueue.Workload{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.By("setting workload reservation in worker2, the workload is admitted in manager and worker1 wl is removed", func() {
		createdWorkload := &kueue.Workload{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			g.Expect(util.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, createdWorkload, admission.Obj())).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
			acs := workload.FindAdmissionCheck(createdWorkload.Status.AdmissionChecks, acName)
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
}

func waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey types.NamespacedName, finishJobReason string) {
	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
		g.Expect(apimeta.FindStatusCondition(createdWorkload.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeComparableTo(&metav1.Condition{
			Type:    kueue.WorkloadFinished,
			Status:  metav1.ConditionTrue,
			Reason:  string(kftraining.JobSucceeded),
			Message: finishJobReason,
		}, util.IgnoreConditionTimestampsAndObservedGeneration))
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

	gomega.Eventually(func(g gomega.Gomega) {
		createdWorkload := &kueue.Workload{}
		g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
	}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
}
