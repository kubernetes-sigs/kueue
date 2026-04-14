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

package statefulset

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/discovery"
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
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/controller/jobs/statefulset"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var (
	cfg                  *rest.Config
	k8sClient            client.Client
	serverVersionFetcher *kubeversion.ServerVersionFetcher
	ctx                  context.Context
	fwk                  *framework.Framework
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"StatefulSet Controller Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

var _ = ginkgo.ReportAfterSuite("Generate JUnit Report", func(report ginkgo.Report) {
	err := util.ConfigureSuiteReporting(report)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = pod.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = statefulset.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		reconciler, err := statefulset.NewReconciler(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = reconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		podReconciler, err := statefulset.NewPodReconciler(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = podReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient())
		preemptionExpectations := preemptexpectations.New()
		queueOptions := []qcache.Option{qcache.WithPreemptionExpectations(preemptionExpectations)}
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache, queueOptions...)
		opts = append(opts, jobframework.WithQueues(queues), jobframework.WithCache(cCache))

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &config.Configuration{}, nil, preemptionExpectations, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		err = statefulset.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = pod.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		jobframework.EnableIntegration(statefulset.FrameworkName)

		discoveryClient, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
		err = serverVersionFetcher.FetchServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
