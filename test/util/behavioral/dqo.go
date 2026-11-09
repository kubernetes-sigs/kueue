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

package behavioral

import (
	"context"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func mustHaveCapacityProviderCapacity(
	ctx context.Context,
	g gomega.Gomega,
	c client.Client,
	cpKey client.ObjectKey,
	cp *kueuealpha.CapacityProvider,
	want *kueuealpha.CapacityProviderNormalizedCapacity,
) {
	ginkgo.GinkgoHelper()
	g.Expect(c.Get(ctx, cpKey, cp)).To(gomega.Succeed())
	g.Expect(cp.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
		kueuealpha.CapacityProviderCapacitySynchronized,
		kueuealpha.CapacityProviderReasonSynchronized,
	))
	cond := apimeta.FindStatusCondition(cp.Status.Conditions, kueuealpha.CapacityProviderCapacitySynchronized)
	g.Expect(cond.ObservedGeneration).To(gomega.Equal(cp.Generation))
	g.Expect(cp.Status.Capacity).To(gomega.BeComparableTo(want))
}

// MustHaveCapacityProviderCapacity checks that the CapacityProvider reports want, with a
// CapacitySynchronized=True condition for the current generation, and returns the fetched object.
func MustHaveCapacityProviderCapacity(ctx context.Context, g gomega.Gomega, c client.Client, cpKey client.ObjectKey, want *kueuealpha.CapacityProviderNormalizedCapacity) *kueuealpha.CapacityProvider {
	ginkgo.GinkgoHelper()
	cp := &kueuealpha.CapacityProvider{}
	mustHaveCapacityProviderCapacity(ctx, g, c, cpKey, cp, want)
	return cp
}

// ExpectCapacityProviderCapacity waits until the CapacityProvider reports want with CapacitySynchronized=True.
func ExpectCapacityProviderCapacity(ctx context.Context, c client.Client, cpKey client.ObjectKey, want *kueuealpha.CapacityProviderNormalizedCapacity) {
	ginkgo.GinkgoHelper()
	cp := &kueuealpha.CapacityProvider{}
	gomega.Eventually(func(g gomega.Gomega) {
		mustHaveCapacityProviderCapacity(ctx, g, c, cpKey, cp, want)
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("CapacityProvider capacity did not match", cp))
}

// ExpectDynamicQuotaOrchestratorDistributed waits until the DynamicQuotaOrchestrator reports
// EffectiveCapacityComputed=True and Distributed=True.
func ExpectDynamicQuotaOrchestratorDistributed(ctx context.Context, c client.Client, dqoKey client.ObjectKey) {
	ginkgo.GinkgoHelper()
	dqo := &kueuealpha.DynamicQuotaOrchestrator{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, dqoKey, dqo)).To(gomega.Succeed())
		g.Expect(dqo.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
			kueuealpha.DynamicQuotaOrchestratorEffectiveCapacityComputed,
			kueuealpha.DynamicQuotaOrchestratorReasonComputed,
		))
		g.Expect(dqo.Status.Conditions).To(utiltesting.HaveConditionStatusTrueAndReason(
			kueuealpha.DynamicQuotaOrchestratorDistributed,
			kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed,
		))
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("DynamicQuotaOrchestrator did not distribute the capacity", dqo))
}

func mustHaveClusterQueueEffectiveQuotas(ctx context.Context, g gomega.Gomega, c client.Client, cq *kueue.ClusterQueue, wantSpec *kueue.ClusterQueue, want *kueue.EffectiveQuotaStatus) {
	ginkgo.GinkgoHelper()
	g.Expect(c.Get(ctx, client.ObjectKeyFromObject(wantSpec), cq)).To(gomega.Succeed())
	g.Expect(cq.Spec.ResourceGroups).To(gomega.BeComparableTo(wantSpec.Spec.ResourceGroups))
	g.Expect(cq.Status.EffectiveQuotas).To(gomega.BeComparableTo(want))
}

// MustHaveClusterQueueEffectiveQuotas checks that the ClusterQueue status reports want while
// its spec.resourceGroups matches wantSpec.
func MustHaveClusterQueueEffectiveQuotas(ctx context.Context, g gomega.Gomega, c client.Client, wantSpec *kueue.ClusterQueue, want *kueue.EffectiveQuotaStatus) {
	ginkgo.GinkgoHelper()
	cq := &kueue.ClusterQueue{}
	mustHaveClusterQueueEffectiveQuotas(ctx, g, c, cq, wantSpec, want)
}

// ExpectClusterQueueEffectiveQuotas waits until the ClusterQueue status reports want while
// its spec.resourceGroups still matches wantSpec.
func ExpectClusterQueueEffectiveQuotas(ctx context.Context, c client.Client, wantSpec *kueue.ClusterQueue, want *kueue.EffectiveQuotaStatus) {
	ginkgo.GinkgoHelper()
	cq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		mustHaveClusterQueueEffectiveQuotas(ctx, g, c, cq, wantSpec, want)
	}, MediumTimeout, Interval).Should(gomega.Succeed(), AssertMsg("ClusterQueue effective quotas did not match", cq))
}

// ConsistentlyClusterQueueEffectiveQuotas verifies that the ClusterQueue status keeps want and
// its spec.resourceGroups stays equal to wantSpec over duration.
func ConsistentlyClusterQueueEffectiveQuotas(ctx context.Context, c client.Client, wantSpec *kueue.ClusterQueue, want *kueue.EffectiveQuotaStatus, duration time.Duration) {
	ginkgo.GinkgoHelper()
	cq := &kueue.ClusterQueue{}
	gomega.Consistently(func(g gomega.Gomega) {
		mustHaveClusterQueueEffectiveQuotas(ctx, g, c, cq, wantSpec, want)
	}, duration, Interval).Should(gomega.Succeed(), AssertMsg("ClusterQueue effective quotas changed unexpectedly", cq))
}

// SetClusterQueueEffectiveQuotas updates status.effectiveQuotas of the ClusterQueue.
func SetClusterQueueEffectiveQuotas(ctx context.Context, c client.Client, cq *kueue.ClusterQueue, effectiveQuotas *kueue.EffectiveQuotaStatus) {
	ginkgo.GinkgoHelper()
	latestCq := &kueue.ClusterQueue{}
	gomega.Eventually(func(g gomega.Gomega) {
		g.Expect(c.Get(ctx, client.ObjectKeyFromObject(cq), latestCq)).To(gomega.Succeed())
		latestCq.Status.EffectiveQuotas = effectiveQuotas
		g.Expect(c.Status().Update(ctx, latestCq)).To(gomega.Succeed())
	}, Timeout, Interval).Should(gomega.Succeed(), AssertMsg("Failed to set ClusterQueue effectiveQuotas", latestCq))
}
