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

package rayservice

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
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/rayservice"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
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
	util.RunSuite(t, "RayService Controller Suite")
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
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient())
		preemptionExpectations := preemptexpectations.New()
		queueOptions := []qcache.Option{qcache.WithPreemptionExpectations(preemptionExpectations)}
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache, queueOptions...)
		opts = append(opts, jobframework.WithQueues(queues))

		failedCtrl, err := core.SetupControllers(
			mgr,
			queues,
			cCache,
			&config.Configuration{},
			core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
		)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		// Setup rayservice
		err = rayservice.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		rayserviceReconciler, _ := rayservice.NewReconciler(ctx, mgr.GetClient(), mgr.GetFieldIndexer(),
			mgr.GetEventRecorder(constants.JobControllerName), opts...)
		err = rayserviceReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = rayservice.SetupRayServiceWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobframework.EnableIntegration(rayservice.FrameworkName)

		// Setup job (so that redis-cleanup job is also intercepted by Kueue)
		err = job.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobReconciler, _ := job.NewReconciler(ctx, mgr.GetClient(), mgr.GetFieldIndexer(),
			mgr.GetEventRecorder(constants.JobControllerName), opts...)
		err = jobReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = job.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobframework.EnableIntegration(job.FrameworkName)

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
