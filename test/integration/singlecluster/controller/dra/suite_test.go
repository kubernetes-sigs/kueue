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
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
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
func managerSetup(ctx context.Context, mgr manager.Manager) {
	// Indexes
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Webhooks
	failedWebhook, err := webhooks.Setup(mgr, config.MultiKueueDispatcherModeAllAtOnce)
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

	err = dra.CreateMapperFromConfiguration(mappings)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	// Controllers configuration
	controllersCfg := &config.Configuration{
		Namespace: ptr.To("kueue-system"),
		Resources: &config.Resources{
			DeviceClassMappings: mappings,
		},
	}
	mgr.GetScheme().Default(controllersCfg)
	controllersCfg.Metrics.EnableClusterQueueResources = true

	cCache := schdcache.New(mgr.GetClient())
	queues := qcache.NewManager(mgr.GetClient(), cCache)

	// Core controllers
	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg)
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

// Helper function to create a ResourceClaim
func makeResourceClaim(name, namespace, deviceClassName string, count int64) *resourcev1beta2.ResourceClaim {
	return &resourcev1beta2.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: resourcev1beta2.ResourceClaimSpec{
			Devices: resourcev1beta2.DeviceClaim{
				Requests: []resourcev1beta2.DeviceRequest{{
					Name: "device-request",
					Exactly: &resourcev1beta2.ExactDeviceRequest{
						DeviceClassName: deviceClassName,
						AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
						Count:           count,
					},
				}},
			},
		},
	}
}
