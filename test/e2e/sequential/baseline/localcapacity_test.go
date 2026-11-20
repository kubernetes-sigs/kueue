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

package baseline

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/localcapacity"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// The flavor selects nodes by a label the test adds to and removes from a kind
// worker, which stands in for adding and removing a node of the flavor without
// affecting other tests.
const localCapacityNodeLabel = "e2e.kueue.x-k8s.io/local-capacity"

var _ = ginkgo.Describe("DQO local-capacity CapacityProvider", ginkgo.Label("feature:dqolocalcapacityprovider", e2e.Shard1), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns       *corev1.Namespace
		worker   *corev1.Node
		flavor   *kueue.ResourceFlavor
		cohort   *kueue.Cohort
		cq       *kueue.ClusterQueue
		lq       *kueue.LocalQueue
		provider *kueuealpha.CapacityProvider
		dqo      *kueuealpha.DynamicQuotaOrchestrator
	)

	setWorkerLabel := func(present bool) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			n := &corev1.Node{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(worker), n)).To(gomega.Succeed())
			if present {
				if n.Labels == nil {
					n.Labels = map[string]string{}
				}
				n.Labels[localCapacityNodeLabel] = flavor.Name
			} else {
				delete(n.Labels, localCapacityNodeLabel)
			}
			g.Expect(k8sClient.Update(ctx, n)).To(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	}

	expectCohortCPUQuota := func(want string) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			c := &kueue.Cohort{}
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), c)).To(gomega.Succeed())
			flavorRef := kueue.ResourceFlavorReference(flavor.Name)
			g.Expect(behavioral.EffectiveQuotaResources(c.Status.EffectiveQuotas, flavorRef)).To(gomega.HaveKey(corev1.ResourceCPU))
			g.Expect(behavioral.EffectiveNominalQuotas(c.Status.EffectiveQuotas, flavorRef)).To(gomega.HaveKeyWithValue(corev1.ResourceCPU, want))
		}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
	}

	makeWorkload := func(name string) *kueue.Workload {
		ginkgo.GinkgoHelper()
		wl := utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "100m").
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	ginkgo.BeforeAll(func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			if cfg.FeatureGates == nil {
				cfg.FeatureGates = make(map[string]bool)
			}
			cfg.FeatureGates[string(features.DynamicQuotaOrchestration)] = true
			cfg.FeatureGates[string(features.DQOLocalCapacityProvider)] = true
		})

		worker = e2e.GetWorkerNode(ctx, k8sClient)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-local-capacity-")
		name := "local-capacity-" + ns.Name

		flavor = utiltestingapi.MakeResourceFlavor(name).
			NodeLabel(localCapacityNodeLabel, name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		// The Cohort is the only participant with a positive weight, so it receives
		// all capacity, and the ClusterQueue borrows from it. With the gates off,
		// the Cohort's spec quota of 1 CPU applies again.
		cohort = utiltestingapi.MakeCohort(kueue.CohortReference(name)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "1").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cohort)

		cq = utiltestingapi.MakeClusterQueue(name).
			Cohort(kueue.CohortReference(cohort.Name)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

		provider = utiltestingalpha.MakeCapacityProvider(name).
			ControllerName(localcapacity.ControllerName).
			OrchestratedFlavors(flavor.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, provider)

		dqo = utiltestingalpha.MakeDynamicQuotaOrchestrator(name).
			DiscoveryProvider(provider.Name, nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, cohort.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, dqo)
	})

	ginkgo.AfterAll(func() {
		setWorkerLabel(false)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, dqo, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, provider, true)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, cq, true, behavioral.MediumTimeout)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, flavor, true, behavioral.MediumTimeout)
	})

	// The scheduler cache applies a new effective quota asynchronously to the
	// Cohort status, so this test does not assert that workloads stay pending
	// after the quota drops. The integration suite covers that by also waiting for
	// the cache to report the new quota.
	ginkgo.It("should admit workloads according to the nodes of the flavor", func() {
		ginkgo.By("Publishing zero quota while the flavor has no nodes", func() {
			expectCohortCPUQuota("0")
		})

		ginkgo.By("Admitting a workload once a node joins the flavor", func() {
			setWorkerLabel(true)
			expectCohortCPUQuota(worker.Status.Allocatable.Cpu().String())
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, makeWorkload("wl-node-joined"))
		})

		ginkgo.By("Publishing zero quota once the node leaves the flavor", func() {
			setWorkerLabel(false)
			expectCohortCPUQuota("0")
		})
	})

	ginkgo.It("should fall back to spec quota when the feature gates are disabled", func() {
		e2e.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			delete(cfg.FeatureGates, string(features.DynamicQuotaOrchestration))
			delete(cfg.FeatureGates, string(features.DQOLocalCapacityProvider))
		})
		// The Cohort's spec quota of 1 CPU admits a workload although the nodes publish no quota.
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, makeWorkload("wl-fallback"))
	})
})
