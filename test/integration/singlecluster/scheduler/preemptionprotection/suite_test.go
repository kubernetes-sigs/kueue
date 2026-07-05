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

package preemptionprotection

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
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
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

func TestSchedulerPreemptionProtection(t *testing.T) {
	util.RunSuite(t, "Scheduler Preemption Protection Suite")
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
		mgr.GetScheme().Default(configuration)

		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient(), schdcache.WithFairSharing(fairsharing.Enabled(configuration.FairSharing)))
		preemptionExpectations := preemptexpectations.New()
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache, qcache.WithPreemptionExpectations(preemptionExpectations))

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

		err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// scheduler.New internally wires the protection-expiry retry
		// (Preemptor.SetRetryAfter -> queues.RebroadcastAtTime), the same
		// wiring cmd/kueue/main.go activates, so pending preemptors are
		// retried when a protection window expires without any cluster event.
		sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorder(constants.AdmissionName),
			scheduler.WithFairSharing(configuration.FairSharing),
			scheduler.WithPreemptionProtection(configuration.PreemptionProtection),
			scheduler.WithPreemptionExpectations(preemptionExpectations))
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
