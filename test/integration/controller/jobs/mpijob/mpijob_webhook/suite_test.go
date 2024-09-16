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

package mpijob_webhook

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
	"sigs.k8s.io/kueue/test/integration/framework"
)

var (
	cfg         *rest.Config
	k8sClient   client.Client
	ctx         context.Context
	fwk         *framework.Framework
	crdPath     = filepath.Join("..", "..", "..", "..", "..", "..", "config", "components", "crd", "bases")
	mpiCrdPath  = filepath.Join("..", "..", "..", "..", "..", "..", "dep-crds", "mpi-operator")
	webhookPath = filepath.Join("..", "..", "..", "..", "..", "..", "config", "components", "webhook")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"MPIJob Webhook Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{
		CRDPath:     crdPath,
		DepCRDPaths: []string{mpiCrdPath},
		WebhookPath: webhookPath,
	}

	cfg = fwk.Init()
	ctx, k8sClient = fwk.RunManager(cfg, managerSetup(false))
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(setupJobManager bool, opts ...jobframework.Option) framework.ManagerSetup {
	return func(ctx context.Context, mgr manager.Manager) {
		reconciler := mpijob.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = mpijob.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = reconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = mpijob.SetupMPIJobWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		jobframework.EnableIntegration(mpijob.FrameworkName)

		if setupJobManager {
			jobReconciler := job.NewReconciler(
				mgr.GetClient(),
				mgr.GetEventRecorderFor(constants.JobControllerName),
				opts...)
			err = job.SetupIndexes(ctx, mgr.GetFieldIndexer())
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = jobReconciler.SetupWithManager(mgr)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			err = job.SetupWebhook(mgr, opts...)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
		}
	}
}
