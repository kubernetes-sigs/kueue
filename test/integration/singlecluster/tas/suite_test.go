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

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
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
		"TopologyAwareScheduling Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
		DepCRDPaths: []string{
			util.AutoscalerCrds,
		},
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(ctx context.Context, mgr manager.Manager) {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	failedWebhook, err := webhooks.Setup(mgr, config.MultiKueueDispatcherModeAllAtOnce)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	controllersCfg := &config.Configuration{}
	mgr.GetScheme().Default(controllersCfg)

	cacheOptions := []schdcache.Option{}
	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	queues := qcache.NewManager(mgr.GetClient(), cCache)

	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "Core controller", failedCtrl)

	failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, controllersCfg)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

	err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	reconciler, err := provisioning.NewController(
		mgr.GetClient(),
		mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = reconciler.SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
	err = sched.Start(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}
