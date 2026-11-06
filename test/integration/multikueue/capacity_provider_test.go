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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

const (
	mkCapacityProviderName       = "mk-cp"
	mkCapacityProviderConfigName = "mk-cp-config"
)

var _ = ginkgo.Describe("MultiKueue CapacityProvider", ginkgo.Label("area:multikueue", "feature:multikueue"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		f                    *multiKueueFixture
		providerConfig       *kueuealpha.MultiKueueCapacityProviderConfig
		extraProviderConfigs []*kueuealpha.MultiKueueCapacityProviderConfig
		cp                   *kueuealpha.CapacityProvider
		dqo                  *kueuealpha.DynamicQuotaOrchestrator
		extraManagerCqs      []*kueue.ClusterQueue
		extraWorker1Cqs      []*kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DQOMultiKueueCapacityProvider, true)
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
	})

	ginkgo.AfterAll(func() {
		managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
	})

	ginkgo.BeforeEach(func() {
		f = setupMultiKueueFixture()
		providerConfig, cp = createMultiKueueCapacityProvider(f)

		ginkgo.By("checking that the provider publishes the sum of the worker quotas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, client.ObjectKeyFromObject(cp), "10")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.AfterEach(func() {
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, dqo, true)
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cp, true)
		for _, extraCfg := range extraProviderConfigs {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, extraCfg, true)
		}
		behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, providerConfig, true)
		for _, cq := range extraManagerCqs {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cq, true)
		}
		for _, cq := range extraWorker1Cqs {
			behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, cq, true)
		}
		f.teardown()
		dqo, cp, providerConfig = nil, nil, nil
		extraProviderConfigs, extraManagerCqs, extraWorker1Cqs = nil, nil, nil
	})

	ginkgo.It("Should update status.capacity when worker or manager queues and provider config change", func() {
		cpKey := client.ObjectKeyFromObject(cp)

		ginkgo.By("decreasing the worker1 ClusterQueue quota", func() {
			updateWorkerClusterQueueCPU(worker1TestCluster, f.worker1Cq, "3")
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting and recreating the worker1 LocalQueue", func() {
			behavioral.ExpectObjectToBeDeleted(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Lq, true)
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "5")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			f.worker1Lq = utiltestingapi.MakeLocalQueue(f.worker1Cq.Name, f.worker1Ns.Name).ClusterQueue(f.worker1Cq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, f.worker1Lq)
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting and recreating the manager LocalQueue", func() {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, f.managerLq, true)
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "0")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			f.managerLq = utiltestingapi.MakeLocalQueue(f.managerCq.Name, f.managerNs.Name).ClusterQueue(f.managerCq.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, f.managerLq)
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting the worker2 ClusterQueue while its LocalQueue still references it", func() {
			behavioral.ExpectObjectToBeDeleted(worker2TestCluster.ctx, worker2TestCluster.client, f.worker2Cq, true)
			f.worker2Cq = nil
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "3")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("retargeting MultiKueueCapacityProviderConfig to another manager ClusterQueue", func() {
			managerCq2 := utiltestingapi.MakeClusterQueue("q2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "5").Obj()).
				AdmissionChecks(kueue.AdmissionCheckReference(f.multiKueueAC.Name)).
				Obj()
			extraManagerCqs = append(extraManagerCqs, managerCq2)
			behavioral.CreateClusterQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerCq2)

			managerLq2 := utiltestingapi.MakeLocalQueue(managerCq2.Name, f.managerNs.Name).ClusterQueue(managerCq2.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(managerTestCluster.ctx, managerTestCluster.client, managerLq2)

			worker1Cq2 := utiltestingapi.MakeClusterQueue("q2").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "6").Obj()).
				Obj()
			extraWorker1Cqs = append(extraWorker1Cqs, worker1Cq2)
			behavioral.CreateClusterQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Cq2)

			worker1Lq2 := utiltestingapi.MakeLocalQueue(worker1Cq2.Name, f.worker1Ns.Name).ClusterQueue(worker1Cq2.Name).Obj()
			behavioral.CreateLocalQueuesAndWaitForActive(worker1TestCluster.ctx, worker1TestCluster.client, worker1Lq2)

			gomega.Eventually(func(g gomega.Gomega) {
				latestCfg := &kueuealpha.MultiKueueCapacityProviderConfig{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(providerConfig), latestCfg)).To(gomega.Succeed())
				latestCfg.Spec.ManagerSubtreeRootRef.Name = managerCq2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, latestCfg)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "6")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should use the worker ClusterQueue status.effectiveQuotas when set", func() {
		cpKey := client.ObjectKeyFromObject(cp)

		ginkgo.By("setting status.effectiveQuotas on the worker1 ClusterQueue", func() {
			setWorkerClusterQueueEffectiveQuotas(worker1TestCluster, f.worker1Cq, utiltestingapi.MakeEffectiveQuotaStatus().
				ResourceGroups(utiltestingapi.ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, "7").Obj(),
				)).
				Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "12")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting empty status.effectiveQuotas.resourceGroups on worker1 so it contributes 0", func() {
			setWorkerClusterQueueEffectiveQuotas(worker1TestCluster, f.worker1Cq, utiltestingapi.MakeEffectiveQuotaStatus().Obj())
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "5")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("clearing status.effectiveQuotas on worker1 so the provider falls back to spec", func() {
			setWorkerClusterQueueEffectiveQuotas(worker1TestCluster, f.worker1Cq, nil)
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "10")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should update the manager ClusterQueue status.effectiveQuotas through the DynamicQuotaOrchestrator", func() {
		cpKey := client.ObjectKeyFromObject(cp)
		managerCqKey := client.ObjectKeyFromObject(f.managerCq)

		ginkgo.By("creating a DynamicQuotaOrchestrator for the manager ClusterQueue", func() {
			dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("mk-dqo").
				DiscoveryProvider(cp.Name, nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, f.managerCq.Name).
				Obj()
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, dqo)
			gomega.Eventually(func(g gomega.Gomega) {
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "10")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("decreasing the worker1 ClusterQueue quota", func() {
			updateWorkerClusterQueueCPU(worker1TestCluster, f.worker1Cq, "3")
			gomega.Eventually(func(g gomega.Gomega) {
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		var providerConfig2 *kueuealpha.MultiKueueCapacityProviderConfig
		ginkgo.By("updating CapacityProvider.spec.parameters without changing capacity so ObservedGeneration advances", func() {
			providerConfig2 = utiltestingalpha.MakeMultiKueueCapacityProviderConfig("mk-cp-config-2").
				ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, f.managerCq.Name).
				Obj()
			extraProviderConfigs = append(extraProviderConfigs, providerConfig2)
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, providerConfig2)

			var prevGen int64
			gomega.Eventually(func(g gomega.Gomega) {
				latestCp := &kueuealpha.CapacityProvider{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, latestCp)).To(gomega.Succeed())
				prevGen = latestCp.Generation
				latestCp.Spec.Parameters.Name = providerConfig2.Name
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, latestCp)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				latestCp := &kueuealpha.CapacityProvider{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, latestCp)).To(gomega.Succeed())
				g.Expect(latestCp.Generation).To(gomega.BeNumerically(">", prevGen))
				expectCapacityProviderCPU(g, cpKey, "8")

				latestDqo := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(dqo), latestDqo)).To(gomega.Succeed())
				g.Expect(latestDqo.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonComputed,
				))
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting the referenced MultiKueueCapacityProviderConfig so the provider becomes Misconfigured and DQO turns ProviderNotReady", func() {
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, providerConfig2, true)
			gomega.Eventually(func(g gomega.Gomega) {
				latestCp := &kueuealpha.CapacityProvider{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, latestCp)).To(gomega.Succeed())
				g.Expect(latestCp.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.CapacityProviderCapacitySynchronized,
					kueuealpha.CapacityProviderReasonMisconfigured,
				))

				latestDqo := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(dqo), latestDqo)).To(gomega.Succeed())
				g.Expect(latestDqo.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
				))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking that the manager ClusterQueue keeps the last status.effectiveQuotas even if a worker quota changes", func() {
			updateWorkerClusterQueueCPU(worker1TestCluster, f.worker1Cq, "2")
			gomega.Consistently(func(g gomega.Gomega) {
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "8")
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should adjust capacity on worker disconnect, preserve effectiveQuotas when all workers disconnect, and resync on reconnect", func() {
		cpKey := client.ObjectKeyFromObject(cp)
		managerCqKey := client.ObjectKeyFromObject(f.managerCq)

		ginkgo.By("creating a DynamicQuotaOrchestrator for the manager ClusterQueue", func() {
			dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("mk-dqo").
				DiscoveryProvider(cp.Name, nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, f.managerCq.Name).
				Obj()
			behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, dqo)
			gomega.Eventually(func(g gomega.Gomega) {
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "10")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		restoreWorker1 := behavioral.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster1, managersConfigNamespace.Name)
		ginkgo.By("checking that disconnecting worker1 publishes the remaining worker2 quota and lists worker1 in the condition message", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "5")
				latestCp := &kueuealpha.CapacityProvider{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, latestCp)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(latestCp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Message).To(gomega.ContainSubstring(f.workerCluster1.Name))
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "5")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		updateWorkerClusterQueueCPU(worker1TestCluster, f.worker1Cq, "9")
		restoreWorker2 := behavioral.BreakConnection(managerTestCluster.ctx, managerTestCluster.client, f.workerCluster2, managersConfigNamespace.Name)
		ginkgo.By("checking that disconnecting all workers sets SourceUnavailable while DQO preserves the last effectiveQuotas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				latestCp := &kueuealpha.CapacityProvider{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, latestCp)).To(gomega.Succeed())
				g.Expect(latestCp.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.CapacityProviderCapacitySynchronized,
					kueuealpha.CapacityProviderReasonSourceUnavailable,
				))
				cond := apimeta.FindStatusCondition(latestCp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Message).To(gomega.ContainSubstring(f.workerCluster1.Name))
				g.Expect(cond.Message).To(gomega.ContainSubstring(f.workerCluster2.Name))

				latestDqo := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(dqo), latestDqo)).To(gomega.Succeed())
				g.Expect(latestDqo.Status.Conditions).To(utiltesting.HaveConditionStatusFalseAndReason(
					kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
					kueuealpha.DynamicQuotaOrchestratorReasonProviderNotReady,
				))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Consistently(func(g gomega.Gomega) {
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "5")
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		restoreWorker1()
		restoreWorker2()
		ginkgo.By("checking that reconnecting both workers resyncs remote caches and updates effectiveQuotas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "14")
				latestCp := &kueuealpha.CapacityProvider{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, latestCp)).To(gomega.Succeed())
				cond := apimeta.FindStatusCondition(latestCp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Message).To(gomega.Equal("Capacity synchronized from MultiKueue worker clusters"))
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "14")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should ignore the deprecated MultiKueueConfig spec.quotaManagement", func() {
		cpKey := client.ObjectKeyFromObject(cp)

		ginkgo.By("setting spec.quotaManagement to Automated", func() {
			setMultiKueueConfigQuotaManagement(f.managerMultiKueueConfig, "Automated")
		})

		ginkgo.By("checking that the manager ClusterQueue spec is not changed", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				managerCq := &kueue.ClusterQueue{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(f.managerCq), managerCq)).To(gomega.Succeed())
				g.Expect(managerCq.Spec.ResourceGroups).To(gomega.BeComparableTo(f.managerCq.Spec.ResourceGroups))
				expectCapacityProviderCPU(g, cpKey, "10")
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("setting spec.quotaManagement to Manual", func() {
			setMultiKueueConfigQuotaManagement(f.managerMultiKueueConfig, "Manual")
		})

		ginkgo.By("checking that the provider still follows worker quota changes", func() {
			updateWorkerClusterQueueCPU(worker1TestCluster, f.worker1Cq, "3")
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})

