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

// Package was contains integration tests for the BringYourOwnPodGroup feature
// (KEP-13150), against a real envtest API server with the upstream
// GenericWorkload feature gate and scheduling.k8s.io/v1alpha2 API enabled, so
// that Pod/Job reconciliation exercises real PodGroup/Workload objects
// instead of only unit-tested plumbing.
package was

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
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
	util.RunSuite(t, "BringYourOwnPodGroup Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.BringYourOwnPodGroup, true)

	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
		APIServerFeatureGates: []string{
			"GenericWorkload=true",
		},
		APIServerRuntimeConfig: []string{
			"scheduling.k8s.io/v1alpha2=true",
		},
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	preemptionExpectations := preemptexpectations.New()
	configuration := &configapi.Configuration{}
	return func(ctx context.Context, mgr manager.Manager) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = pod.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient())
		opts = append(opts, jobframework.WithCache(cCache))

		podReconciler, err := pod.NewReconciler(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorder(constants.JobControllerName),
			opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = podReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		err = job.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		jobReconciler, err := job.NewReconciler(
			ctx,
			mgr.GetClient(),
			mgr.GetFieldIndexer(),
			mgr.GetEventRecorder(constants.JobControllerName),
			opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = jobReconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobframework.EnableIntegration(job.FrameworkName)

		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache, qcache.WithPreemptionExpectations(preemptionExpectations))
		opts = append(opts, jobframework.WithQueues(queues))

		mgr.GetScheme().Default(configuration)

		failedCtrl, err := core.SetupControllers(
			mgr,
			queues,
			cCache,
			configuration,
			core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
		)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		err = job.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = pod.SetupWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		sched := scheduler.New(
			queues,
			cCache,
			mgr.GetClient(),
			mgr.GetEventRecorder(constants.AdmissionName),
			scheduler.WithPreemptionExpectations(preemptionExpectations),
		)
		gomega.Expect(sched.Start(ctx)).NotTo(gomega.HaveOccurred())
	}
}
