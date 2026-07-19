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

package scheduler

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
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
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

type subResourcePatchFn func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error
type fakeClientUsage int
type fakeClientCallSpec func(obj client.Object) (fakeClientUsage, error)
type fakeClientCallResponseHookSpec func(obj client.Object, err error) (fakeClientUsage, error)

const (
	emitResponse = iota
	fallThrough
)

var (
	cfg                                  *rest.Config
	k8sClient                            client.Client
	ctx                                  context.Context
	fwk                                  *framework.Framework
	fakeClientMutex                      sync.RWMutex
	fakeSubResourcePatchSpec             fakeClientCallSpec
	fakeSubResourcePatchResponseHookSpec fakeClientCallResponseHookSpec
)

func setFakeSubResourcePatchSpec(f fakeClientCallSpec) {
	fakeClientMutex.Lock()
	defer fakeClientMutex.Unlock()
	fakeSubResourcePatchSpec = f
}

func setFakeSubResourcePatchResponseHookSpec(g fakeClientCallResponseHookSpec) {
	fakeClientMutex.Lock()
	defer fakeClientMutex.Unlock()
	fakeSubResourcePatchResponseHookSpec = g
}

func TestScheduler(t *testing.T) {
	util.RunSuite(t, "Scheduler Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		WebhookPath: util.WebhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = setupInterceptedClient()
	fwk.StartManager(ctx, cfg, managerAndSchedulerSetup, framework.WithNewClient(newInterceptedClient))
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

var _ = ginkgo.BeforeEach(func() {
	setFakeSubResourcePatchSpec(nil)
	setFakeSubResourcePatchResponseHookSpec(nil)
})

func managerAndSchedulerSetup(ctx context.Context, mgr manager.Manager) {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	transformations := []config.ResourceTransformation{
		{
			Input:    corev1.ResourceName(pseudoCPU),
			Strategy: ptr.To(config.Replace),
			Outputs:  corev1.ResourceList{corev1.ResourceCPU: resourcev1.MustParse("2")},
		},
	}
	cCache := schdcache.New(mgr.GetClient())
	preemptionExpectations := preemptexpectations.New()
	queues := util.NewManagerForIntegrationTests(ctx, mgr.GetClient(), cCache,
		qcache.WithResourceTransformations(transformations),
		qcache.WithPreemptionExpectations(preemptionExpectations),
		qcache.WithResourceMetrics(true),
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

func setupInterceptedClient() (context.Context, client.Client) {
	ctx, baseClient := fwk.SetupClient(cfg)
	funcs := interceptor.Funcs{
		SubResourcePatch: fakeSubResourcePatchFrom(baseClient),
	}
	client := interceptor.NewClient(baseClient, funcs)
	return ctx, client
}

func newInterceptedClient(config *rest.Config, options client.Options) (client.Client, error) {
	baseClient, err := client.NewWithWatch(config, options)
	if err != nil {
		return nil, err
	}
	funcs := interceptor.Funcs{
		SubResourcePatch: fakeSubResourcePatchFrom(baseClient),
	}
	client := interceptor.NewClient(baseClient, funcs)
	return client, nil
}

func fakeSubResourcePatchFrom(baseK8sClient client.Client) subResourcePatchFn {
	return func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		fakeClientMutex.RLock()
		fSpec := fakeSubResourcePatchSpec
		gSpec := fakeSubResourcePatchResponseHookSpec
		fakeClientMutex.RUnlock()

		if fSpec != nil {
			fakeUsage, err := fSpec(obj)
			if fakeUsage == emitResponse {
				return err
			}
		}
		switch subResourceName {
		case "status":
			response := baseK8sClient.Status().Patch(ctx, obj, patch, opts...)
			if gSpec != nil {
				fakeUsage, err := gSpec(obj, response)
				if fakeUsage == emitResponse {
					return err
				}
			}
			return response
		default:
			return fmt.Errorf("Unsupported subresource: %s", subResourceName)
		}
	}
}