var _ = ginkgo.Describe("MultiKueue CapacityProvider when the feature gate is toggled", ginkgo.Label("area:multikueue", "feature:multikueue"), func() {
	var (
		f              *multiKueueFixture
		providerConfig *kueuealpha.MultiKueueCapacityProviderConfig
		cp             *kueuealpha.CapacityProvider
		dqo            *kueuealpha.DynamicQuotaOrchestrator
		managerRunning bool
	)

	// The indexes, the controllers and the worker watches are set up only when the manager starts,
	// so the manager is restarted for every feature gate state.
	startManager := func() {
		managerTestCluster.fwk.StartManager(managerTestCluster.ctx, managerTestCluster.cfg, func(ctx context.Context, mgr manager.Manager) {
			managerAndMultiKueueSetup(ctx, mgr, 2*time.Second, defaultEnabledIntegrations, config.MultiKueueDispatcherModeAllAtOnce)
		})
		managerRunning = true
	}

	stopManager := func() {
		if managerRunning {
			managerTestCluster.fwk.StopManager(managerTestCluster.ctx)
			managerRunning = false
		}
	}

	// cleanup deletes test objects while a manager is running (so finalizers are removed)
	// and then stops the manager. Calling cleanup at the end of It stops all controller
	// goroutines before It-scoped SetFeatureGatesDuringTest cleanups mutate feature gates;
	// AfterEach calls cleanup again as a safety net if an assertion fails mid-test.
	cleanup := func() {
		if f != nil {
			if !managerRunning {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DQOMultiKueueCapacityProvider, true)
				startManager()
			}
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, dqo, true)
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, cp, true)
			behavioral.ExpectObjectToBeDeleted(managerTestCluster.ctx, managerTestCluster.client, providerConfig, true)
			f.teardown()
			dqo, cp, providerConfig, f = nil, nil, nil, nil
		}
		stopManager()
	}

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DQOMultiKueueCapacityProvider, true)
		startManager()
		f = setupMultiKueueFixture()
		providerConfig, cp = createMultiKueueCapacityProvider(f)
		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator("mk-dqo").
			DiscoveryProvider(cp.Name, nil).
			SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, f.managerCq.Name).
			Obj()
		behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, dqo)
	})

	ginkgo.AfterEach(func() {
		cleanup()
	})

	ginkgo.It("Should not update status.capacity while the gate is disabled, support DQO unpinning, and update after the gate is enabled again", func() {
		cpKey := client.ObjectKeyFromObject(cp)
		managerCqKey := client.ObjectKeyFromObject(f.managerCq)
		dqoKey := client.ObjectKeyFromObject(dqo)

		ginkgo.By("checking that the provider and DQO publish the sum of the worker quotas", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "10")
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "10")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restarting the manager with DQOMultiKueueCapacityProvider disabled and DynamicQuotaOrchestration enabled", func() {
			stopManager()
			features.SetFeatureGatesDuringTest(ginkgo.GinkgoTB(), map[featuregate.Feature]bool{
				features.DynamicQuotaOrchestration:     true,
				features.DQOMultiKueueCapacityProvider: false,
			})
			startManager()
		})

		ginkgo.By("checking that a worker quota change leaves provider status and DQO effectiveQuotas frozen", func() {
			updateWorkerClusterQueueCPU(worker1TestCluster, f.worker1Cq, "3")
			gomega.Consistently(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "10")
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "10")
			}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("unpinning DQO and clearing manager ClusterQueue status.effectiveQuotas while the gate is disabled", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				latestDqo := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, dqoKey, latestDqo)).To(gomega.Succeed())
				latestDqo.Spec.CapacityDistribution = nil
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, latestDqo)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				latestDqo := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, dqoKey, latestDqo)).To(gomega.Succeed())
				g.Expect(apimeta.FindStatusCondition(latestDqo.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)).To(gomega.BeNil())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				latestCq := &kueue.ClusterQueue{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, managerCqKey, latestCq)).To(gomega.Succeed())
				latestCq.Status.EffectiveQuotas = nil
				g.Expect(managerTestCluster.client.Status().Update(managerTestCluster.ctx, latestCq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Consistently(func(g gomega.Gomega) {
				latestCq := &kueue.ClusterQueue{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, managerCqKey, latestCq)).To(gomega.Succeed())
				g.Expect(latestCq.Status.EffectiveQuotas).To(gomega.BeNil())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("restarting the manager with DQOMultiKueueCapacityProvider enabled and re-attaching DQO distribution", func() {
			stopManager()
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.DQOMultiKueueCapacityProvider, true)
			startManager()

			gomega.Eventually(func(g gomega.Gomega) {
				latestDqo := &kueuealpha.DynamicQuotaOrchestrator{}
				g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, dqoKey, latestDqo)).To(gomega.Succeed())
				latestDqo.Spec.CapacityDistribution = &kueuealpha.CapacityDistribution{
					SubtreeRootQuotaRef: kueuealpha.CapacityDistributionSubtreeRootRef{
						Kind: kueuealpha.ClusterQueueSubtreeRootRefKind,
						Name: f.managerCq.Name,
					},
				}
				g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, latestDqo)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking that the provider status and manager effectiveQuotas are updated", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				expectCapacityProviderCPU(g, cpKey, "8")
				expectManagerClusterQueueEffectiveCPU(g, managerCqKey, dqo.Name, "5", "8")
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		cleanup()
	})
})

