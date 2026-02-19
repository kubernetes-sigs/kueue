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

package mpijob

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
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"MPIJob Controller Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		DepCRDPaths: []string{util.MpiOperatorCrds},
		WebhookPath: util.WebhookPath,
	}

	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(setupJobManager bool, opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		controllersSetup(ctx, mgr, setupJobManager, opts...)
	}
}

func managerAndSchedulerSetup(setupTASControllers bool, opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		cCache, queues, configuration := controllersSetup(ctx, mgr, true, opts...)
		if setupTASControllers {
			failedCtrl, err := tas.SetupControllers(mgr, queues, cCache, configuration, nil)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

			err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
		err := sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}

func controllersSetup(
	ctx context.Context, mgr manager.Manager, setupJobManager bool, opts ...jobframework.Option,
) (*schdcache.Cache, *qcache.Manager, *config.Configuration) {
	cCache := schdcache.New(mgr.GetClient())
	queues := util.NewManagerForIntegrationTests(mgr.GetClient(), cCache)
	opts = append(opts, jobframework.WithCache(cCache), jobframework.WithQueues(queues))

	reconciler, err := mpijob.NewReconciler(
		ctx,
		mgr.GetClient(),
		mgr.GetFieldIndexer(),
		mgr.GetEventRecorderFor(constants.JobControllerName),
		opts...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = mpijob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = reconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	err = mpijob.SetupMPIJobWebhook(mgr, opts...)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	jobframework.EnableIntegration(mpijob.FrameworkName)
	configuration := &config.Configuration{}
	mgr.GetScheme().Default(configuration)
	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration, nil)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)
	failedWebhook, err := webhooks.Setup(mgr, nil)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	if setupJobManager {
		jobReconciler, _ := job.NewReconciler(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err = job.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = jobReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = job.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}

	return cCache, queues, configuration
}
