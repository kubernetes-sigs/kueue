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
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/envtest/printer"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/kueue/pkg/scheduler"

	"sigs.k8s.io/kueue/pkg/capacity"
	"sigs.k8s.io/kueue/pkg/constants"
	kueuectrl "sigs.k8s.io/kueue/pkg/controller/core"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/workload/job"
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

func TestScheduler(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)

	ginkgo.RunSpecsWithDefaultAndCustomReporters(t,
		"Scheduler Suite",
		[]ginkgo.Reporter{printer.NewlineReporter{}})
}

var _ = ginkgo.BeforeSuite(func() {
	ctx, cancel = context.WithCancel(context.Background())
	crdPath := filepath.Join("..", "..", "..", "config", "crd", "bases")
	cfg, k8sClient, testEnv = framework.BeforeSuite(ctx, crdPath, managerAndSchedulerSetup)
}, 60)

var _ = ginkgo.AfterSuite(func() {
	cancel()
	framework.AfterSuite(testEnv)
})

func managerAndSchedulerSetup(mgr manager.Manager) {
	err := queue.SetupIndexes(mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = capacity.SetupIndexes(mgr.GetFieldIndexer())
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	queues := queue.NewManager(mgr.GetClient())
	cache := capacity.NewCache(mgr.GetClient())

	err = kueuectrl.NewQueueReconciler(queues).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = kueuectrl.NewCapacityReconciler(mgr.GetClient(), cache).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = kueuectrl.NewQueuedWorkloadReconciler(queues, cache).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	err = workloadjob.NewReconciler(mgr.GetScheme(), mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.JobControllerName)).SetupWithManager(mgr)
	gomega.Expect(err).NotTo(gomega.HaveOccurred())

	sched := scheduler.New(queues, cache, mgr.GetClient(), mgr.GetEventRecorderFor(constants.ManagerName))
	go func() {
		sched.Start(ctx)
	}()
}
