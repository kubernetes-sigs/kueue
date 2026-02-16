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

package dra

import (
	"context"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
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
		"DRA Controller Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DynamicResourceAllocation, true)

	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
		APIServerFeatureGates: []string{
			"DynamicResourceAllocation=true",
		},
		APIServerRuntimeConfig: []string{
			"resource.k8s.io/v1beta2=true",
		},
	}

	cfg = fwk.Init()

	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	ginkgo.By("tearing down the test environment")
	fwk.Teardown()
})

// Manager setup used by tests to start controllers with DRA ConfigMap configuration
func managerSetup(modifyConfig func(*config.Configuration)) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		// Indexes
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		// Webhooks
		failedWebhook, err := webhooks.Setup(mgr, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

		mappings := []config.DeviceClassMapping{
			{
				Name:             corev1.ResourceName("foo"),
				DeviceClassNames: []corev1.ResourceName{"foo.example.com"},
			},
			{
				Name:             corev1.ResourceName("res-1"),
				DeviceClassNames: []corev1.ResourceName{"test-deviceclass-1"},
			},
			{
				Name:             corev1.ResourceName("res-2"),
				DeviceClassNames: []corev1.ResourceName{"test-deviceclass-2"},
			},
		}

		// Controllers configuration
		controllersCfg := &config.Configuration{
			Namespace: ptr.To("kueue-system"),
			Resources: &config.Resources{
				DeviceClassMappings: mappings,
			},
		}
		mgr.GetScheme().Default(controllersCfg)
		controllersCfg.Metrics.EnableClusterQueueResources = true
		if modifyConfig != nil {
			modifyConfig(controllersCfg)
		}

		err = dra.CreateMapperFromConfiguration(controllersCfg.Resources.DeviceClassMappings)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := schdcache.New(mgr.GetClient())
		queues := util.NewManagerForIntegrationTests(mgr.GetClient(), cCache)

		// Core controllers
		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg, nil)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		// Scheduler - required for workload admission
		sched := scheduler.New(
			queues,
			cCache,
			mgr.GetClient(),
			mgr.GetEventRecorderFor("kueue-admission"),
		)
		err = mgr.Add(sched)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