// createMultiKueueCapacityProvider creates a MultiKueueCapacityProviderConfig that points to the
// fixture's manager ClusterQueue, and a MultiKueue CapacityProvider that references it.
func createMultiKueueCapacityProvider(f *multiKueueFixture) (*kueuealpha.MultiKueueCapacityProviderConfig, *kueuealpha.CapacityProvider) {
	ginkgo.GinkgoHelper()
	providerConfig := utiltestingalpha.MakeMultiKueueCapacityProviderConfig(mkCapacityProviderConfigName).
		ManagerSubtreeRootRef(kueuealpha.ClusterQueueSubtreeRootRefKind, f.managerCq.Name).
		Obj()
	behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, providerConfig)

	cp := utiltestingalpha.MakeCapacityProvider(mkCapacityProviderName).
		ControllerName(kueue.MultiKueueControllerName).
		OrchestratedFlavors(string(multikueueTestFlavor)).
		Parameters(kueuealpha.SchemeGroupVersion.Group, "MultiKueueCapacityProviderConfig", providerConfig.Name).
		Obj()
	behavioral.MustCreate(managerTestCluster.ctx, managerTestCluster.client, cp)
	return providerConfig, cp
}

// expectCapacityProviderCPU checks that the provider reports the given CPU capacity for the
// manager flavor, with a CapacitySynchronized=True condition for the current generation.
func expectCapacityProviderCPU(g gomega.Gomega, cpKey types.NamespacedName, cpu string) {
	ginkgo.GinkgoHelper()
	cp := &kueuealpha.CapacityProvider{}
	g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cpKey, cp)).To(gomega.Succeed())
	g.Expect(cp.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
		kueuealpha.CapacityProviderCapacitySynchronized,
		kueuealpha.CapacityProviderReasonSynchronized,
	))
	cond := apimeta.FindStatusCondition(cp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
	g.Expect(cond.ObservedGeneration).To(gomega.Equal(cp.Generation))
	g.Expect(cp.Status.Capacity).To(gomega.BeComparableTo(utiltestingalpha.MakeNormalizedCapacity().
		Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, cpu).Obj()).
		Obj()))
}

