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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("MultiKueue cluster status metrics", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var f *multiKueueFixture
	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueMetricForClusterByStatus, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DQOMultiKueueCapacityProvider, false)
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})
	ginkgo.AfterAll(func() { managerTestCluster.fwk.StopManager(managerTestCluster.ctx) })
	ginkgo.BeforeEach(func() {
		f = setupMultiKueueFixture()
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster1.Name: metav1.ConditionTrue, f.workerCluster2.Name: metav1.ConditionTrue})
	})
	ginkgo.AfterEach(func() {
		f.teardown()
		gomega.Eventually(func() []testingmetrics.MetricDataPoint {
			return testingmetrics.CollectFilteredGaugeVec(metrics.MultiKueueClusterByStatus, nil)
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeEmpty())
	})

	ginkgo.It("Should report connection loss and recovery without quota automation", func() {
		before := &kueue.ClusterQueue{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), before)).To(gomega.Succeed())
		restore := behavioral.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, managersConfigNamespace.Name)
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster1.Name: metav1.ConditionFalse, f.workerCluster2.Name: metav1.ConditionTrue})
		restore()
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster1.Name: metav1.ConditionTrue, f.workerCluster2.Name: metav1.ConditionTrue})
		after := &kueue.ClusterQueue{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), after)).To(gomega.Succeed())
		gomega.Expect(after.Spec).To(gomega.Equal(before.Spec))
	})

	ginkgo.It("Should remove only the deleted ClusterQueue's series for shared workers", func() {
		second := utiltestingapi.MakeClusterQueue("metrics-second").AdmissionChecks(kueue.AdmissionCheckReference(f.multiKueueAC.Name)).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, second)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, second, true)
		})
		expectWorkerStatusMetrics(second.Name, map[string]metav1.ConditionStatus{f.workerCluster1.Name: metav1.ConditionTrue, f.workerCluster2.Name: metav1.ConditionTrue})
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, second, true)
		expectNoWorkerStatusMetrics(map[string]string{"cluster_queue": second.Name})
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster1.Name: metav1.ConditionTrue, f.workerCluster2.Name: metav1.ConditionTrue})
	})

	ginkgo.It("Should remove series when the ClusterQueue stops using MultiKueue", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), cq)).To(gomega.Succeed())
			cq.Spec.AdmissionChecksStrategy = nil
			g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectNoWorkerStatusMetrics(map[string]string{"cluster_queue": f.managerCq.Name})
	})

	ginkgo.It("Should replace series when the config's workers change", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			cfg := &kueue.MultiKueueConfig{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerMultiKueueConfig), cfg)).To(gomega.Succeed())
			cfg.Spec.Clusters = []string{f.workerCluster2.Name}
			g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, cfg)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster2.Name: metav1.ConditionTrue})
	})

	ginkgo.It("Should replace series when the AdmissionCheck switches configs", func() {
		cfg := utiltestingapi.MakeMultiKueueConfig("metrics-second").Clusters(f.workerCluster2.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, cfg)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cfg, true)
		})
		gomega.Eventually(func(g gomega.Gomega) {
			ac := &kueue.AdmissionCheck{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.multiKueueAC), ac)).To(gomega.Succeed())
			ac.Spec.Parameters.Name = cfg.Name
			g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, ac)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster2.Name: metav1.ConditionTrue})
	})

	ginkgo.It("Should replace series when the ClusterQueue switches AdmissionChecks", func() {
		cfg := utiltestingapi.MakeMultiKueueConfig("metrics-second").Clusters(f.workerCluster2.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, cfg)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cfg, true)
		})
		ac := utiltestingapi.MakeAdmissionCheck("metrics-second").ControllerName(kueue.MultiKueueControllerName).
			Parameters(kueue.SchemeGroupVersion.Group, "MultiKueueConfig", cfg.Name).Obj()
		behavioral.CreateAdmissionChecksAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, ac)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, ac, true)
		})
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), cq)).To(gomega.Succeed())
			cq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{AdmissionChecks: []kueue.AdmissionCheckStrategyRule{{Name: kueue.AdmissionCheckReference(ac.Name)}}}
			g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster2.Name: metav1.ConditionTrue})
	})

	ginkgo.It("Should keep reporting workers until the terminating AdmissionCheck is unlinked", func() {
		// AdmissionChecks remain protected by their finalizer while a ClusterQueue uses them.
		gomega.Expect(managerTestCluster.client.Delete(managerTestCluster.ctx, f.multiKueueAC)).To(gomega.Succeed())
		ac := &kueue.AdmissionCheck{}
		gomega.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.multiKueueAC), ac)).To(gomega.Succeed())
		gomega.Expect(ac.DeletionTimestamp.IsZero()).To(gomega.BeFalse())
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster1.Name: metav1.ConditionTrue, f.workerCluster2.Name: metav1.ConditionTrue})
		gomega.Eventually(func(g gomega.Gomega) {
			cq := &kueue.ClusterQueue{}
			g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), cq)).To(gomega.Succeed())
			cq.Spec.AdmissionChecksStrategy = nil
			g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, cq)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.multiKueueAC, true)
		expectNoWorkerStatusMetrics(map[string]string{"cluster_queue": f.managerCq.Name})
	})

	ginkgo.It("Should remove series when the config is deleted", func() {
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerMultiKueueConfig, true)
		expectNoWorkerStatusMetrics(map[string]string{"cluster_queue": f.managerCq.Name})
	})

	ginkgo.It("Should omit deleted workers and report them again after recreation", func() {
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, true)
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{f.workerCluster2.Name: metav1.ConditionTrue})
		worker := utiltestingapi.MakeMultiKueueCluster(f.workerCluster1.Name).KubeConfig(kueue.SecretLocationType, f.managerMultiKueueSecret1.Name).Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, worker)
		expectWorkerStatusMetrics(f.managerCq.Name, map[string]metav1.ConditionStatus{worker.Name: metav1.ConditionTrue, f.workerCluster2.Name: metav1.ConditionTrue})
	})
})

