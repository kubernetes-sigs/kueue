/*
Copyright 2021 The Kubernetes Authors.

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

package jobs

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg                  *rest.Config
	k8sClient            client.Client
	serverVersionFetcher *kubeversion.ServerVersionFetcher
	ctx                  context.Context
	fwk                  *framework.Framework
	crdPath              = filepath.Join("..", "..", "..", "..", "config", "components", "crd", "bases")
	webhookPath          = filepath.Join("..", "..", "..", "..", "config", "components", "webhook")
	mpiCrdPath           = filepath.Join("..", "..", "..", "..", "dep-crds", "mpi-operator")
	jobsetCrdPath        = filepath.Join("..", "..", "..", "..", "dep-crds", "jobset-operator")
	rayCrdPath           = filepath.Join("..", "..", "..", "..", "dep-crds", "ray-operator")
	kubeflowCrdPath      = filepath.Join("..", "..", "..", "..", "dep-crds", "training-operator-crds")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Jobs Webhook Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		CRDPath:     crdPath,
		DepCRDPaths: []string{jobsetCrdPath, mpiCrdPath, rayCrdPath, kubeflowCrdPath},
		WebhookPath: webhookPath,
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(setup func(ctrl.Manager, ...jobframework.Option) error, opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		cCache := cache.New(mgr.GetClient())
		queues := queue.NewManager(mgr.GetClient(), cCache)
		opts = append(opts, jobframework.WithCache(cCache), jobframework.WithQueues(queues))

		err := setup(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobframework.EnableIntegration(job.FrameworkName)
	}
}
