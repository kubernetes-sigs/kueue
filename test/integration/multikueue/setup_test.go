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

package multikueue

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		managerNs *corev1.Namespace
		worker1Ns *corev1.Namespace
		worker2Ns *corev1.Namespace

		managerMultiKueueSecret1 *corev1.Secret
		managerMultiKueueSecret2 *corev1.Secret
		workerCluster1           *kueue.MultiKueueCluster
		workerCluster2           *kueue.MultiKueueCluster
		managerMultiKueueConfig  *kueue.MultiKueueConfig
		multiKueueAC             *kueue.AdmissionCheck
		managerCq                *kueue.ClusterQueue
		managerLq                *kueue.LocalQueue

		worker1Cq *kueue.ClusterQueue
		worker1Lq *kueue.LocalQueue

		worker2Cq *kueue.ClusterQueue
		worker2Lq *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		managerNs = util.CreateNamespaceFromPrefixWithLog(managerTestCluster.ctx, managerTestCluster.client, "multikueue-")
		worker1Ns = util.CreateNamespaceWithLog(worker1TestCluster.ctx, worker1TestCluster.client, managerNs.Name)
		worker2Ns = util.CreateNamespaceWithLog(worker2TestCluster.ctx, worker2TestCluster.client, managerNs.Name)

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		w2Kubeconfig, err := worker2TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		managerMultiKueueSecret1 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue1",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w1Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "multikueue2",
				Namespace: managersConfigNamespace.Name,
			},
			Data: map[string][]byte{
				kueue.MultiKueueConfigSecretKey: w2Kubeconfig,
			},
		}
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltesting.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltesting.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltesting.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltesting.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, multiKueueAC)).Should(gomega.Succeed())

		ginkgo.By("wait for check active", func() {
			updatedAc := kueue.AdmissionCheck{}
			acKey := client.ObjectKeyFromObject(multiKueueAC)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, &updatedAc)).To(gomega.Succeed())
				g.Expect(updatedAc.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.AdmissionCheckActive))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		managerCq = utiltesting.MakeClusterQueue("q1").
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
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
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueConfig, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, workerCluster2, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret1, true)
		util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, managerMultiKueueSecret2, true)
	})
	ginkgo.It("Should properly manage the active condition of AdmissionChecks and MultiKueueClusters, kubeconfig provided by secret", func() {
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
})
