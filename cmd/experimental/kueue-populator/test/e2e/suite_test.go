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
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/IrvingMg/kindkit"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	kindClusterName           = "kueue-populator-e2e"
	kueueSystemNamespace      = "kueue-system"
	defaultKueueVersion       = "v0.16.0"
	kueueManagerDeployment    = "kueue-controller-manager"
	populatorDeployment       = "kueue-populator"
	kueueReleaseManifestURL   = "https://github.com/kubernetes-sigs/kueue/releases/download/%s/manifests.yaml"
	clusterReadyWaitTimeout   = 3 * time.Minute
	kueueReadyWaitTimeout     = 5 * time.Minute
	populatorReadyTimeout     = 2 * time.Minute
	kueueManifestFetchTimeout = 30 * time.Second
)

var (
	k8sClient client.Client
	cfg       *rest.Config
	ctx       context.Context

	cluster *kindkit.Cluster
)

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "KueuePopulator E2E Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	populatorImage := os.Getenv("KUEUE_POPULATOR_IMAGE")
	gomega.Expect(populatorImage).NotTo(gomega.BeEmpty(), "KUEUE_POPULATOR_IMAGE must be set")

	populatorManifest := os.Getenv("KUEUE_POPULATOR_MANIFEST")
	gomega.Expect(populatorManifest).NotTo(gomega.BeEmpty(), "KUEUE_POPULATOR_MANIFEST must be set")

	kueueVersion := os.Getenv("KUEUE_VERSION")
	if kueueVersion == "" {
		kueueVersion = defaultKueueVersion
	}

	ctx = context.Background()

	ginkgo.By("creating kind cluster " + kindClusterName)
	var err error
	cluster, err = kindkit.Create(ctx, kindClusterName, kindkit.WithWaitForReady(clusterReadyWaitTimeout))
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "create kind cluster")

	ginkgo.By("installing Kueue " + kueueVersion)
	kueueManifest, err := fetchURL(ctx, fmt.Sprintf(kueueReleaseManifestURL, kueueVersion))
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "fetch kueue release manifest")
	gomega.Expect(cluster.ApplyManifests(ctx, kueueManifest)).To(gomega.Succeed(), "apply kueue release manifest")

	cfg, err = cluster.RESTConfig()
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "get cluster REST config")

	gomega.Expect(kueue.AddToScheme(scheme.Scheme)).To(gomega.Succeed())
	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())

	ginkgo.By("waiting for kueue-controller-manager to become Available")
	gomega.Expect(waitForDeploymentAvailable(
		ctx, k8sClient, kueueSystemNamespace, kueueManagerDeployment, kueueReadyWaitTimeout,
	)).To(gomega.Succeed(), "wait for kueue-controller-manager")

	ginkgo.By("loading populator image " + populatorImage + " into kind cluster")
	gomega.Expect(cluster.LoadImages(ctx, populatorImage)).To(gomega.Succeed(), "load populator image")

	ginkgo.By("applying populator manifest " + populatorManifest)
	populatorYAML, err := os.ReadFile(populatorManifest)
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "read populator manifest")
	gomega.Expect(cluster.ApplyManifests(ctx, populatorYAML)).To(gomega.Succeed(), "apply populator manifest")

	ginkgo.By("waiting for kueue-populator to become Available")
	gomega.Expect(waitForDeploymentAvailable(
		ctx, k8sClient, kueueSystemNamespace, populatorDeployment, populatorReadyTimeout,
	)).To(gomega.Succeed(), "wait for kueue-populator")
})

var _ = ginkgo.AfterSuite(func() {
	if cluster == nil {
		return
	}
	if err := cluster.Delete(context.Background()); err != nil {
		fmt.Fprintf(ginkgo.GinkgoWriter, "failed to delete kind cluster %q: %v\n", kindClusterName, err)
	}
	cluster = nil
})

func fetchURL(ctx context.Context, url string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, kueueManifestFetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func waitForDeploymentAvailable(ctx context.Context, c client.Client, namespace, name string, timeout time.Duration) error {
	key := types.NamespacedName{Namespace: namespace, Name: name}
	return wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		var deploy appsv1.Deployment
		if err := c.Get(ctx, key, &deploy); err != nil {
			if apierrors.IsNotFound(err) {
				return false, nil
			}
			return false, err
		}
		for _, cond := range deploy.Status.Conditions {
			if cond.Type == appsv1.DeploymentAvailable && cond.Status == corev1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}
