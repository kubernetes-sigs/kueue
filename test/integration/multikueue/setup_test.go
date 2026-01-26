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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/clientcmd"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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

		managerMultiKueueSecret1 = utiltesting.MakeSecret("multikueue1", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret1)).To(gomega.Succeed())

		managerMultiKueueSecret2 = utiltesting.MakeSecret("multikueue2", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w2Kubeconfig).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueSecret2)).To(gomega.Succeed())

		workerCluster1 = utiltestingapi.MakeMultiKueueCluster("worker1").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret1.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster1)).To(gomega.Succeed())

		workerCluster2 = utiltestingapi.MakeMultiKueueCluster("worker2").KubeConfig(kueue.SecretLocationType, managerMultiKueueSecret2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, workerCluster2)).To(gomega.Succeed())

		managerMultiKueueConfig = utiltestingapi.MakeMultiKueueConfig("multikueueconfig").Clusters(workerCluster1.Name, workerCluster2.Name).Obj()
		gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managerMultiKueueConfig)).Should(gomega.Succeed())

		multiKueueAC = utiltestingapi.MakeAdmissionCheck("ac1").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", managerMultiKueueConfig.Name).
			Obj()
		util.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, multiKueueAC)

		managerCq = utiltestingapi.MakeClusterQueue("q1").
			AdmissionChecks(kueue.AdmissionCheckReference(multiKueueAC.Name)).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq)

		managerLq = utiltestingapi.MakeLocalQueue(managerCq.Name, managerNs.Name).ClusterQueue(managerCq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq)

		worker1Cq = utiltestingapi.MakeClusterQueue("q1").Obj()
		util.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq)
		worker1Lq = utiltestingapi.MakeLocalQueue(worker1Cq.Name, worker1Ns.Name).ClusterQueue(worker1Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq)

		worker2Cq = utiltestingapi.MakeClusterQueue("q1").Obj()
		util.CreateClusterQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Cq)
		worker2Lq = utiltestingapi.MakeLocalQueue(worker2Cq.Name, worker2Ns.Name).ClusterQueue(worker2Cq.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(worker2TestCluster.ctx, worker2TestCluster.client, worker2Lq)
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
		ac := utiltestingapi.MakeAdmissionCheck("testing-ac").
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
			badConfig := utiltestingapi.MakeMultiKueueConfig("bad-config").Clusters("c1", "c2", "c1").Obj()
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, badConfig).Error()).Should(gomega.Equal(
				`MultiKueueConfig.kueue.x-k8s.io "bad-config" is invalid: spec.clusters[2]: Duplicate value: "c1"`))
		})

		config := utiltestingapi.MakeMultiKueueConfig("testing-config").Clusters("testing-cluster").Obj()
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

		cluster := utiltestingapi.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.SecretLocationType, "testing-secret").Obj()
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
						Reason:  "BadKubeConfig",
						Message: `load client config failed: Secret "testing-secret" not found`,
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
		secret := utiltesting.MakeSecret("testing-secret", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1Kubeconfig).Obj()

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
		ac := utiltestingapi.MakeAdmissionCheck("testing-ac").
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

		config := utiltestingapi.MakeMultiKueueConfig("testing-config").Clusters("testing-cluster").Obj()
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

		cluster := utiltestingapi.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.PathLocationType, fsKubeConfig).Obj()
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
						Reason:  "BadKubeConfig",
						Message: fmt.Sprintf("load client config failed: open %s: no such file or directory", fsKubeConfig),
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

	ginkgo.It("Should properly detect insecure kubeconfig of MultiKueueClusters, kubeconfig provided by secret", func() {
		var w1KubeconfigInvalidBytes []byte
		ginkgo.By("Create a kubeconfig with an invalid certificate authority path", func() {
			cfg, err := worker1TestCluster.kubeConfigBytes()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			w1KubeconfigInvalid, err := clientcmd.Load(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(w1KubeconfigInvalid).NotTo(gomega.BeNil())

			w1KubeconfigInvalid.Clusters["default-cluster"].CertificateAuthority = "/some/random/path"
			w1KubeconfigInvalidBytes, err = clientcmd.Write(*w1KubeconfigInvalid)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		secret := utiltesting.MakeSecret("testing-secret", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1KubeconfigInvalidBytes).Obj()
		ginkgo.By("creating the secret, the kubeconfig is insecure", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, secret)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, secret) })
		})

		ac := utiltestingapi.MakeAdmissionCheck("testing-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "testing-config").
			Obj()
		ginkgo.By("creating the admission check", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, ac)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, ac) })
		})

		config := utiltestingapi.MakeMultiKueueConfig("testing-config").Clusters("testing-cluster").Obj()
		ginkgo.By("creating the config", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, config)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, config) })
		})

		cluster := utiltestingapi.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.SecretLocationType, "testing-secret").Obj()
		ginkgo.By("creating the cluster, its Active state is updated", func() {
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
						Reason:  "InsecureKubeConfig",
						Message: "load client config failed: certificate-authority file paths are not allowed, use certificate-authority-data for cluster default-cluster",
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
						Message: "Inactive clusters: [testing-cluster]",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("updating the secret with a valid kubeconfig", func() {
			updatedSecret := &corev1.Secret{}
			secretKey := client.ObjectKeyFromObject(secret)
			gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, secretKey, updatedSecret)).To(gomega.Succeed())
			updatedSecret.Data[kueue.MultiKueueConfigSecretKey] = w1Kubeconfig
			gomega.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, updatedSecret)).To(gomega.Succeed())
		})

		ginkgo.By("the cluster become active", func() {
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

	ginkgo.It("Should properly detect insecure kubeconfig of MultiKueueClusters, kubeconfig provided by file", func() {
		var w1KubeconfigInvalidBytes []byte
		ginkgo.By("Create a kubeconfig with disallowed tokenFile", func() {
			cfg, err := worker1TestCluster.kubeConfigBytes()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			w1KubeconfigInvalid, err := clientcmd.Load(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(w1KubeconfigInvalid).NotTo(gomega.BeNil())

			w1KubeconfigInvalid.AuthInfos["default-user"].TokenFile = "/some/random/path"
			w1KubeconfigInvalidBytes, err = clientcmd.Write(*w1KubeconfigInvalid)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		tempDir := ginkgo.GinkgoT().TempDir()
		fsKubeConfig := filepath.Join(tempDir, "testing.kubeconfig")

		ginkgo.By("creating the kubeconfig file, the kubeconfig is insecure", func() {
			gomega.Expect(os.WriteFile(fsKubeConfig, w1KubeconfigInvalidBytes, 0666)).Should(gomega.Succeed())
		})

		ac := utiltestingapi.MakeAdmissionCheck("testing-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "testing-config").
			Obj()
		ginkgo.By("creating the admission check", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, ac)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, ac) })
		})

		config := utiltestingapi.MakeMultiKueueConfig("testing-config").Clusters("testing-cluster").Obj()
		ginkgo.By("creating the config", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, config)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, config) })
		})

		cluster := utiltestingapi.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.PathLocationType, fsKubeConfig).Obj()
		ginkgo.By("creating the cluster, its Active state is updated", func() {
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
						Reason:  "InsecureKubeConfig",
						Message: "load client config failed: tokenFile is not allowed",
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
						Message: "Inactive clusters: [testing-cluster]",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		w1Kubeconfig, err := worker1TestCluster.kubeConfigBytes()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("creating the kubeconfig file, the cluster become active", func() {
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

	ginkgo.It("Should allow insecure kubeconfig of MultiKueueClusters, kubeconfig provided by secret, when MultiKueueAllowInsecureKubeconfigs enabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueAllowInsecureKubeconfigs, true)

		tempDir := ginkgo.GinkgoT().TempDir()
		tokenFile := filepath.Join(tempDir, "testing.tokenfile")

		ginkgo.By("creating the tokenFile file", func() {
			gomega.Expect(os.WriteFile(tokenFile, []byte("FAKE-TOKEN-123456"), 0666)).Should(gomega.Succeed())
		})

		var w1KubeconfigInvalidBytes []byte
		ginkgo.By("Create a kubeconfig with disallowed tokenFile", func() {
			cfg, err := worker1TestCluster.kubeConfigBytes()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			w1KubeconfigInvalid, err := clientcmd.Load(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(w1KubeconfigInvalid).NotTo(gomega.BeNil())

			w1KubeconfigInvalid.AuthInfos["default-user"].TokenFile = tokenFile
			w1KubeconfigInvalidBytes, err = clientcmd.Write(*w1KubeconfigInvalid)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		secret := utiltesting.MakeSecret("testing-secret", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1KubeconfigInvalidBytes).Obj()
		ginkgo.By("creating the secret, the kubeconfig is insecure", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, secret)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, secret) })
		})

		ac := utiltestingapi.MakeAdmissionCheck("testing-ac").
			ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "testing-config").
			Obj()
		ginkgo.By("creating the admission check", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, ac)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, ac) })
		})

		config := utiltestingapi.MakeMultiKueueConfig("testing-config").Clusters("testing-cluster").Obj()
		ginkgo.By("creating the config", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, config)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, config) })
		})

		cluster := utiltestingapi.MakeMultiKueueCluster("testing-cluster").KubeConfig(kueue.SecretLocationType, "testing-secret").Obj()
		ginkgo.By("creating the cluster, its Active state is updated", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cluster)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, cluster) })

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

	ginkgo.It("Should properly detect insecure kubeconfig of MultiKueueClusters and remove remote client", func() {
		var w1KubeconfigInvalidBytes []byte
		ginkgo.By("Create a kubeconfig with an invalid certificate authority path", func() {
			cfg, err := worker1TestCluster.kubeConfigBytes()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			w1KubeconfigInvalid, err := clientcmd.Load(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(w1KubeconfigInvalid).NotTo(gomega.BeNil())

			w1KubeconfigInvalid.Clusters["default-cluster"].CertificateAuthority = "/some/random/path"
			w1KubeconfigInvalidBytes, err = clientcmd.Write(*w1KubeconfigInvalid)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		})

		secret := utiltesting.MakeSecret("testing-secret", managersConfigNamespace.Name).Data(kueue.MultiKueueConfigSecretKey, w1KubeconfigInvalidBytes).Obj()
		ginkgo.By("creating the secret, with insecure kubeconfig", func() {
			gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, secret)).Should(gomega.Succeed())
			ginkgo.DeferCleanup(func() error { return managerTestCluster.client.Delete(managerTestCluster.ctx, secret) })
		})

		clusterKey := client.ObjectKeyFromObject(workerCluster1)
		acKey := client.ObjectKeyFromObject(multiKueueAC)

		ginkgo.By("updating the cluster, the worker1 cluster becomes inactive", func() {
			updatedCluster := kueue.MultiKueueCluster{}
			ginkgo.By("updating the cluster spec", func() {
				gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
				updatedCluster.Spec.ClusterSource.KubeConfig.LocationType = kueue.SecretLocationType
				updatedCluster.Spec.ClusterSource.KubeConfig.Location = secret.Name
				gomega.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, &updatedCluster)).To(gomega.Succeed())
			})

			ginkgo.By("wait for the status update", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, &updatedCluster)).To(gomega.Succeed())
					g.Expect(updatedCluster.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
						Status:  metav1.ConditionFalse,
						Reason:  "InsecureKubeConfig",
						Message: "load client config failed: certificate-authority file paths are not allowed, use certificate-authority-data for cluster default-cluster",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, acKey, multiKueueAC)).To(gomega.Succeed())
					g.Expect(multiKueueAC.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionTrue,
						Reason:  "SomeActiveClusters",
						Message: "Inactive clusters: [worker1]",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		job := testingjob.MakeJob("job1", managerNs.Name).
			Queue(kueue.LocalQueueName(managerLq.Name)).
			Obj()
		ginkgo.By("create a job and reserve quota, only existing remoteClients are part of the NominatedClusterNames", func() {
			util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, job)
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(job.Name, job.UID), Namespace: managerNs.Name}

			ginkgo.By("setting workload reservation in the management cluster", func() {
				admission := utiltestingapi.MakeAdmission(managerCq.Name).Obj()
				util.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, wlLookupKey, admission)
			})

			ginkgo.By("verify remote clients are managed correctly: worker1 was removed and worker2 is still active", func() {
				managerWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, wlLookupKey, managerWl)).To(gomega.Succeed())
					g.Expect(managerWl.Status.NominatedClusterNames).NotTo(gomega.ContainElements(workerCluster1.Name))
					g.Expect(managerWl.Status.NominatedClusterNames).To(gomega.ContainElements(workerCluster2.Name))

					createdWorkload := &kueue.Workload{}
					g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, wlLookupKey, createdWorkload)).To(utiltesting.BeNotFoundError())
					g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, wlLookupKey, createdWorkload)).To(gomega.Succeed())
					g.Expect(createdWorkload.Spec).To(gomega.BeComparableTo(managerWl.Spec))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("MultiKueue with ClusterProfile", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("Feature gate enabled", ginkgo.Ordered, func() {
		var (
			mkc1 *kueue.MultiKueueCluster
			mkc2 *kueue.MultiKueueCluster
			cp1  *inventoryv1alpha1.ClusterProfile
			cp2  *inventoryv1alpha1.ClusterProfile
		)

		ginkgo.BeforeEach(func() {
			// The flag must be called before multikueue.SetupIndexer so that MultiKueueCluster ClusterProfile indexer is registered.
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueClusterProfile, true)

			managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
				managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
			})
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, mkc1, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, mkc2, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cp1, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cp2, true)
			mkc1, mkc2, cp1, cp2 = nil, nil, nil, nil
			managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
		})

		ginkgo.It("Should report no cluster providers configured", func() {
			ginkgo.By("Create a MultiKueueCluster with ClusterProfile", func() {
				mkc1 = utiltestingapi.MakeMultiKueueCluster("worker3").ClusterProfile("test-profile").Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc1)).To(gomega.Succeed())
			})

			mkcKey := client.ObjectKeyFromObject(mkc1)
			ginkgo.By("Verify status conditions of the MultiKueueCluster", func() {
				mkc := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, mkcKey, mkc)).To(gomega.Succeed())
					activeCondition := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
						Status:  metav1.ConditionFalse,
						Reason:  "BadClusterProfile",
						Message: "load client config failed: ClusterProfile.multicluster.x-k8s.io \"test-profile\" not found",
					}, util.IgnoreConditionTimestampsAndObservedGeneration))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Create ClusterProfile and trigger MultiKueueCluster reconciliation", func() {
				cp1 = utiltestingapi.MakeClusterProfile("test-profile", config.DefaultNamespace).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp1)).To(gomega.Succeed())
			})

			ginkgo.By("Verify status of the MultiKueueCluster", func() {
				mkc := &kueue.MultiKueueCluster{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, mkcKey, mkc)).To(gomega.Succeed())
					g.Expect(mkc.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.MultiKueueClusterActive,
						Status:  metav1.ConditionFalse,
						Reason:  "BadClusterProfile",
						Message: "load client config failed: no credentials provider configured",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should reconcile a MultiKueueCluster when the referenced ClusterProfile is deleted and re-created", func() {
			cpName := "cp-delete-recreate"
			clusterName := "cp-worker-delete-recreate"

			ginkgo.By("Create a MultiKueueCluster referencing a missing ClusterProfile", func() {
				mkc1 = utiltestingapi.MakeMultiKueueCluster(clusterName).ClusterProfile(cpName).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc1)).To(gomega.Succeed())
			})
			clusterKey := client.ObjectKey{Name: clusterName}

			ginkgo.By("Wait for status to indicate ClusterProfile is missing", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Create the referenced ClusterProfile", func() {
				cp1 = utiltestingapi.MakeClusterProfile(cpName, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp1)).To(gomega.Succeed())
			})

			ginkgo.By("Wait for status to indicate 'no credentials provider configured'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("no credentials provider configured"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the referenced ClusterProfile", func() {
				util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cp1, true)
			})

			ginkgo.By("Wait for status to go back to 'not found'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Re-create the referenced ClusterProfile", func() {
				cp1 = utiltestingapi.MakeClusterProfile(cpName, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp1)).To(gomega.Succeed())
			})

			ginkgo.By("Wait for status again to indicate 'no credentials provider configured'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("no credentials provider configured"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should reconcile all MultiKueueClusters that reference the same ClusterProfile", func() {
			cpName := "cp-shared"
			clusterNames := []string{"cp-worker-shared-1", "cp-worker-shared-2"}

			ginkgo.By("Create two MultiKueueClusters referencing the same missing ClusterProfile", func() {
				mkc1 = utiltestingapi.MakeMultiKueueCluster(clusterNames[0]).ClusterProfile(cpName).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc1)).To(gomega.Succeed())

				mkc2 = utiltestingapi.MakeMultiKueueCluster(clusterNames[1]).ClusterProfile(cpName).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc2)).To(gomega.Succeed())
			})

			ginkgo.By("Both clusters should report ClusterProfile missing", func() {
				for _, n := range clusterNames {
					clusterKey := client.ObjectKey{Name: n}
					gomega.Eventually(func(g gomega.Gomega) {
						mkc := &kueue.MultiKueueCluster{}
						g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

						active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
						g.Expect(active).NotTo(gomega.BeNil())
						g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
						g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})

			ginkgo.By("Create the shared ClusterProfile", func() {
				cp1 = utiltestingapi.MakeClusterProfile(cpName, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp1)).To(gomega.Succeed())
			})

			ginkgo.By("Both clusters should move to 'no credentials provider configured'", func() {
				for _, n := range clusterNames {
					clusterKey := client.ObjectKey{Name: n}
					gomega.Eventually(func(g gomega.Gomega) {
						mkc := &kueue.MultiKueueCluster{}
						g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

						active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
						g.Expect(active).NotTo(gomega.BeNil())
						g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
						g.Expect(active.Message).To(gomega.ContainSubstring("no credentials provider configured"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				}
			})
		})

		ginkgo.It("Should require ClusterProfile to exist in the configured namespace", func() {
			cpName := "cp-wrong-namespace"
			clusterName := "cp-worker-wrong-namespace"

			otherNS := utiltesting.MakeNamespace("cp-other-ns")
			ginkgo.By("Create a non-system namespace", func() {
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, otherNS)).To(gomega.Succeed())
				ginkgo.DeferCleanup(func() {
					gomega.Expect(util.DeleteNamespace(managerTestCluster.ctx, managerTestCluster.client, otherNS)).To(gomega.Succeed())
				})
			})

			ginkgo.By("Create MultiKueueCluster referencing ClusterProfile", func() {
				mkc1 = utiltestingapi.MakeMultiKueueCluster(clusterName).ClusterProfile(cpName).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc1)).To(gomega.Succeed())
			})

			clusterKey := client.ObjectKey{Name: clusterName}
			ginkgo.By("Wait for status to indicate ClusterProfile missing", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Create ClusterProfile with the same name in a different namespace", func() {
				cp1 = utiltestingapi.MakeClusterProfile(cpName, otherNS.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp1)).To(gomega.Succeed())
			})

			ginkgo.By("Status should remain as 'not found'", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			ginkgo.By("Create ClusterProfile in the configured namespace", func() {
				cp2 = utiltestingapi.MakeClusterProfile(cpName, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp2)).To(gomega.Succeed())
			})

			ginkgo.By("Status should move to 'no credentials provider configured'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("no credentials provider configured"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should update status when switching ClusterProfileRef to another name", func() {
			clusterName := "cp-worker-switch"
			cpName1 := "cp-switch-1"
			cpName2 := "cp-switch-2"

			ginkgo.By("Create a MultiKueueCluster referencing cp1 (initially missing)", func() {
				mkc1 = utiltestingapi.MakeMultiKueueCluster(clusterName).ClusterProfile(cpName1).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc1)).To(gomega.Succeed())
			})
			clusterKey := client.ObjectKey{Name: clusterName}

			ginkgo.By("Wait for status to indicate cp1 missing", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Create cp1 and wait for 'no credentials provider configured'", func() {
				cp1 = utiltestingapi.MakeClusterProfile(cpName1, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp1)).To(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("no credentials provider configured"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Switch cluster to reference cp2 (missing) and wait for 'not found'", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())
					mkc.Spec.ClusterSource.KubeConfig = nil
					mkc.Spec.ClusterSource.ClusterProfileRef = &kueue.ClusterProfileReference{Name: cpName2}
					g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, mkc)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("not found"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Create cp2 and wait for 'no credentials provider configured'", func() {
				cp2 = utiltestingapi.MakeClusterProfile(cpName2, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp2)).To(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("BadClusterProfile"))
					g.Expect(active.Message).To(gomega.ContainSubstring("no credentials provider configured"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Feature gate disabled", ginkgo.Ordered, func() {
		var (
			mkc *kueue.MultiKueueCluster
			cp  *inventoryv1alpha1.ClusterProfile
		)

		ginkgo.BeforeEach(func() {
			managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
				managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
			})
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, mkc, true)
			util.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cp, true)
			mkc, cp = nil, nil
			managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
		})

		ginkgo.It("Should report feature gate disabled when ClusterProfileRef is set", func() {
			clusterName := "cp-worker-feature-disabled"
			cpName := "cp-feature-disabled"

			ginkgo.By("Create a MultiKueueCluster with ClusterProfile", func() {
				mkc = utiltestingapi.MakeMultiKueueCluster(clusterName).ClusterProfile(cpName).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, mkc)).To(gomega.Succeed())
			})

			clusterKey := client.ObjectKey{Name: clusterName}
			ginkgo.By("Verify status condition indicates MultiKueueClusterProfile feature gate is disabled", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(active.Reason).To(gomega.Equal("MultiKueueClusterProfileFeatureDisabled"))
					g.Expect(active.Message).To(gomega.ContainSubstring("feature gate is disabled"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Creating a ClusterProfile should not make the cluster active (still feature disabled)", func() {
				cp = utiltestingapi.MakeClusterProfile(cpName, managersConfigNamespace.Name).Obj()
				gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, cp)).To(gomega.Succeed())

				gomega.Consistently(func(g gomega.Gomega) {
					mkc := &kueue.MultiKueueCluster{}
					g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, clusterKey, mkc)).To(gomega.Succeed())

					active := apimeta.FindStatusCondition(mkc.Status.Conditions, kueue.MultiKueueClusterActive)
					g.Expect(active).NotTo(gomega.BeNil())
					g.Expect(active.Reason).To(gomega.Equal("MultiKueueClusterProfileFeatureDisabled"))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})
	})
})
