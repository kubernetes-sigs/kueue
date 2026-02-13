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

// Note: AfterSuite is intentionally omitted. With parallel ginkgo processes
// (-procs=2), each process runs AfterSuite independently. If one process
// finishes early and deletes secrets, the other process's tests fail with
// "Secret not found".
package mkdra

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/types"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/test/util"
)

var (
	managerClusterName string
	worker1ClusterName string
	worker2ClusterName string

	k8sManagerClient client.WithWatch
	k8sWorker1Client client.WithWatch
	k8sWorker2Client client.WithWatch

	managerCfg *rest.Config
	worker1Cfg *rest.Config
	worker2Cfg *rest.Config

	managerRestClient *rest.RESTClient
	worker1RestClient *rest.RESTClient
	worker2RestClient *rest.RESTClient

	managerK8SVersion *versionutil.Version

	ctx context.Context
)

var kueueNS = util.GetKueueNamespace()

func TestAPIs(t *testing.T) {
	suiteName := "End To End MultiKueue DRA Suite"
	if ver, found := os.LookupEnv("E2E_KIND_VERSION"); found {
		suiteName = fmt.Sprintf("%s: %s", suiteName, ver)
	}
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, suiteName)
}

func waitForDRAExampleDriverAvailability(ctx context.Context, k8sClient client.Client, clusterName string) {
	dsKey := types.NamespacedName{Namespace: "dra-example-driver", Name: "dra-example-driver-kubeletplugin"}
	daemonset := &appsv1.DaemonSet{}
	waitForAvailableStart := time.Now()
	ginkgo.By(fmt.Sprintf("Waiting for availability of daemonset %q on cluster %s", dsKey, clusterName))
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(k8sClient.Get(ctx, dsKey, daemonset)).To(gomega.Succeed())
		g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.BeNumerically(">", 0))
		g.Expect(daemonset.Status.DesiredNumberScheduled).To(gomega.Equal(daemonset.Status.NumberAvailable))
	}, util.StartUpTimeout, util.Interval).Should(gomega.Succeed())
	ginkgo.GinkgoLogr.Info("DaemonSet is available in the cluster", "daemonset", dsKey, "cluster", clusterName, "waitingTime", time.Since(waitForAvailableStart))
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

	// Use MinimalMultiKueueRules for DRA tests - only Jobs, Workloads, Pods
	worker1KConfig, err := util.KubeconfigForMultiKueueSA(ctx, k8sWorker1Client, worker1Cfg, kueueNS, "mksa", worker1ClusterName, util.MinimalMultiKueueRules())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(util.MakeMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue1", worker1KConfig)).To(gomega.Succeed())

	worker2KConfig, err := util.KubeconfigForMultiKueueSA(ctx, k8sWorker2Client, worker2Cfg, kueueNS, "mksa", worker2ClusterName, util.MinimalMultiKueueRules())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(util.MakeMultiKueueSecret(ctx, k8sManagerClient, kueueNS, "multikueue2", worker2KConfig)).To(gomega.Succeed())

	waitForAvailableStart := time.Now()

	// Wait for Kueue availability on all clusters
	util.WaitForKueueAvailability(ctx, k8sManagerClient)
	util.WaitForKueueAvailability(ctx, k8sWorker1Client)
	util.WaitForKueueAvailability(ctx, k8sWorker2Client)

	// Wait for DRA example driver availability on all clusters
	waitForDRAExampleDriverAvailability(ctx, k8sManagerClient, managerClusterName)
	waitForDRAExampleDriverAvailability(ctx, k8sWorker1Client, worker1ClusterName)
	waitForDRAExampleDriverAvailability(ctx, k8sWorker2Client, worker2ClusterName)

	ginkgo.GinkgoLogr.Info(
		"Kueue and DRA example driver are available in all clusters",
		"waitingTime", time.Since(waitForAvailableStart),
	)

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(managerCfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	managerK8SVersion, err = kubeversion.FetchServerVersion(discoveryClient)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})
