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
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadleaderworkerset "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("MultiKueue LeaderWorkerSet", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture

	ginkgo.BeforeAll(func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			enabledIntegrations := defaultEnabledIntegrations.Union(sets.New(workloadleaderworkerset.FrameworkName))
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, enabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
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
		group0Key := behavioral.WorkloadKeyForLeaderWorkerSet(lws, "0")
		group1Key := behavioral.WorkloadKeyForLeaderWorkerSet(lws, "1")

		admission := utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
			utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Obj(),
		)

		admitWorkloadAndCheckWorkerCopies(f.multiKueueAC.Name, group0Key, admission)

		ginkgo.By("admitting the second group on the worker of the first one", func() {
			behavioral.SetQuotaReservation(managerTestCluster.ctx, managerTestCluster.client, group1Key, admission.Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, group1Key, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			behavioral.SetQuotaReservation(worker2TestCluster.ctx, worker2TestCluster.client, group1Key, admission.Obj())
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

		ginkgo.By("checking the LeaderWorkerSet of the remaining group is kept on worker2", func() {
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
