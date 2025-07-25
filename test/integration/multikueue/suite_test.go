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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadaw "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	workloadjobset "sigs.k8s.io/kueue/pkg/controller/jobs/jobset"
	workloadpaddlejob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/paddlejob"
	workloadpytorchjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/pytorchjob"
	workloadtfjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/tfjob"
	workloadxgboostjob "sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/xgboostjob"
	workloadmpijob "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	workloadpod "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	workloadraycluster "sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
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
		"MultiKueue Suite",
	)
}

func createCluster(setupFnc framework.ManagerSetup, apiFeatureGates ...string) cluster {
	c := cluster{}
	c.fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
		DepCRDPaths: []string{
			util.JobsetCrds,
			util.TrainingOperatorCrds,
			util.MpiOperatorCrds,
			util.RayOperatorCrds,
			util.AppWrapperCrds,
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

	failedWebhook, err := webhooks.Setup(mgr, ptr.Deref(configuration.MultiKueue.DispatcherName, config.MultiKueueDispatcherModeAllAtOnce))
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

	err = workloadpod.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	podReconciller := workloadpod.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = podReconciller.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	nsSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", "kueue-system"},
			},
		},
	}
	mjnsSelector, err := metav1.LabelSelectorAsSelector(nsSelector)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadpod.SetupWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues), jobframework.WithManagedJobsNamespaceSelector(mjnsSelector))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadrayjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	rayJobReconciler := workloadrayjob.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = rayJobReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadrayjob.SetupRayJobWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadraycluster.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	rayClusterReconciler := workloadraycluster.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = rayClusterReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadraycluster.SetupRayClusterWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadaw.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	appwrapperReconciler := workloadaw.NewReconciler(
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName))
	err = appwrapperReconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadaw.SetupAppWrapperWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func managerAndMultiKueueSetup(
	ctx context.Context,
	mgr manager.Manager,
	gcInterval time.Duration,
	enabledIntegrations sets.Set[string],
	dispatcherName string,
) {
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
		multikueue.WithDispatcherName(dispatcherName),
	)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

var _ = ginkgo.BeforeSuite(func() {
	var managerFeatureGates []string
	ginkgo.By("creating the clusters", func() {
		wg := sync.WaitGroup{}
		wg.Add(3)
		go func() {
			defer ginkgo.GinkgoRecover()
			defer wg.Done()
			// pass nil setup since the manager for the manage cluster is different in some specs.
			c := createCluster(nil, managerFeatureGates...)
			managerTestCluster = c
		}()
		go func() {
			defer ginkgo.GinkgoRecover()
			defer wg.Done()
			c := createCluster(managerSetup)
			worker1TestCluster = c
		}()
		go func() {
			defer ginkgo.GinkgoRecover()
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

	managersConfigNamespace = utiltesting.MakeNamespace("kueue-system")
	util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, managersConfigNamespace)
})

var _ = ginkgo.AfterSuite(func() {
	managerTestCluster.fwk.Teardown()
	worker1TestCluster.fwk.Teardown()
	worker2TestCluster.fwk.Teardown()
})
