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

package multikueue

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
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	//+kubebuilder:scaffold:imports
)

var (
	leaderCfg       *rest.Config
	k8sLeaderClient client.Client
	leaderCtx       context.Context
	leaderFwk       *framework.Framework

	workerCfg       *rest.Config
	k8sWorkerClient client.Client
	workerCtx       context.Context
	workerFwk       *framework.Framework
)

func TestScheduler(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Multikueue Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	leaderFwk = &framework.Framework{
		CRDPath:     filepath.Join("..", "..", "..", "config", "components", "crd", "bases"),
		WebhookPath: filepath.Join("..", "..", "..", "config", "components", "webhook"),
	}
	leaderCfg = leaderFwk.Init()
	leaderCtx, k8sLeaderClient = leaderFwk.RunManager(leaderCfg, managerSetup)

	workerFwk = &framework.Framework{
		CRDPath:     filepath.Join("..", "..", "..", "config", "components", "crd", "bases"),
		WebhookPath: filepath.Join("..", "..", "..", "config", "components", "webhook"),
	}
	workerCfg = workerFwk.Init()
	workerCtx, k8sWorkerClient = workerFwk.RunManager(workerCfg, managerSetup)
})

var _ = ginkgo.AfterSuite(func() {
	workerFwk.Teardown()
	leaderFwk.Teardown()
})

func managerSetup(mgr manager.Manager, ctx context.Context) {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	cCache := cache.New(mgr.GetClient())
	queues := queue.NewManager(mgr.GetClient(), cCache)

	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &config.Configuration{})
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)

	failedWebhook, err := webhooks.Setup(mgr)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	err = workloadjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}
