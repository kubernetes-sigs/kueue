/*
Copyright 2022 The Kubernetes Authors.

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

package core

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/test/integration/framework"
	//+kubebuilder:scaffold:imports
)

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecs(t,
		"Core Controllers Suite",
	)
}

var _ = ginkgo.BeforeSuite(func() {
	ctx, cancel = context.WithCancel(context.Background())
	crdPath := filepath.Join("..", "..", "..", "..", "config", "crd", "bases")
	cfg, k8sClient, testEnv = framework.BeforeSuite(ctx, crdPath, managerSetup)
})

var _ = ginkgo.AfterSuite(func() {
	cancel()
	framework.AfterSuite(testEnv)
})

func managerSetup(mgr manager.Manager) {
	err := queue.SetupIndexes(mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = cache.SetupIndexes(mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	queues := queue.NewManager(mgr.GetClient())
	cCache := cache.New(mgr.GetClient())

	err = core.NewQueueReconciler(mgr.GetClient(), queues).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = core.NewClusterQueueReconciler(mgr.GetClient(), queues, cCache).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = core.NewQueuedWorkloadReconciler(queues, cCache).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
}