// expectManagerClusterQueueEffectiveCPU checks the CPU quota and orchestratorRef that DQO wrote
// to the manager ClusterQueue status.effectiveQuotas, and verifies spec.resourceGroups is unchanged.
func expectManagerClusterQueueEffectiveCPU(g gomega.Gomega, cqKey types.NamespacedName, orchestratorName, specCPU, effectiveCPU string) {
	ginkgo.GinkgoHelper()
	cq := &kueue.ClusterQueue{}
	g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, cqKey, cq)).To(gomega.Succeed())
	g.Expect(cq.Spec.ResourceGroups).To(gomega.BeComparableTo([]kueue.ResourceGroup{
		utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, specCPU).Obj()),
	}))
	g.Expect(cq.Status.EffectiveQuotas).NotTo(gomega.BeNil())
	g.Expect(cq.Status.EffectiveQuotas.OrchestratorRef).To(gomega.Equal(kueue.EffectiveQuotaStatusOrchestratorRef{
		APIGroup: kueuealpha.SchemeGroupVersion.Group,
		Kind:     "DynamicQuotaOrchestrator",
		Name:     orchestratorName,
	}))
	g.Expect(cq.Status.EffectiveQuotas.ResourceGroups).To(gomega.BeComparableTo([]kueue.ResourceGroup{
		utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas(string(multikueueTestFlavor)).Resource(corev1.ResourceCPU, effectiveCPU).Obj()),
	}))
}

