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

package multikueuesequential

import (
	"cmp"
	"context"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/test/util"
)

var (
	managerK8SVersion  *versionutil.Version
	managerClusterName string
	worker1ClusterName string
	worker2ClusterName string
	kueueNS            = util.GetKueueNamespace()

	k8sManagerClient client.Client
	k8sWorker1Client client.Client
	k8sWorker2Client client.Client
	ctx              context.Context

	managerCfg *rest.Config
	worker1Cfg *rest.Config
	worker2Cfg *rest.Config

	worker1KConfig []byte
	worker2KConfig []byte

	managerRestClient *rest.RESTClient
	worker1RestClient *rest.RESTClient
	worker2RestClient *rest.RESTClient
)

func TestAPIs(t *testing.T) {
	util.RunE2ESuite(t, "End To End MultiKueue Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	util.SetupLogger()

	managerClusterName = cmp.Or(os.Getenv("MANAGER_KIND_CLUSTER_NAME"), "kind-manager")
	worker1ClusterName = cmp.Or(os.Getenv("WORKER1_KIND_CLUSTER_NAME"), "kind-worker1")
	worker2ClusterName = cmp.Or(os.Getenv("WORKER2_KIND_CLUSTER_NAME"), "kind-worker2")

	var err error
	k8sManagerClient, managerCfg, err = util.CreateClientUsingCluster("kind-" + managerClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sWorker1Client, worker1Cfg, err = util.CreateClientUsingCluster("kind-" + worker1ClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	k8sWorker2Client, worker2Cfg, err = util.CreateClientUsingCluster("kind-" + worker2ClusterName)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	managerRestClient = util.CreateRestClient(managerCfg)
	worker1RestClient = util.CreateRestClient(worker1Cfg)
	worker2RestClient = util.CreateRestClient(worker2Cfg)

	ctx = ginkgo.GinkgoT().Context()

	worker1KConfig, err = util.KubeconfigForMultiKueueSA(ctx, k8sWorker1Client, worker1Cfg, kueueNS, "mksa", worker1ClusterName, util.MultiKueueRulesForManager(ctx, k8sManagerClient))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(util.MakeMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue1", worker1KConfig)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		gomega.Expect(util.CleanMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue1")).To(gomega.Succeed())
		gomega.Expect(util.CleanKubeconfigForMultiKueueSA(ctx, k8sWorker1Client, kueueNS, "mksa")).To(gomega.Succeed())
	})

	worker2KConfig, err = util.KubeconfigForMultiKueueSA(ctx, k8sWorker2Client, worker2Cfg, kueueNS, "mksa", worker2ClusterName, util.MultiKueueRulesForManager(ctx, k8sManagerClient))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(util.MakeMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue2", worker2KConfig)).To(gomega.Succeed())
	ginkgo.DeferCleanup(func() {
		gomega.Expect(util.CleanMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue2")).To(gomega.Succeed())
		gomega.Expect(util.CleanKubeconfigForMultiKueueSA(ctx, k8sWorker2Client, kueueNS, "mksa")).To(gomega.Succeed())
	})

	waitForAvailableStart := time.Now()
	util.WaitForKueueAvailability(ctx, k8sManagerClient)
	util.WaitForKueueAvailability(ctx, k8sWorker1Client)
	util.WaitForKueueAvailability(ctx, k8sWorker2Client)

	ginkgo.GinkgoLogr.Info(
		"Kueue and all required operators are available in all the clusters",
		"waitingTime", time.Since(waitForAvailableStart),
	)
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(managerCfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	managerK8SVersion, err = kubeversion.FetchServerVersion(discoveryClient)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})
