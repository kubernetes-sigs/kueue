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

package excluderesources

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/pkg/util/webhook"
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

func TestSchedulerWithExcludeResourcePrefixes(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Scheduler with Exclude Resource Prefixes Suite",
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

func managerAndSchedulerSetup(configuration *config.Configuration) framework.ManagerSetup {
	if configuration == nil {
		configuration = &config.Configuration{}
	}
	return func(ctx context.Context, mgr manager.Manager) {
		var queueOptions []qcache.Option
		var cacheOptions []schdcache.Option

		mgr.GetScheme().Default(configuration)

		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Configure excludeResourcePrefixes
		if configuration.Resources != nil && len(configuration.Resources.ExcludeResourcePrefixes) > 0 {
			queueOptions = append(queueOptions,
				qcache.WithExcludedResourcePrefixes(configuration.Resources.ExcludeResourcePrefixes))
			cacheOptions = append(cacheOptions,
				schdcache.WithExcludedResourcePrefixes(configuration.Resources.ExcludeResourcePrefixes))
		}

		cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
		queues := util.NewManagerForIntegrationTests(mgr.GetClient(), cCache, queueOptions...)

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		// Setup TAS controllers for TAS integration tests
		failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, configuration, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

		err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		err = webhook.SetupNoopWebhook(mgr, &corev1.Pod{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		sched := scheduler.New(queues, cCache, mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.AdmissionName))
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