var _ = ginkgo.Describe("MultiKueue cluster status metrics disabled", ginkgo.Label("area:multikueue", "feature:multikueue"), func() {
	ginkgo.It("Should not report worker statuses with the gate disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.MultiKueueMetricForClusterByStatus, false)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DQOMultiKueueCapacityProvider, false)
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
		ginkgo.DeferCleanup(func() { managerTestCluster.fwk.StopManager(managerTestCluster.ctx) })
		f := setupMultiKueueFixture()
		ginkgo.DeferCleanup(f.teardown)
		gomega.Consistently(func() []testingmetrics.MetricDataPoint {
			return testingmetrics.CollectFilteredGaugeVec(metrics.MultiKueueClusterByStatus, nil)
		}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.BeEmpty())
	})
})

func expectWorkerStatusMetrics(cq string, statuses map[string]metav1.ConditionStatus) {
	ginkgo.GinkgoHelper()
	want := make([]testingmetrics.MetricDataPoint, 0, len(statuses)*len(metrics.ConditionStatusValues))
	for cluster, status := range statuses {
		for _, active := range metrics.ConditionStatusValues {
			value := 0.0
			if active == status {
				value = 1
			}
			want = append(
				want,
				testingmetrics.MetricDataPoint{Labels: map[string]string{"cluster_queue": cq, "cluster": cluster, "active": string(active), "replica_role": roletracker.RoleStandalone}, Value: value},
			)
		}
	}
	gomega.Eventually(func() []testingmetrics.MetricDataPoint {
		return testingmetrics.CollectFilteredGaugeVec(metrics.MultiKueueClusterByStatus, map[string]string{"cluster_queue": cq})
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.ConsistOf(want))
}

func expectNoWorkerStatusMetrics(labels map[string]string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func() []testingmetrics.MetricDataPoint {
		return testingmetrics.CollectFilteredGaugeVec(metrics.MultiKueueClusterByStatus, labels)
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeEmpty())
}
