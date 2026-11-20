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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	workloadfinish "sigs.k8s.io/kueue/pkg/workload/finish"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("MultiKueue ElasticJob partial scale-up", ginkgo.Label("area:multikueue", "feature:multikueue", "job:ray"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	const workersGroup = "workers-group-0"
	var (
		f                  *multiKueueFixture
		rayCluster         *rayv1.RayCluster
		initial, expansion *kueue.Workload
		remoteUID          types.UID
	)

	// This suite does not run schedulers. Drive their admission/hand-over writes
	// through the API; all MultiKueue and job reconciliation runs through watches.
	admitSlice := func(c *cluster, wl, predecessor *kueue.Workload, workers int32) {
		ginkgo.GinkgoHelper()
		key := client.ObjectKeyFromObject(wl)
		if predecessor != nil {
			gomega.Eventually(func(g gomega.Gomega) {
				old := &kueue.Workload{}
				g.Expect(c.client.Get(c.ctx, client.ObjectKeyFromObject(predecessor), old)).To(gomega.Succeed())
				next := &kueue.Workload{}
				g.Expect(c.client.Get(c.ctx, key, next)).To(gomega.Succeed())
				if old.Status.ClusterName != nil && next.Status.ClusterName == nil {
					next.Status.ClusterName = old.Status.ClusterName
					g.Expect(c.client.Status().Update(c.ctx, next)).To(gomega.Succeed())
				}
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
		integration.SetQuotaReservation(c.ctx, c.client, key,
			utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(f.managerCq.Name)).PodSets(
				utiltestingapi.MakePodSetAssignment("head").Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(1).Obj(),
				utiltestingapi.MakePodSetAssignment(workersGroup).Flavor(corev1.ResourceCPU, multikueueTestFlavor).Count(workers).Obj(),
			).Obj())
		if predecessor != nil {
			gomega.Eventually(func(g gomega.Gomega) {
				old := &kueue.Workload{}
				g.Expect(c.client.Get(c.ctx, client.ObjectKeyFromObject(predecessor), old)).To(gomega.Succeed())
				g.Expect(workloadfinish.Finish(c.ctx, c.client, old, kueue.WorkloadSliceReplaced, "Replaced by the next admitted slice", behavioral.RealClock)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}
	}
	expectRemoteCount := func(wl *kueue.Workload, count int32) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			remote := &kueue.Workload{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(wl), remote)).To(gomega.Succeed())
			g.Expect(remote.Spec.PodSets[1].Count).To(gomega.Equal(count))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}
	expectRunningSameCluster := func(active *kueue.Workload) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			remote := &rayv1.RayCluster{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(rayCluster), remote)).To(gomega.Succeed())
			g.Expect(remote.UID).To(gomega.Equal(remoteUID))
			g.Expect(remote.Spec.Suspend).To(gomega.Equal(new(false)))
			g.Expect(remote.Spec.WorkerGroupSpecs[0].Replicas).To(gomega.Equal(new(int32(6))))
			g.Expect(jobframework.PrebuiltWorkloadNameFor(remote)).To(gomega.Equal(active.Name))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithPartialReplicaScaleUp, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueRayInTreeAutoscaling, true)
		managerTestCluster.fwk.StartManagerForSpec(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})
	ginkgo.BeforeEach(func() {
		f = setupMultiKueueFixture()
		rayCluster = testingraycluster.MakeCluster("partial", f.managerNs.Name).
			SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			SetAnnotation(constants.ElasticJobScaleUpStrategyAnnotationKey, constants.ElasticJobScaleUpStrategyPartial).
			Queue(f.managerLq.Name).WithEnableAutoscaling(new(true)).FirstWorkerGroupReplicas(1, 0, 6).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, rayCluster)
		initial = &behavioral.ExpectWorkloadsInNamespace(managerTestCluster.ctx, managerTestCluster.client, f.managerNs.Name, 1)[0]
		admitSlice(&managerTestCluster, initial, nil, 1)
		expectRemoteCount(initial, 1)
		admitSlice(&worker1TestCluster, initial, nil, 1)
		behavioral.ExpectAdmissionCheckStateWithMessage(
			managerTestCluster.ctx,
			managerTestCluster.client,
			client.ObjectKeyFromObject(initial),
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		gomega.Eventually(func(g gomega.Gomega) {
			remote := &rayv1.RayCluster{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(rayCluster), remote)).To(gomega.Succeed())
			g.Expect(remote.Spec.Suspend).To(gomega.Equal(new(false)))
			remoteUID = remote.UID
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		ginkgo.By("requesting six workers on the worker, as the Ray autoscaler would")
		gomega.Eventually(func(g gomega.Gomega) {
			remote := &rayv1.RayCluster{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(rayCluster), remote)).To(gomega.Succeed())
			g.Expect(remote.Spec.Suspend).To(gomega.Equal(new(false)))
			remote.Spec.WorkerGroupSpecs[0].Replicas = new(int32(6))
			g.Expect(worker1TestCluster.client.Update(worker1TestCluster.ctx, remote)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expansion = behavioral.ExpectNewWorkloadSlice(managerTestCluster.ctx, managerTestCluster.client, initial)
		gomega.Expect(expansion.Spec.PodSets[1].Count).To(gomega.Equal(int32(6)))
		gomega.Expect(expansion.Spec.PodSets[1].MinCount).To(gomega.Equal(new(int32(1))))
	})
	ginkgo.AfterEach(func() { f.teardown() })

	ginkgo.It("Should continue probing when the worker admits fewer replicas than the manager", func() {
		ginkgo.By("reserving six workers on the manager but only two on the worker")
		admitSlice(&managerTestCluster, expansion, initial, 6)
		expectRemoteCount(expansion, 6)
		admitSlice(&worker1TestCluster, expansion, initial, 2)
		gomega.Eventually(func(g gomega.Gomega) {
			local := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(expansion), local)).To(gomega.Succeed())
			g.Expect(apimeta.IsStatusConditionTrue(local.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
			g.Expect(local.Spec.PodSets[1].Count).To(gomega.Equal(int32(2)))
			g.Expect(local.Status.Admission.PodSetAssignments[1].Count).To(gomega.Equal(new(int32(6))))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		ginkgo.By("creating a distinct probe without another Ray scaling event")
		probe := behavioral.ExpectNewWorkloadSlice(managerTestCluster.ctx, managerTestCluster.client, expansion)
		gomega.Expect(probe.Name).NotTo(gomega.Equal(expansion.Name))
		gomega.Expect(probe.Spec.PodSets[1].Count).To(gomega.Equal(int32(6)))
		ginkgo.By("reserving the probe on the manager while worker capacity remains unavailable")
		admitSlice(&managerTestCluster, probe, expansion, 6)
		expectRemoteCount(probe, 6)
		gomega.Consistently(func(g gomega.Gomega) {
			pending := &kueue.Workload{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(probe), pending)).To(gomega.Succeed())
			g.Expect(pending.Status.Admission).To(gomega.BeNil())
		}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		expectRunningSameCluster(expansion)

		ginkgo.By("granting the queued probe when worker capacity becomes available")
		admitSlice(&worker1TestCluster, probe, expansion, 6)
		behavioral.ExpectPodSetAdmittedCount(managerTestCluster.ctx, managerTestCluster.client, probe, workersGroup, 6)
		behavioral.ExpectPodSetAdmittedCount(worker1TestCluster.ctx, worker1TestCluster.client, probe, workersGroup, 6)
		behavioral.ExpectAdmissionCheckStateWithMessage(
			managerTestCluster.ctx,
			managerTestCluster.client,
			client.ObjectKeyFromObject(probe),
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		expectRunningSameCluster(probe)
	})

	ginkgo.It("Should bound worker admission by the manager grant and expand after a larger grant", func() {
		ginkgo.By("reserving only two of the six requested workers on the manager")
		admitSlice(&managerTestCluster, expansion, initial, 2)
		expectRemoteCount(expansion, 2)
		admitSlice(&worker1TestCluster, expansion, initial, 2)
		behavioral.ExpectAdmissionCheckStateWithMessage(
			managerTestCluster.ctx,
			managerTestCluster.client,
			client.ObjectKeyFromObject(expansion),
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		probe := behavioral.ExpectNewWorkloadSlice(managerTestCluster.ctx, managerTestCluster.client, expansion)
		gomega.Expect(probe.Spec.PodSets[1].Count).To(gomega.Equal(int32(6)))
		gomega.Consistently(func(g gomega.Gomega) {
			pending := &kueue.Workload{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(probe), pending)).To(gomega.Succeed())
			g.Expect(pending.Status.Admission).To(gomega.BeNil())
			remote := &kueue.Workload{}
			g.Expect(worker1TestCluster.client.Get(worker1TestCluster.ctx, client.ObjectKeyFromObject(expansion), remote)).To(gomega.Succeed())
			g.Expect(remote.Spec.PodSets[1].Count).To(gomega.Equal(int32(2)))
			g.Expect(remote.Status.Admission.PodSetAssignments[1].Count).To(gomega.Equal(new(int32(2))))
		}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		expectRunningSameCluster(expansion)

		ginkgo.By("granting the queued probe when manager capacity becomes available")
		admitSlice(&managerTestCluster, probe, expansion, 6)
		expectRemoteCount(probe, 6)
		admitSlice(&worker1TestCluster, probe, expansion, 6)
		behavioral.ExpectPodSetAdmittedCount(managerTestCluster.ctx, managerTestCluster.client, probe, workersGroup, 6)
		behavioral.ExpectPodSetAdmittedCount(worker1TestCluster.ctx, worker1TestCluster.client, probe, workersGroup, 6)
		behavioral.ExpectAdmissionCheckStateWithMessage(
			managerTestCluster.ctx,
			managerTestCluster.client,
			client.ObjectKeyFromObject(probe),
			f.multiKueueAC.Name,
			kueue.CheckStateReady,
			`The workload was admitted on "worker1"`,
		)
		expectRunningSameCluster(probe)
	})
})
