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

package core

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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
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
		"Core Controllers Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
	metrics.Register()
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

var _ = ginkgo.ReportAfterSuite("Generate JUnit Report", func(report ginkgo.Report) {
	err := util.ConfigureSuiteReporting(report)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})

type managerSetupOpts struct {
	runScheduler bool
	roleTracker  *roletracker.RoleTracker
}

type managerSetupOption func(*managerSetupOpts)

func runScheduler(opts *managerSetupOpts) {
	opts.runScheduler = true
}

func withRoleTracker(rt *roletracker.RoleTracker) managerSetupOption {
	return func(opts *managerSetupOpts) {
		opts.roleTracker = rt
	}
}

func managerSetup(ctx context.Context, mgr manager.Manager) {
	managerAndControllerSetup(nil)(ctx, mgr)
}

func managerAndSchedulerSetup(ctx context.Context, mgr manager.Manager) {
	managerAndControllerSetup(nil, runScheduler)(ctx, mgr)
}

func managerAndControllerSetup(controllersCfg *config.Configuration, options ...managerSetupOption) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		var opts managerSetupOpts
		for _, opt := range options {
			opt(&opts)
		}

		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		if controllersCfg == nil {
			controllersCfg = &config.Configuration{}
		}

		mgr.GetScheme().Default(controllersCfg)

		controllersCfg.Metrics.EnableClusterQueueResources = true

		lqMetrics := metrics.NewLocalQueueMetricsConfig(controllersCfg.Metrics.LocalQueueMetrics)

		var customLabels *metrics.CustomLabels
		if features.Enabled(features.CustomMetricLabels) && len(controllersCfg.Metrics.CustomLabels) > 0 {
			customLabels = metrics.NewCustomLabels(controllersCfg.Metrics.CustomLabels)
		}

		cacheOpts := []schdcache.Option{
			schdcache.WithResourceMetrics(controllersCfg.Metrics.EnableClusterQueueResources),
			schdcache.WithRoleTracker(opts.roleTracker),
			schdcache.WithLocalQueueMetrics(lqMetrics),
			schdcache.WithCustomLabels(customLabels),
		}
		queueOpts := []qcache.Option{
			qcache.WithRoleTracker(opts.roleTracker),
			qcache.WithLocalQueueMetrics(lqMetrics),
			qcache.WithCustomLabels(customLabels),
		}

		cCache := schdcache.New(mgr.GetClient(), cacheOpts...)
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache, queueOpts...)

		preemptionExpectations := preemptexpectations.New()
		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg, opts.roleTracker, preemptionExpectations, customLabels)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		if opts.roleTracker != nil {
			opts.roleTracker.OnElected(func() {
				metrics.ClearGaugeMetricsForRole(roletracker.RoleFollower)
				cCache.ResyncGaugeMetrics()
				queues.ResyncGaugeMetrics()
			})
		}

		if opts.runScheduler {
			sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName), scheduler.WithPreemptionExpectations(preemptionExpectations), scheduler.WithCustomLabels(customLabels), scheduler.WithLocalQueueMetrics(lqMetrics))
			err = sched.Start(ctx)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
}
