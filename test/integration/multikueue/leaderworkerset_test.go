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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	workloadleaderworkerset "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("MultiKueue LeaderWorkerSet", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			enabledIntegrations := defaultEnabledIntegrations.Union(sets.New(workloadleaderworkerset.FrameworkName))
			// A long worker lost timeout keeps the periodic requeue from syncing the
			// LeaderWorkerSet, so that the tests rely on the watch of the local one.
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, enabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce,
				multikueue.WithWorkerLostTimeout(time.Minute))
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

	ginkgo.It("Should keep the remote LeaderWorkerSet until its last group is removed", func() {
		lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", f.managerNs.Name).
			Queue(f.managerLq.Name).
			Replicas(2).
			Request(corev1.ResourceCPU, "1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lws)
		lwsKey := client.ObjectKeyFromObject(lws)
		group0Key := types.NamespacedName{Name: workloadleaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: lws.Namespace}
		group1Key := types.NamespacedName{Name: workloadleaderworkerset.GetWorkloadName(lws.UID, lws.Name, "1"), Namespace: lws.Namespace}

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, group0Key, admission)

		ginkgo.By("admitting the second group on the worker of the first one", func() {
			integration.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, group1Key, admission.Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, group1Key, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			integration.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, group1Key, admission.Obj())
			behavioral.ExpectAdmissionCheckStateWithMessage(
				managerTestCluster.ctx, managerTestCluster.client, group1Key,
				f.multiKueueAC.Name,
				kueue.CheckStateReady,
				`The workload was admitted on "worker2"`,
			)
		})

		remoteLws := &leaderworkersetv1.LeaderWorkerSet{}
		ginkgo.By("checking the LeaderWorkerSet is created on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, remoteLws)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("scaling the LeaderWorkerSet down to one group", func() {
			scaleLeaderWorkerSet(lwsKey, 1)
			waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, group1Key, "worker2", behavioral.Timeout)
		})

		ginkgo.By("checking the LeaderWorkerSet of the remaining group is kept on worker2 and shrunk to one group", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				gotLws := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, gotLws)).To(gomega.Succeed())
				g.Expect(gotLws.Spec.Replicas).To(gomega.Equal(new(int32(1))))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(func(g gomega.Gomega) {
				gotLws := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, gotLws)).To(gomega.Succeed())
				g.Expect(gotLws.UID).To(gomega.Equal(remoteLws.UID))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, group0Key, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("scaling the LeaderWorkerSet down to zero groups, the LeaderWorkerSet is deleted from worker2", func() {
			scaleLeaderWorkerSet(lwsKey, 0)
			waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, group0Key, "worker2", behavioral.Timeout)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, &leaderworkersetv1.LeaderWorkerSet{})).To(utiltesting.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should copy the changed init containers to the remote LeaderWorkerSet", func() {
		lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", f.managerNs.Name).
			Queue(f.managerLq.Name).
			Request(corev1.ResourceCPU, "1").
			RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, lws)
		lwsKey := client.ObjectKeyFromObject(lws)
		group0Key := types.NamespacedName{Name: workloadleaderworkerset.GetWorkloadName(lws.UID, lws.Name, "0"), Namespace: lws.Namespace}

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, group0Key, admission)

		remoteLws := &leaderworkersetv1.LeaderWorkerSet{}
		ginkgo.By("checking the LeaderWorkerSet is created on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, remoteLws)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		initContainer := *utiltesting.MakeContainer().Name("init").Image("pause").WithResourceReq(corev1.ResourceCPU, "500m").Obj()
		ginkgo.By("adding an init container within the effective requests on the manager", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				managerLws := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, lwsKey, managerLws)).To(gomega.Succeed())
				managerLws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.InitContainers = []corev1.Container{initContainer}
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, managerLws)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking the init container is copied to the LeaderWorkerSet on worker2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				gotLws := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, gotLws)).To(gomega.Succeed())
				g.Expect(gotLws.UID).To(gomega.Equal(remoteLws.UID))
				g.Expect(gotLws.Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.InitContainers).To(gomega.BeComparableTo([]corev1.Container{initContainer}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})

func scaleLeaderWorkerSet(key client.ObjectKey, replicas int32) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		lws := &leaderworkersetv1.LeaderWorkerSet{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, lws)).To(gomega.Succeed())
		lws.Spec.Replicas = &replicas
		g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, lws)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
