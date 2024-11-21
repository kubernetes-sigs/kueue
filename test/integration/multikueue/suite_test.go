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
	"sync"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
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
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadtfjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/tfjob"
	workloadxgboostjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/xgboostjob"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
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
		CRDPath:     filepath.Join("..", "..", "..", "config", "components", "crd", "bases"),
		WebhookPath: filepath.Join("..", "..", "..", "config", "components", "webhook"),
		DepCRDPaths: []string{filepath.Join("..", "..", "..", "dep-crds", "jobset-operator"),
			filepath.Join("..", "..", "..", "dep-crds", "training-operator-crds"),
			filepath.Join("..", "..", "..", "dep-crds", "mpi-operator"),
		},
		APIServerFeatureGates: apiFeatureGates,
	}
	c.cfg = c.fwk.Init()
	c.ctx, c.client = c.fwk.SetupClient(c.cfg)
	// skip the manager setup if setup func is not provided
	if setupFnc != nil {
		c.fwk.StartManager(c.ctx, c.cfg, setupFnc)
	}
	return c
}

func managerSetup(ctx context.Context, mgr manager.Manager) {
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

	err = workloadjob.SetupWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjobset.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	jobsetReconciler := workloadjobset.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = jobsetReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjobset.SetupJobSetWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadtfjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	tfjobReconciler := workloadtfjob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = tfjobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadtfjob.SetupTFJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadpaddlejob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	paddleJobReconciler := workloadpaddlejob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = paddleJobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadpaddlejob.SetupPaddleJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadpytorchjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	pyTorchJobReconciler := workloadpytorchjob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = pyTorchJobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadpytorchjob.SetupPyTorchJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadxgboostjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	xgboostJobReconciler := workloadxgboostjob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = xgboostJobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadxgboostjob.SetupXGBoostJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadmpijob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	mpiJobReconciler := workloadmpijob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = mpiJobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadmpijob.SetupMPIJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func managerAndMultiKueueSetup(ctx context.Context, mgr manager.Manager, gcInterval time.Duration, enabledIntegrations sets.Set[string]) {
	managerSetup(ctx, mgr)

	err := multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), managersConfigNamespace.Name)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	adapters, err := jobframework.GetMultiKueueAdapters(enabledIntegrations)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = multikueue.SetupControllers(mgr, managersConfigNamespace.Name,
		multikueue.WithGCInterval(gcInterval),
		multikueue.WithWorkerLostTimeout(testingWorkerLostTimeout),
		multikueue.WithEventsBatchPeriod(100*time.Millisecond),
		multikueue.WithAdapters(adapters),
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.BeforeSuite(func() {
	var managerFeatureGates []string
	version, err := versionutil.ParseGeneric(os.Getenv("ENVTEST_K8S_VERSION"))
	if err != nil || !version.LessThan(versionutil.MustParseSemantic("1.30.0")) {
		managerFeatureGates = []string{"JobManagedBy=true"}
	}

	ginkgo.By("creating the clusters", func() {
		wg := sync.WaitGroup{}
		wg.Add(3)
		go func() {
			defer wg.Done()
			// pass nil setup since the manager for the manage cluster is different in some specs.
			c := createCluster(nil, managerFeatureGates...)
			managerTestCluster = c
		}()
		go func() {
			defer wg.Done()
			c := createCluster(managerSetup)
			worker1TestCluster = c
		}()
		go func() {
			defer wg.Done()
			c := createCluster(managerSetup)
			worker2TestCluster = c
		}()
		wg.Wait()
	})

	discoveryClient, err := discovery.NewDiscoveryClientForConfig(managerTestCluster.cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	managerK8sVersion, err = kubeversion.FetchServerVersion(discoveryClient)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	managersConfigNamespace = &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: "kueue-system",
		},
	}
	gomega.Expect(managerTestCluster.client.Create(managerTestCluster.ctx, managersConfigNamespace)).To(gomega.Succeed())
})

var _ = ginkgo.AfterSuite(func() {
	managerTestCluster.fwk.Teardown()
	worker1TestCluster.fwk.Teardown()
	worker2TestCluster.fwk.Teardown()
})
