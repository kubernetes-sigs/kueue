/*
Copyright 2024 The Kubernetes Authors.

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

package kueuectl

import (
	"context"
	"os"
	"path"
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
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/integration/framework"
	// +kubebuilder:scaffold:imports
)

var (
	cfg         *rest.Config
	k8sClient   client.Client
	ctx         context.Context
	fwk         *framework.Framework
	crdPath     = filepath.Join("..", "..", "..", "config", "components", "crd", "bases")
	webhookPath = filepath.Join("..", "..", "..", "config", "components", "webhook")

	kueuectlPath   string
	kassetsPath    string
	kubeconfigPath string
)

func TestKueuectl(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "Kueuectl Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	fwk = &framework.Framework{CRDPath: crdPath, WebhookPath: webhookPath}
	cfg = fwk.Init()
	ctx, k8sClient = fwk.RunManager(cfg, managerSetup)

	kassetsPath = os.Getenv("KUBEBUILDER_ASSETS")
	kueuectlPath = path.Join(os.Getenv("KUEUE_BIN"), "kubectl-kueue")
	kubeconfigPath = path.Join(ginkgo.GinkgoT().TempDir(), "testing.kubeconfig")
	configBites, err := utiltesting.RestConfigToKubeConfig(cfg)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(os.WriteFile(kubeconfigPath, configBites, 0666)).To(gomega.Succeed())
})

var _ = ginkgo.AfterSuite(func() {
	fwk.Teardown()
})

func managerSetup(mgr manager.Manager, ctx context.Context) {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	failedWebhook, err := webhooks.Setup(mgr)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

	controllersCfg := &config.Configuration{}
	mgr.GetScheme().Default(controllersCfg)

	controllersCfg.Metrics.EnableClusterQueueResources = true
	controllersCfg.QueueVisibility = &config.QueueVisibility{
		UpdateIntervalSeconds: 2,
		ClusterQueues: &config.ClusterQueueVisibility{
			MaxCount: 3,
		},
	}

	cCache := cache.New(mgr.GetClient())
	queues := queue.NewManager(mgr.GetClient(), cCache)

	failedCtrl, err := core.SetupControllers(mgr, queues, cCache, controllersCfg)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "controller", failedCtrl)
}
