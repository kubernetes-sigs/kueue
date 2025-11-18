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

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

type fakeClientUsage int
type fakeClientCallSpec func(obj client.Object) (fakeClientUsage, error)

const (
	emitResponse = iota
	fallThrough
)

var (
	cfg                  *rest.Config
	k8sClient            client.Client
	ctx                  context.Context
	fwk                  *framework.Framework
	fakeSubResourcePatch fakeClientCallSpec
)

func TestScheduler(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Scheduler Suite",
	)
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
	fakeSubResourcePatch = nil
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
	cCache := cache.New(mgr.GetClient())
	queues := queue.NewManager(mgr.GetClient(), cCache, queue.WithResourceTransformations(transformations))

	configuration := &config.Configuration{}
	mgr.GetScheme().Default(configuration)

	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

	failedWebhook, err := webhooks.Setup(mgr)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
	err = sched.Start(ctx)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}

func setupInterceptedClient() (context.Context, client.Client) {
	ctx, baseClient := fwk.SetupClient(cfg)
	funcs := interceptor.Funcs{
		SubResourcePatch: wrapFakeSubResourcePatch(&fakeSubResourcePatch, baseClient),
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
		SubResourcePatch: wrapFakeSubResourcePatch(&fakeSubResourcePatch, baseClient),
	}
	client := interceptor.NewClient(baseClient, funcs)
	return client, nil
}

func wrapFakeSubResourcePatch(f *fakeClientCallSpec, baseK8sClient client.Client) func(context.Context, client.Client, string, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	if f == nil {
		panic("Nil pointer passed to wrapFakeSubResourcePatch")
	}
	return func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
		if *f != nil {
			fakeUsage, err := (*f)(obj)
			if fakeUsage == emitResponse {
				return err
			}
		}
		switch subResourceName {
		case "status":
			return baseK8sClient.Status().Patch(ctx, obj, patch, opts...)
		default:
			return fmt.Errorf("Unsupported subresource: %s", subResourceName)
		}
	}
}
