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

package rayjobandraycluster

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
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	"sigs.k8s.io/kueue/pkg/scheduler"
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
		"RayJob and RayCluster Controllers Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		DepCRDPaths: []string{util.RayOperatorCrds},
	}

	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerAndSchedulerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		// Setup common indexes
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Setup core controllers and scheduler
		cCache := schdcache.New(mgr.GetClient())
		queues := qcache.NewManager(mgr.GetClient(), cCache)
		opts = append(opts, jobframework.WithQueues(queues))

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &config.Configuration{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		// Setup RayCluster controller
		err = raycluster.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		rayClusterReconciler := raycluster.NewReconciler(mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName), opts...)
		err = rayClusterReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = raycluster.SetupRayClusterWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Setup RayJob controller
		err = rayjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		rayJobReconciler := rayjob.NewReconciler(mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName), opts...)
		err = rayJobReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = rayjob.SetupRayJobWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Enable both integrations
		jobframework.EnableIntegration(raycluster.FrameworkName)
		jobframework.EnableIntegration(rayjob.FrameworkName)

		// Start scheduler
		sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
