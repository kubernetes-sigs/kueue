/*
Copyright 2023 The Kubernetes Authors.

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

package mxjob

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/kubeflow/jobs/mxjob"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg          *rest.Config
	k8sClient    client.Client
	ctx          context.Context
	fwk          *framework.Framework
	crdPath      = filepath.Join("..", "..", "..", "..", "..", "config", "components", "crd", "bases")
	mxnetCrdPath = filepath.Join("..", "..", "..", "..", "..", "dep-crds", "training-operator-crds")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"MXJob Controller Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		CRDPath:     crdPath,
		DepCRDPaths: []string{mxnetCrdPath},
	}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.SetupClient(cfg)
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		reconciler := mxjob.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err := mxjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = reconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = mxjob.SetupMXJobWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}

func managerAndSchedulerSetup(setupTASControllers bool, opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		managerSetup(opts...)(ctx, mgr)

		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := cache.New(mgr.GetClient())
		queues := queue.NewManager(mgr.GetClient(), cCache)

		configuration := &config.Configuration{}
		mgr.GetScheme().Default(configuration)

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, configuration)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		if setupTASControllers {
			failedCtrl, err = tas.SetupControllers(mgr, queues, cCache, configuration)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "TAS controller", failedCtrl)

			err = tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}

		sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
