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

package provisioning

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
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

func TestProvisioning(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Provisioning admission check suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		DepCRDPaths: []string{
			util.AutoscalerCrds,
		},
		WebhookPath: util.WebhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

type managerSetupOpts struct {
	runScheduler     bool
	runJobController bool
}

type managerSetupOption func(*managerSetupOpts)

func runScheduler(opts *managerSetupOpts) {
	opts.runScheduler = true
}

func runJobController(opts *managerSetupOpts) {
	opts.runJobController = true
}

func managerSetup(options ...managerSetupOption) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		var opts managerSetupOpts
		for _, opt := range options {
			opt(&opts)
		}

		var jobReconciler jobframework.JobReconcilerInterface

		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		controllersCfg := &config.Configuration{}
		mgr.GetScheme().Default(controllersCfg)

		controllersCfg.Metrics.EnableClusterQueueResources = true

		cCache := schdcache.New(mgr.GetClient())
		queues := util.NewManagerForIntegrationTests(mgr.GetClient(), cCache)

		if opts.runJobController {
			var err error
			jobReconciler, err = job.NewReconciler(
				ctx,
				mgr.GetClient(),
				mgr.GetFieldIndexer(),
				mgr.GetEventRecorderFor(constants.JobControllerName))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = job.SetupIndexes(ctx, mgr.GetFieldIndexer())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = jobReconciler.SetupWithManager(mgr)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = job.SetupWebhook(mgr, jobframework.WithCache(cCache), jobframework.WithQueues(queues))
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			jobframework.EnableIntegration(job.FrameworkName)
		}

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		err = provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		reconciler, err := provisioning.NewController(
			mgr.GetClient(),
			mgr.GetEventRecorderFor("kueue-provisioning-request-controller"), nil)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = reconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		if opts.runScheduler {
			sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
			err = sched.Start(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
}
