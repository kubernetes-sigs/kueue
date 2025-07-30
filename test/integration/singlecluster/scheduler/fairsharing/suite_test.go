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

package fairsharing

import (
	"context"
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
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
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

func TestScheduler(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Scheduler Fair Sharing Suite",
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

func managerAndSchedulerSetup(admissionFairSharing *config.AdmissionFairSharing) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		fairSharing := &config.FairSharing{
			Enable: true,
		}

		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		_ = features.SetEnable(features.AdmissionFairSharing, true)
		cCache := cache.New(mgr.GetClient(), cache.WithFairSharing(fairSharing.Enable), cache.WithAdmissionFairSharing(admissionFairSharing))
		queues := queue.NewManager(mgr.GetClient(), cCache, queue.WithAdmissionFairSharing(admissionFairSharing))

		configuration := &config.Configuration{FairSharing: fairSharing, AdmissionFairSharing: admissionFairSharing}
		configuration.Metrics.EnableClusterQueueResources = true
		mgr.GetScheme().Default(configuration)

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		failedWebhook, err := webhooks.Setup(mgr, config.MultiKueueDispatcherModeAllAtOnce)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName),
			scheduler.WithFairSharing(fairSharing), scheduler.WithAdmissionFairSharing(admissionFairSharing))
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
