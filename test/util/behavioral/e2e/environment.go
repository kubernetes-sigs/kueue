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

package e2e

import (
	"context"
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

// IsE2EModeDev returns true if running E2E tests in dev mode
func IsE2EModeDev() bool {
	return os.Getenv("E2E_MODE") == "dev"
}

// GetKueueNamespace returns the Kueue namespace from environment or default
func GetKueueNamespace() string {
	if ns := os.Getenv("KUEUE_NAMESPACE"); ns != "" {
		return ns
	}
	return configapi.DefaultNamespace
}

// CreateNamespaceWithLog creates a namespace with the given name and logs it
func CreateNamespaceWithLog(ctx context.Context, k8sClient client.Client, nsName string) *corev1.Namespace {
	ginkgo.GinkgoHelper()
	return CreateNamespaceFromObjectWithLog(ctx, k8sClient, utiltesting.MakeNamespace(nsName))
}

// CreateNamespaceFromPrefixWithLog creates a namespace with generated name from prefix and logs it
func CreateNamespaceFromPrefixWithLog(ctx context.Context, k8sClient client.Client, nsPrefix string) *corev1.Namespace {
	ginkgo.GinkgoHelper()
	return CreateNamespaceFromObjectWithLog(ctx, k8sClient, utiltesting.MakeNamespaceWithGenerateName(nsPrefix))
}

// CreateNamespaceFromObjectWithLog creates a namespace and logs it
func CreateNamespaceFromObjectWithLog(ctx context.Context, k8sClient client.Client, ns *corev1.Namespace) *corev1.Namespace {
	MustCreate(ctx, k8sClient, ns)
	ginkgo.GinkgoLogr.Info("Created namespace", "namespace", ns.Name)
	return ns
}

// GetClusterServerAddress returns the Kubernetes API server address for a given cluster
func GetClusterServerAddress(clusterName string) string {
	return "https://" + clusterName + "-control-plane:6443"
}

// GetAuthInfoFromKubeConfig extracts AuthInfo from kubeconfig bytes
func GetAuthInfoFromKubeConfig(kubeConfig []byte) *clientcmdapi.AuthInfo {
	ginkgo.GinkgoHelper()
	cfg, err := clientcmd.Load(kubeConfig)
	gomega.Expect(err).To(gomega.Succeed())
	return cfg.AuthInfos[cfg.Contexts[cfg.CurrentContext].AuthInfo]
}

// GetKubernetesVersion returns the Kubernetes server version
func GetKubernetesVersion(cfg *rest.Config) string {
	ginkgo.GinkgoHelper()
	discoveryClient := discovery.NewDiscoveryClientForConfigOrDie(cfg)
	ver, err := discoveryClient.ServerVersion()
	gomega.Expect(err).To(gomega.Succeed())
	return ver.String()
}