func updateWorkerClusterQueueCPU(worker cluster, cq *kueue.ClusterQueue, cpu string) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		latestCq := &kueue.ClusterQueue{}
		g.Expect(worker.client.Get(worker.ctx, client.ObjectKeyFromObject(cq), latestCq)).To(gomega.Succeed())
		latestCq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse(cpu)
		g.Expect(worker.client.Update(worker.ctx, latestCq)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func setWorkerClusterQueueEffectiveQuotas(worker cluster, cq *kueue.ClusterQueue, effectiveQuotas *kueue.EffectiveQuotaStatus) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		latestCq := &kueue.ClusterQueue{}
		g.Expect(worker.client.Get(worker.ctx, client.ObjectKeyFromObject(cq), latestCq)).To(gomega.Succeed())
		latestCq.Status.EffectiveQuotas = effectiveQuotas
		g.Expect(worker.client.Status().Update(worker.ctx, latestCq)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}

func setMultiKueueConfigQuotaManagement(mkConfig *kueue.MultiKueueConfig, mode kueue.MultiKueueConfigQuotaManagementMode) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		latestConfig := &kueue.MultiKueueConfig{}
		g.Expect(managerTestCluster.client.Get(managerTestCluster.ctx, client.ObjectKeyFromObject(mkConfig), latestConfig)).To(gomega.Succeed())
		latestConfig.Spec.QuotaManagement = &mode //nolint:staticcheck // SA1019: tests that the deprecated field is ignored
		g.Expect(managerTestCluster.client.Update(managerTestCluster.ctx, latestConfig)).To(gomega.Succeed())
	}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
}
