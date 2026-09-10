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

package multikueue

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadappwrapper "sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("MultiKueue AppWrapper", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		f = setupMultiKueueFixture()
	})

	ginkgo.AfterEach(func() {
		f.teardown()
	})

	ginkgo.It("Should run an appwrapper on worker if admitted", func() {
		aw := testingaw.MakeAppWrapper("aw", f.managerNs.Name).
			Component(testingaw.Component{
				Template: testingjob.MakeJob("job-1", f.managerNs.Name).SetTypeMeta().Parallelism(1).Obj(),
			}).
			Queue(f.managerLq.Name).
			ManagedBy(kueue.MultiKueueControllerName).
			Obj()

		util.MustCreate(managerTestCluster.ctx, managerTestCluster.client, aw)
		wlLookupKey := types.NamespacedName{Name: workloadappwrapper.GetWorkloadNameForAppWrapper(aw.Name, aw.UID), Namespace: f.managerNs.Name}

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment("aw-0").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, wlLookupKey, admission)

		ginkgo.By("changing the status of the appwrapper in the worker, updates the manager's appwrappers status", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := awv1beta2.AppWrapper{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(aw), &createdAppWrapper)).To(gomega.Succeed())
				createdAppWrapper.Status.Phase = awv1beta2.AppWrapperRunning
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdAppWrapper)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := awv1beta2.AppWrapper{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(aw), &createdAppWrapper)).To(gomega.Succeed())
				g.Expect(createdAppWrapper.Status.Phase).To(gomega.Equal(awv1beta2.AppWrapperRunning))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("finishing the worker appwrapper, the manager's wl is marked as finished and the worker2 wl removed", func() {
			finishJobReason := "AppWrapper finished successfully"
			gomega.Eventually(func(g gomega.Gomega) {
				createdAppWrapper := awv1beta2.AppWrapper{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, client.ObjectKeyFromObject(aw), &createdAppWrapper)).To(gomega.Succeed())
				createdAppWrapper.Status.Phase = awv1beta2.AppWrapperSucceeded
				g.Expect(worker2TestCluster.client.Status().Update(worker2TestCluster.ctx, &createdAppWrapper)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			waitForWorkloadToFinishAndRemoteWorkloadToBeDeleted(wlLookupKey, finishJobReason)
		})
	})
})
