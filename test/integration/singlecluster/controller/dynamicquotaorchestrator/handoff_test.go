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

package dynamicquotaorchestrator

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DynamicQuotaOrchestrator subtree handoff", ginkgo.Label("controller:dynamicquotaorchestrator", "area:dynamicquotaorchestration"), func() {
	for _, moveCohort := range []bool{false, true} {
		mode := "ClusterQueue"
		if moveCohort {
			mode = "Cohort"
		}
		ginkgo.It(fmt.Sprintf("updates destination quotas after moving a %s between active subtrees", mode), func() {
			prefix := "handoff-queue-"
			if moveCohort {
				prefix = "handoff-cohort-"
			}
			name := func(s string) string { return prefix + s }
			rootA := utiltestingapi.MakeCohort(kueue.CohortReference(name("a"))).Obj()
			rootB := utiltestingapi.MakeCohort(kueue.CohortReference(name("b"))).Obj()
			createHandoffObject(rootA)
			createHandoffObject(rootB)
			parent := rootA.Name
			var movingCohort *kueue.Cohort
			if moveCohort {
				movingCohort = utiltestingapi.MakeCohort(kueue.CohortReference(name("moving"))).Parent(kueue.CohortReference(rootA.Name)).Obj()
				createHandoffObject(movingCohort)
				parent = movingCohort.Name
			}
			quota := func(cpu string) kueue.ResourceGroup {
				return utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, cpu).Obj())
			}
			queue := func(n, cohort string) *kueue.ClusterQueue {
				return utiltestingapi.MakeClusterQueue(name(n)).Cohort(kueue.CohortReference(cohort)).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
			}
			cqA, cqMoved, cqB := queue("qa", rootA.Name), queue("moved", parent), queue("qb", rootB.Name)
			createHandoffObject(cqA)
			createHandoffObject(cqMoved)
			createHandoffObject(cqB)
			provider := func(n string) *kueuealpha.CapacityProvider {
				return utiltestingalpha.MakeCapacityProvider(name(n)).ControllerName("example.com/test-provider").
					OrchestratedFlavors("f1").
					Capacity(utiltestingalpha.MakeNormalizedCapacity().Flavors(
						utiltestingalpha.MakeNormalizedCapacityFlavor("f1").Resource(corev1.ResourceCPU, "100").Obj(),
					).Obj()).
					Condition(metav1.Condition{Type: kueuealpha.CapacityProviderCapacitySynchronized, Status: metav1.ConditionTrue, Reason: kueuealpha.CapacityProviderReasonSynchronized}).Obj()
			}
			cpA, cpB := provider("cpa"), provider("cpb")
			createCapacityProvider(ctx, k8sClient, cpA)
			ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cpA, true) })
			createCapacityProvider(ctx, k8sClient, cpB)
			ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cpB, true) })
			dqoA := utiltestingalpha.MakeDynamicQuotaOrchestrator(name("dqoa")).DiscoveryProvider(cpA.Name, nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, rootA.Name).Obj()
			dqoB := utiltestingalpha.MakeDynamicQuotaOrchestrator(name("dqob")).DiscoveryProvider(cpB.Name, nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, rootB.Name).Obj()
			createHandoffObject(dqoA)
			createHandoffObject(dqoB)
			expectDistributed := func(g gomega.Gomega, dqo *kueuealpha.DynamicQuotaOrchestrator) {
				var latest kueuealpha.DynamicQuotaOrchestrator
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqo), &latest)).Should(gomega.Succeed())
				g.Expect(latest.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed, kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed))
			}
			check := func(a, movedOwner, moved, b string) func(gomega.Gomega) {
				return func(g gomega.Gomega) {
					expectDistributed(g, dqoA)
					expectDistributed(g, dqoB)
					expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqA), dqoA.Name, quota(a))
					expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqMoved), movedOwner, quota(moved))
					expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqB), dqoB.Name, quota(b))
					if movingCohort != nil {
						var actual kueue.Cohort
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(movingCohort), &actual)).Should(gomega.Succeed())
						g.Expect(actual.Status.EffectiveQuotas).ShouldNot(gomega.BeNil())
						g.Expect(actual.Status.EffectiveQuotas.OrchestratorRef.Name).Should(gomega.Equal(movedOwner))
					}
				}
			}
			move := func(destination string) {
				ginkgo.GinkgoHelper()
				gomega.Eventually(func(g gomega.Gomega) {
					if movingCohort != nil {
						var latest kueue.Cohort
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(movingCohort), &latest)).Should(gomega.Succeed())
						latest.Spec.ParentName = kueue.CohortReference(destination)
						g.Expect(k8sClient.Update(ctx, &latest)).Should(gomega.Succeed())
					} else {
						var latest kueue.ClusterQueue
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cqMoved), &latest)).Should(gomega.Succeed())
						latest.Spec.CohortName = kueue.CohortReference(destination)
						g.Expect(k8sClient.Update(ctx, &latest)).Should(gomega.Succeed())
					}
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}
			gomega.Eventually(check("50", dqoA.Name, "50", "100"), behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			move(rootB.Name)
			gomega.Eventually(check("100", dqoB.Name, "50", "50"), behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			setCapacityProviderCapacity(ctx, k8sClient, cpB, utiltestingalpha.MakeNormalizedCapacity().Flavors(
				utiltestingalpha.MakeNormalizedCapacityFlavor("f1").Resource(corev1.ResourceCPU, "40").Obj()).Obj())
			gomega.Eventually(check("100", dqoB.Name, "20", "20"), behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Consistently(check("100", dqoB.Name, "20", "20"), 2*time.Second, 200*time.Millisecond).Should(gomega.Succeed())
			move(rootA.Name)
			gomega.Eventually(check("50", dqoA.Name, "50", "40"), behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	}

	ginkgo.It("wakes an existing destination DQO when its previous owner retargets the subtree root", func() {
		name := func(s string) string { return "handoff-retarget-" + s }
		rootA := utiltestingapi.MakeCohort(kueue.CohortReference(name("root-a"))).Obj()
		rootB := utiltestingapi.MakeCohort(kueue.CohortReference(name("root-b"))).Obj()
		createHandoffObject(rootA)
		createHandoffObject(rootB)

		quota := func(cpu string) kueue.ResourceGroup {
			return utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, cpu).Obj())
		}
		queue := func(n, cohort string) *kueue.ClusterQueue {
			return utiltestingapi.MakeClusterQueue(name(n)).Cohort(kueue.CohortReference(cohort)).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		}
		cqA := queue("queue-a", rootA.Name)
		cqB := queue("queue-b", rootB.Name)
		createHandoffObject(cqA)
		createHandoffObject(cqB)

		provider := func(n, cpu string) *kueuealpha.CapacityProvider {
			return utiltestingalpha.MakeCapacityProvider(name(n)).ControllerName("example.com/test-provider").
				OrchestratedFlavors("f1").
				Capacity(utiltestingalpha.MakeNormalizedCapacity().Flavors(
					utiltestingalpha.MakeNormalizedCapacityFlavor("f1").Resource(corev1.ResourceCPU, cpu).Obj(),
				).Obj()).
				Condition(metav1.Condition{
					Type:   kueuealpha.CapacityProviderCapacitySynchronized,
					Status: metav1.ConditionTrue,
					Reason: kueuealpha.CapacityProviderReasonSynchronized,
				}).Obj()
		}
		cpA, cpB := provider("provider-a", "100"), provider("provider-b", "40")
		createCapacityProvider(ctx, k8sClient, cpA)
		ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cpA, true) })
		createCapacityProvider(ctx, k8sClient, cpB)
		ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cpB, true) })

		dqoA := utiltestingalpha.MakeDynamicQuotaOrchestrator(name("owner-a")).
			DiscoveryProvider(cpA.Name, nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, rootA.Name).Obj()
		// B is a descendant of A at first, so it cannot distribute to cqA.
		dqoB := utiltestingalpha.MakeDynamicQuotaOrchestrator(name("owner-b")).
			DiscoveryProvider(cpB.Name, nil).
			SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, cqA.Name).Obj()
		createHandoffObject(dqoA)
		ginkgo.By("waiting for A to own the queue before creating B")
		gomega.Eventually(func(g gomega.Gomega) {
			expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqA), dqoA.Name, quota("100"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		createHandoffObject(dqoB)

		blocked := func(g gomega.Gomega) {
			var latest kueuealpha.DynamicQuotaOrchestrator
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqoB), &latest)).Should(gomega.Succeed())
			g.Expect(latest.Status.Conditions).Should(utiltesting.HaveConditionStatusFalseAndReason(
				kueuealpha.DynamicQuotaOrchestratorDistributed,
				kueuealpha.DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator,
			))
			expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqA), dqoA.Name, quota("100"))
		}
		ginkgo.By("verifying the already-existing destination is blocked by A's ancestor root")
		gomega.Eventually(blocked, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		gomega.Consistently(blocked, time.Second, 200*time.Millisecond).Should(gomega.Succeed())

		ginkgo.By("retargeting A without modifying either ClusterQueue, Cohort, or provider")
		gomega.Eventually(func(g gomega.Gomega) {
			var latest kueuealpha.DynamicQuotaOrchestrator
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqoA), &latest)).Should(gomega.Succeed())
			latest.Spec.CapacityDistribution.SubtreeRootQuotaRef.Name = rootB.Name
			g.Expect(k8sClient.Update(ctx, &latest)).Should(gomega.Succeed())
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

		// There is deliberately no direct Reconcile call or other object update.
		// The DQO spec-generation watch must requeue B to take over A's stale quotas.
		ginkgo.By("waiting for the DQO watch to unblock B and hand over the old queue")
		gomega.Eventually(func(g gomega.Gomega) {
			for _, dqo := range []*kueuealpha.DynamicQuotaOrchestrator{dqoA, dqoB} {
				var latest kueuealpha.DynamicQuotaOrchestrator
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(dqo), &latest)).Should(gomega.Succeed())
				g.Expect(latest.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(
					kueuealpha.DynamicQuotaOrchestratorDistributed,
					kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
				))
			}
			expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqA), dqoB.Name, quota("40"))
			expectClusterQueueOwnedBy(g, client.ObjectKeyFromObject(cqB), dqoA.Name, quota("100"))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
})

func createHandoffObject[PtrT behavioral.ObjAsPtr[T], T any](obj PtrT) {
	ginkgo.GinkgoHelper()
	behavioral.MustCreate(ctx, k8sClient, obj)
	ginkgo.DeferCleanup(func() { behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, obj, true) })
}
