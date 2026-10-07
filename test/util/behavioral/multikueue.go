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

package behavioral

import (
	"context"
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type ClusterInfo struct {
	Name   string
	Client client.Client
	Ctx    context.Context
}

//revive:disable:context-as-argument

func DefaultClusterInfosForTests(
	ctx1 context.Context,
	client1 client.Client,
	ctx2 context.Context,
	client2 client.Client,
) []ClusterInfo {
	return []ClusterInfo{
		{
			Name:   "worker1",
			Client: client1,
			Ctx:    ctx1,
		},
		{
			Name:   "worker2",
			Client: client2,
			Ctx:    ctx2,
		},
	}
}

//revive:enable:context-as-argument

func GetClientForSelectedWorkerCluster(g gomega.Gomega, managerWl *kueue.Workload, clusters ...ClusterInfo) ClusterInfo {
	ginkgo.GinkgoHelper()

	clusterName := managerWl.Status.ClusterName
	g.Expect(clusterName).ToNot(gomega.BeNil())

	for _, cluster := range clusters {
		if cluster.Name == *clusterName {
			return cluster
		}
	}

	ginkgo.Fail("none of the supplied clusters was selected")
	return ClusterInfo{}
}

// BreakConnection simulates a network loss to cluster's worker by rewriting the API server in its
// kubeconfig secret to an unreachable address (connection refused).
// The cluster stores the kubeconfig secret's name (Location) but not its namespace, so the caller
// supplies secretNamespace. Returns a callback that restores the original kubeconfig.
func BreakConnection(ctx context.Context, cli client.Client, cluster *kueue.MultiKueueCluster, secretNamespace string) (restoreConnection func()) {
	ginkgo.GinkgoHelper()

	clusterKey := client.ObjectKeyFromObject(cluster)
	secretKey := client.ObjectKey{Namespace: secretNamespace, Name: cluster.Spec.ClusterSource.KubeConfig.Location}

	originalSecret := &corev1.Secret{}
	gomega.Expect(cli.Get(ctx, secretKey, originalSecret)).To(gomega.Succeed())
	originalKubeConfig := originalSecret.Data[kueue.MultiKueueConfigSecretKey]
	gomega.Expect(originalKubeConfig).NotTo(gomega.BeEmpty())

	ginkgo.By(fmt.Sprintf("breaking the connection to %s", clusterKey), func() {
		setSecretKubeConfig(ctx, cli, secretKey, unreachableKubeConfig(originalKubeConfig))
		expectClusterActive(ctx, cli, clusterKey, metav1.ConditionFalse, "ClientConnectionFailed")
	})

	return func() {
		ginkgo.GinkgoHelper()
		ginkgo.By(fmt.Sprintf("restoring the connection to %s", clusterKey), func() {
			setSecretKubeConfig(ctx, cli, secretKey, originalKubeConfig)
			expectClusterActive(ctx, cli, clusterKey, metav1.ConditionTrue, "Active")
		})
	}
}

func setSecretKubeConfig(ctx context.Context, cli client.Client, secretKey client.ObjectKey, kubeConfig []byte) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		secret := &corev1.Secret{}
		g.Expect(cli.Get(ctx, secretKey, secret)).To(gomega.Succeed())
		secret.Data[kueue.MultiKueueConfigSecretKey] = kubeConfig
		g.Expect(cli.Update(ctx, secret)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed())
}

// unreachableKubeConfig returns kubeConfig with every cluster's server rewritten to an unreachable
// address, so a client built from it fails to connect (connection refused) rather than reaching the
// real API server.
func unreachableKubeConfig(kubeConfig []byte) []byte {
	ginkgo.GinkgoHelper()
	cfg, err := clientcmd.Load(kubeConfig)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	for name := range cfg.Clusters {
		cfg.Clusters[name].Server = "https://127.0.0.1:1"
	}
	out, err := clientcmd.Write(*cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	return out
}

func expectClusterActive(ctx context.Context, cli client.Client, clusterKey client.ObjectKey, status metav1.ConditionStatus, reason string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		cluster := &kueue.MultiKueueCluster{}
		g.Expect(cli.Get(ctx, clusterKey, cluster)).To(gomega.Succeed())
		activeCondition := apimeta.FindStatusCondition(cluster.Status.Conditions, kueue.MultiKueueClusterActive)
		g.Expect(activeCondition).To(gomega.BeComparableTo(&metav1.Condition{
			Type:   kueue.MultiKueueClusterActive,
			Status: status,
			Reason: reason,
		}, IgnoreConditionMessage, IgnoreConditionTimestampsAndObservedGeneration))
	}, Timeout, Interval).Should(gomega.Succeed())
}
