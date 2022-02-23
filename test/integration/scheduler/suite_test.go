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

package scheduler

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/onsi/ginkgo"
	"github.com/onsi/gomega"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/kueue/pkg/scheduler"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/capacity"
	"sigs.k8s.io/kueue/pkg/constants"
	kueuectrl "sigs.k8s.io/kueue/pkg/controller/core"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/workload/job"
	"sigs.k8s.io/kueue/pkg/queue"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var (
	cfg       *rest.Config
	k8sClient client.Client
	testEnv   *envtest.Environment
	ctx       context.Context
	cancel    context.CancelFunc
	sched     *scheduler.Scheduler
)

func TestAPIs(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecsWithDefaultAndCustomReporters(t,
		"Controller Suite",
		[]ginkgo.Reporter{printer.NewlineReporter{}})
}

var _ = ginkgo.BeforeSuite(func() {
	ctrl.SetLogger(zap.New(zap.WriteTo(ginkgo.GinkgoWriter), zap.UseDevMode(true)))

	ginkgo.By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "..", "..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(cfg).NotTo(gomega.BeNil())

	err = kueue.AddToScheme(scheme.Scheme)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
	gomega.Expect(k8sClient).NotTo(gomega.BeNil())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:             scheme.Scheme,
		MetricsBindAddress: "0", // disable metrics to avoid conflicts with other pkgs
	})
	gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to create manager")

	err = queue.SetupIndexes(mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = capacity.SetupIndexes(mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	queues := queue.NewManager(mgr.GetClient())
	cache := capacity.NewCache(mgr.GetClient())

	err = kueuectrl.NewQueueReconciler(queues).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = kueuectrl.NewCapacityReconciler(cache).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = kueuectrl.NewQueuedWorkloadReconciler(queues, cache).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjob.NewReconciler(mgr.GetScheme(), mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName)).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	ctx, cancel = context.WithCancel(context.TODO())
	sched = scheduler.New(queues, cache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.ManagerName))
	go func() {
		sched.Start(ctx)
	}()
	go func() {
		defer ginkgo.GinkgoRecover()
		err = mgr.Start(ctx)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "failed to run manager")
	}()
}, 60)

var _ = ginkgo.AfterSuite(func() {
	ginkgo.By("tearing down the test environment")
	cancel()
	err := testEnv.Stop()
	gomega.Expect(err).NotTo(gomega.HaveOccurred())
})
