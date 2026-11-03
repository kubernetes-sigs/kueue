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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadleaderworkerset "sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
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

	// admitTwoGroupsOnWorker2 creates a LeaderWorkerSet with two groups and admits both on worker2.
	admitTwoGroupsOnWorker2 := func() (client.ObjectKey, types.NamespacedName, types.NamespacedName, *leaderworkersetv1.LeaderWorkerSet) {
		ginkgo.GinkgoHelper()
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
		return lwsKey, group0Key, group1Key, remoteLws
	}

	ginkgo.It("Should keep the remote LeaderWorkerSet on a worker removed from the MultiKueueConfig until its last group is evicted", func() {
		lwsKey, group0Key, group1Key, remoteLws := admitTwoGroupsOnWorker2()

		ginkgo.By("removing worker2 from the MultiKueueConfig, both groups stay admitted", func() {
			f.updateConfigClusters(f.workerCluster1.Name)
			expectNoEviction([]types.NamespacedName{group0Key, group1Key}, f.multiKueueAC.Name, behavioral.LongConsistentDuration)
		})

		ginkgo.By("evicting the first group, only its remote workload is deleted", func() {
			evictWorkload(group0Key)
			waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, group0Key, "worker2", behavioral.Timeout)
			gomega.Consistently(func(g gomega.Gomega) {
				gotLws := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, gotLws)).To(gomega.Succeed())
				g.Expect(gotLws.UID).To(gomega.Equal(remoteLws.UID))
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, group1Key, &kueue.Workload{})).To(gomega.Succeed())
			}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("evicting the last group, the LeaderWorkerSet is deleted from worker2", func() {
			evictWorkload(group1Key)
			waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, group1Key, "worker2", behavioral.Timeout)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, &leaderworkersetv1.LeaderWorkerSet{})).To(utiltesting.BeNotFoundError())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should keep the remote LeaderWorkerSet until its last group is removed", func() {
		lwsKey, group0Key, group1Key, remoteLws := admitTwoGroupsOnWorker2()

		ginkgo.By("scaling the LeaderWorkerSet down to one group", func() {
			scaleLeaderWorkerSet(lwsKey, 1)
			waitForRemoteWorkloadToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, group1Key, "worker2", behavioral.Timeout)
		})

		ginkgo.By("checking the LeaderWorkerSet of the remaining group is kept on worker2 and shrunk to one group", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				gotLws := &leaderworkersetv1.LeaderWorkerSet{}
				g.Expect(worker2TestCluster.client.Get(worker2TestCluster.ctx, lwsKey, gotLws)).To(gomega.Succeed())
				g.Expect(gotLws.Spec.Replicas).To(gomega.Equal(ptr.To[int32](1)))
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
})

func evictWorkload(key types.NamespacedName) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		wl := &kueue.Workload{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, wl)).To(gomega.Succeed())
		g.Expect(workloadevict.Evict(
			managerTestCluster.ctx, managerTestCluster.client, &utiltesting.EventRecorder{}, wl,
			kueue.WorkloadEvictedByPreemption, "Evicted by the test", "", behavioral.RealClock, false, nil, nil,
		)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func scaleLeaderWorkerSet(key client.ObjectKey, replicas int32) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		lws := &leaderworkersetv1.LeaderWorkerSet{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, key, lws)).To(gomega.Succeed())
		lws.Spec.Replicas = &replicas
		g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, lws)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
