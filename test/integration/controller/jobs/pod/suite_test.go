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

package pod

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg                  *rest.Config
	k8sClient            client.Client
	serverVersionFetcher *kubeversion.ServerVersionFetcher
	ctx                  context.Context
	fwk                  *framework.Framework
	crdPath              = filepath.Join("..", "..", "..", "..", "..", "config", "components", "crd", "bases")
	webhookPath          = filepath.Join("..", "..", "..", "..", "..", "config", "components", "webhook")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Pod Controller Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		CRDPath:     crdPath,
		WebhookPath: webhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(
	setupTASControllers bool,
	enableScheduler bool,
	configuration *config.Configuration,
	opts ...jobframework.Option,
) framework.ManagerSetup {
	if configuration == nil {
		configuration = &config.Configuration{}
	}
	if configuration.WaitForPodsReady != nil {
		opts = append(opts, jobframework.WithWaitForPodsReady(configuration.WaitForPodsReady))
	}
	var queueOptions []queue.Option
	if configuration.Resources != nil && len(configuration.Resources.ExcludeResourcePrefixes) > 0 {
		queueOptions = append([]queue.Option{}, queue.WithExcludedResourcePrefixes(configuration.Resources.ExcludeResourcePrefixes))
	}
	return func(ctx context.Context, mgr manager.Manager) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = pod.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		podReconciler := pod.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err = podReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Job reconciler is enabled for "pod parent managed by queue" tests
		err = job.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		jobReconciler := job.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err = jobReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobframework.EnableIntegration(job.FrameworkName)

		cCache := cache.New(mgr.GetClient())
		queues := queue.NewManager(mgr.GetClient(), cCache, queueOptions...)

		mgr.GetScheme().Default(configuration)

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		err = pod.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		failedWebhook, err := webhooks.Setup(mgr)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		if setupTASControllers {
			failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, configuration)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)
		}

		if enableScheduler {
			sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
			err := sched.Start(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
}
