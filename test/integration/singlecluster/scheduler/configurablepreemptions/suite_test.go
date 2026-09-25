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

package configurablepreemptions

import (
	"context"
	"testing"
	"time"

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
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

const (
	extraResource    = "example.com/tpus-count"
	commonLabelKey   = "commonTestingKey"
	commonLabelValue = "commonTestingValue"
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	ctx       context.Context
	fwk       *framework.Framework
	qManager  *qcache.Manager
)

func TestConfigurablePreemptions(t *testing.T) {
	util.RunSuite(t, "Configurable Preemptions Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ConfigurablePreemptions, true)
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PrioritizePreemptorWorkloads, true)

	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerAndSchedulerSetup() framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient())
		preemptionExpectations := preemptexpectations.New()
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache,
			qcache.WithPreemptionExpectations(preemptionExpectations))
		qManager = queues

		configuration := &config.Configuration{
			Namespace: new("kueue-system"),
		}
		mgr.GetScheme().Default(configuration)

		failedCtrl, err := core.SetupControllers(
			mgr,
			queues,
			cCache,
			configuration,
			core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
		)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, configuration, nil, tas.WithRequeueBatchInterval(time.Second))
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

		err = pod.SetupWebhook(mgr, jobframework.WithQueues(queues))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		sched := scheduler.New(
			queues,
			cCache,
			mgr.GetClient(),
			mgr.GetEventRecorder(constants.AdmissionName),
			scheduler.WithPreemptionExpectations(preemptionExpectations),
		)
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
