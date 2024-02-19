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

package raycluster

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
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/test/integration/framework"
	//+kubebuilder:scaffold:imports
)

var (
	cfg         *rest.Config
	k8sClient   client.Client
	ctx         context.Context
	fwk         *framework.Framework
	crdPath     = filepath.Join("..", "..", "..", "..", "..", "config", "components", "crd", "bases")
	rayCrdPath  = filepath.Join("..", "..", "..", "..", "..", "dep-crds", "ray-operator")
	webhookPath = filepath.Join("..", "..", "..", "..", "..", "config", "components", "webhook")
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"RayCluster Controller Suite",
	)
}

func managerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(mgr manager.Manager, ctx context.Context) {
		reconciler := raycluster.NewReconciler(
			mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName),
			opts...)
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = raycluster.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = reconciler.SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = raycluster.SetupRayClusterWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}

func managerAndSchedulerSetup(opts ...jobframework.Option) framework.ManagerSetup {
	return func(mgr manager.Manager, ctx context.Context) {
		err := indexer.Setup(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		cCache := cache.New(mgr.GetClient())
		queues := queue.NewManager(mgr.GetClient(), cCache)

		failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &config.Configuration{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

		err = raycluster.SetupIndexes(ctx, mgr.GetFieldIndexer())
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = raycluster.NewReconciler(mgr.GetClient(),
			mgr.GetEventRecorderFor(constants.JobControllerName), opts...).SetupWithManager(mgr)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		err = raycluster.SetupRayClusterWebhook(mgr, opts...)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		sched := scheduler.New(queues, cCache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.AdmissionName))
		err = sched.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
	}
}
