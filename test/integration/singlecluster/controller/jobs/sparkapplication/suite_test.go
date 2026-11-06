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

package sparkapplication

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/elasticjobs"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	jobcontrollers "sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	workloadsparkapplication "sigs.k8s.io/kueue/pkg/controller/jobs/sparkapplication"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

func TestAPIs(t *testing.T) {
	behavioral.RunSuite(t, "SparkApplication Controller Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SparkApplicationIntegration, true)

	fwk = &framework.Framework{
		DepCRDPaths: []string{behavioral.SparkOperatorCrds},
		WebhookPath: behavioral.WebhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	ginkgo.GinkgoHelper()
	return func(ctx context.Context, mgr manager.Manager) {
		controllersSetup(ctx, mgr, opts...)
	}
}

func managerAndSchedulerSetup(setupTASControllers bool, opts ...jobframework.Option) framework.ManagerSetup {
	ginkgo.GinkgoHelper()
	return func(ctx context.Context, mgr manager.Manager) {
		cCache, queues, configuration, preemptionExpectations := controllersSetup(ctx, mgr, opts...)

		if setupTASControllers {
			failedCtrl, err := tas.SetupControllers(mgr, queues, cCache, configuration, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

			err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		if features.Enabled(features.ElasticJobsViaWorkloadSlices) {
			failedCtrl, err := elasticjobs.SetupWithManager(mgr, configuration, nil, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)
		}

		sched := scheduler.New(
			queues,
			cCache,
			mgr.GetClient(),
			mgr.GetEventRecorder(constants.AdmissionName),
			scheduler.WithPreemptionExpectations(preemptionExpectations),
		)
		err := mgr.Add(sched)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}

// controllersSetup sets up the SparkApplication controller, the core controllers and the webhooks.
// The webhooks get the queues and the cache because the API server calls them, see WebhookPath.
func controllersSetup(
	ctx context.Context, mgr manager.Manager, opts ...jobframework.Option,
) (*schdcache.Cache, *qcache.Manager, *config.Configuration, *expectations.Store) {
	ginkgo.GinkgoHelper()
	integrationManager := jobcontrollers.NewIntegrationManager()
	cCache := schdcache.New(mgr.GetClient())
	preemptionExpectations := preemptexpectations.New()
	queueOptions := []qcache.Option{qcache.WithPreemptionExpectations(preemptionExpectations)}
	queues := integration.NewManager(ctx, mgr.GetClient(), cCache, queueOptions...)
	opts = append(opts, jobframework.WithIntegrationManager(integrationManager), jobframework.WithCache(cCache), jobframework.WithQueues(queues))

	reconciler, err := workloadsparkapplication.NewReconciler(
		ctx,
		mgr.GetClient(),
		mgr.GetFieldIndexer(),
		mgr.GetEventRecorder(constants.JobControllerName),
		opts...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = workloadsparkapplication.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = reconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = workloadsparkapplication.SetupWebhook(mgr, opts...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = pod.SetupWebhook(mgr, opts...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	integrationManager.EnableIntegration(workloadsparkapplication.FrameworkName)

	configuration := &config.Configuration{}
	mgr.GetScheme().Default(configuration)
	failedCtrl, err := core.SetupControllers(
		mgr,
		queues,
		cCache,
		configuration,
		core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
	)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)
	failedWebhook, err := webhooks.Setup(mgr, nil)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	return cCache, queues, configuration, preemptionExpectations
}
