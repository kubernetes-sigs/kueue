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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	versionutil "k8s.io/apimachinery/pkg/util/version"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	// +kubebuilder:scaffold:imports
)

const (
	testingWorkerLostTimeout = 3 * time.Second
)

type cluster struct {
	cfg    *rest.Config
	client client.Client
	ctx    context.Context
	fwk    *framework.Framework
}

func (c *cluster) kubeConfigBytes() ([]byte, error) {
	return utiltesting.RestConfigToKubeConfig(c.cfg)
}

var (
	managerK8sVersion       *versionutil.Version
	managerTestCluster      cluster
	worker1TestCluster      cluster
	worker2TestCluster      cluster
	managersConfigNamespace *corev1.Namespace
)

func TestMultiKueue(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Multikueue Suite",
	)
}

func createCluster(setupFnc framework.ManagerSetup, apiFeatureGates ...string) cluster {
	c := cluster{}
	c.fwk = &framework.Framework{
		CRDPath:               filepath.Join("..", "..", "..", "config", "components", "crd", "bases"),
		WebhookPath:           filepath.Join("..", "..", "..", "config", "components", "webhook"),
		DepCRDPaths:           []string{filepath.Join("..", "..", "..", "dep-crds", "jobset-operator")},
		APIServerFeatureGates: apiFeatureGates,
	}
	c.cfg = c.fwk.Init()
	c.ctx, c.client = c.fwk.RunManager(c.cfg, setupFnc)
	return c
}

var _ = ginkgo.BeforeSuite(func() {
	var managerFeatureGates []string
	version, err := versionutil.ParseGeneric(os.Getenv("ENVTEST_K8S_VERSION"))
	if err != nil || !version.LessThan(versionutil.MustParseSemantic("1.30.0")) {
		managerFeatureGates = []string{"JobManagedBy=true"}
	}

	managerTestCluster = createCluster(managerAndMultiKueueSetup, managerFeatureGates...)
	worker1TestCluster = createCluster(managerSetup)
	worker2TestCluster = createCluster(managerSetup)

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(managerTestCluster.cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	managerK8sVersion, err = kubeversion.FetchServerVersion(discoveryClient)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

var _ = ginkgo.AfterSuite(func() {
	managerTestCluster.fwk.Teardown()
	worker1TestCluster.fwk.Teardown()
	worker2TestCluster.fwk.Teardown()
})

func managerSetup(mgr manager.Manager, ctx context.Context) {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	cCache := cache.New(mgr.GetClient())
	queues := queue.NewManager(mgr.GetClient(), cCache)

	configuration := &config.Configuration{}
	mgr.GetScheme().Default(configuration)

	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

	failedWebhook, err := webhooks.Setup(mgr)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	jobReconciler := workloadjob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = jobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjob.SetupWebhook(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjobset.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	jobsetReconciler := workloadjobset.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = jobsetReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjobset.SetupJobSetWebhook(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func managerAndMultiKueueSetup(mgr manager.Manager, ctx context.Context) {
	managerSetup(mgr, ctx)

	managersConfigNamespace = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kueue-system",
		},
	}
	gomega.Expect(mgr.GetClient().Create(ctx, managersConfigNamespace)).To(gomega.Succeed())

	err := multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), managersConfigNamespace.Name)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = multikueue.SetupControllers(mgr, managersConfigNamespace.Name,
		multikueue.WithGCInterval(2*time.Second),
		multikueue.WithWorkerLostTimeout(testingWorkerLostTimeout),
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}
