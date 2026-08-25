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

package reclaimbackoff

import (
	"context"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"k8s.io/utils/clock"
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
	"sigs.k8s.io/kueue/pkg/scheduler/reclaimbackoff"
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

// backoffBase is the base cooldown used by the integration tracker. It is kept
// short so that the block is observable and the expiry happens within the
// envtest timeout budget.
const backoffBase = 2 * time.Second

func TestScheduler(t *testing.T) {
	util.RunSuite(t, "Scheduler Reclaim Backoff Suite")
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

// managerAndSchedulerSetup starts the scheduler. When withBackoff is true the
// reclaim backoff tracker is injected (as the binary does when
// Configuration.ReclaimBackoff is set); when false the scheduler runs with a
// nil tracker, matching a Configuration that omits the reclaimBackoff block.
func managerAndSchedulerSetup(withBackoff bool) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient())
		preemptionExpectations := preemptexpectations.New()
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache,
			qcache.WithPreemptionExpectations(preemptionExpectations),
		)

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

		err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		var tracker *reclaimbackoff.Tracker
		if withBackoff {
			// Reset window is set far above the base so a single spec never crosses it.
			tracker = reclaimbackoff.New(backoffBase, time.Hour, time.Hour, clock.RealClock{})
		}

		sched := scheduler.New(
			queues,
			cCache,
			mgr.GetClient(),
			mgr.GetEventRecorder(constants.AdmissionName),
			scheduler.WithPreemptionExpectations(preemptionExpectations),
			scheduler.WithReclaimBackoff(tracker),
		)
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
