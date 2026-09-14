//go:build !exclude_scheduler_library

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

package was

import (
	"context"
	"testing"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
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

func TestAPIs(t *testing.T) {
	util.RunSuite(t, "WAS DRA Feasibility Integration Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SchedulerLibraryIntegration, true)
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRADeviceFeasibility, true)
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegration, true)

	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
		DepCRDPaths: []string{
			util.AutoscalerCrds,
		},
		APIServerFeatureGates: []string{
			"DynamicResourceAllocation=true",
		},
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup() func(ctx context.Context, mgr manager.Manager) {
	return func(ctx context.Context, mgr manager.Manager) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = core.SetupResourceSliceIndexer(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		err = webhook.SetupNoopWebhook(mgr, &corev1.Pod{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		mappings := []config.DeviceClassMapping{
			{
				Name:             corev1.ResourceName("test-gpus"),
				DeviceClassNames: []corev1.ResourceName{"gpu.test.com"},
			},
		}

		controllersCfg := &config.Configuration{
			Namespace: new("kueue-system"),
			Resources: &config.Resources{
				DeviceClassMappings: mappings,
			},
		}
		mgr.GetScheme().Default(controllersCfg)

		draMapper := dra.NewResourceMapper()
		err = draMapper.PopulateFromConfiguration(controllersCfg.Resources.DeviceClassMappings)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		resourceFormatter := resources.NewResourceFormatter()

		sim, err := was.NewWASSimulator(ctx, mgr.GetConfig(), was.WithDRA(mgr.GetClient()))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cacheOptions := []schdcache.Option{
			schdcache.WithSchedulingSimulator(sim),
			schdcache.WithResourceFormatter(resourceFormatter),
		}
		cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
		preemptionExpectations := preemptexpectations.New()
		draBackedResources := dra.NewExtendedResourceCache()
		queueOptions := []qcache.Option{
			qcache.WithPreemptionExpectations(preemptionExpectations),
			qcache.WithDRABackedResources(draBackedResources),
			qcache.WithResourceFormatter(resourceFormatter),
		}
		queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache, queueOptions...)

		failedCtrl, err := core.SetupControllers(
			mgr,
			queues,
			cCache,
			controllersCfg,
			core.SetupControllersOpts{
				PreemptionExpectations:    preemptionExpectations,
				DRAMapper:                 draMapper,
				DRABackedResources:        draBackedResources,
				ResourceFormatter:         resourceFormatter,
				ResourceSliceAPIAvailable: true,
			},
		)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "Core controller", failedCtrl)

		failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, controllersCfg, nil, tas.WithRequeueBatchInterval(time.Second))
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

		err = pod.SetupWebhook(mgr, jobframework.WithQueues(queues))
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		sched := scheduler.New(
			queues,
			cCache,
			mgr.GetClient(),
			mgr.GetEventRecorder(constants.AdmissionName),
			scheduler.WithPreemptionExpectations(preemptionExpectations),
			scheduler.WithResourceFormatter(resourceFormatter),
		)
		err = mgr.Add(sched)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
