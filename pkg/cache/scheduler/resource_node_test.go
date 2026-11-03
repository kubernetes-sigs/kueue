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

package scheduler

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestCohortLendable(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())

	cq1 := utiltestingapi.MakeClusterQueue("cq1").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").LendingLimit("8").Append().
				ResourceQuotaWrapper("example.com/gpu").NominalQuota("3").LendingLimit("3").Append().
				Obj(),
		).Cohort("test-cohort").
		ClusterQueue

	cq2 := utiltestingapi.MakeClusterQueue("cq2").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("2").LendingLimit("2").Append().
				Obj(),
		).Cohort("test-cohort").
		ClusterQueue

	if err := cache.AddClusterQueue(ctx, &cq1); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}
	if err := cache.AddClusterQueue(ctx, &cq2); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}

	wantLendable := map[corev1.ResourceName]resources.Amount{
		corev1.ResourceCPU: resources.NewAmount(10_000),
		"example.com/gpu":  resources.NewAmount(3),
	}

	lendable := cache.hm.Cohort("test-cohort").resourceNode.Lendable
	if diff := cmp.Diff(wantLendable, lendable); diff != "" {
		t.Errorf("Unexpected cohort lendable (-want,+got):\n%s", diff)
	}
}

func TestCohortEffectiveQuotasUpdateAndFallback(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	cohortSpec := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).Obj()

	c := newCohort("test-cohort")
	if err := c.updateCohort(cohortSpec, nil); err != nil {
		t.Fatalf("unexpected error updating cohort: %v", err)
	}

	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	if q := c.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(10000)) {
		t.Errorf("expected nominal quota 10000 from spec, got %v", q.Nominal)
	}

	cohortEffective := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).
		EffectiveQuotas(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "500").Obj(),
		).Obj()

	if err := c.updateCohort(cohortEffective, nil); err != nil {
		t.Fatalf("unexpected error updating cohort: %v", err)
	}

	if q := c.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(500000)) {
		t.Errorf("expected nominal quota 500000 from EffectiveQuotas, got %v", q.Nominal)
	}

	cohortCleared := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).Obj()

	if err := c.updateCohort(cohortCleared, nil); err != nil {
		t.Fatalf("unexpected error updating cohort: %v", err)
	}

	if q := c.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(10000)) {
		t.Errorf("expected nominal quota 10000 after clearing EffectiveQuotas, got %v", q.Nominal)
	}
}

// The Cohort ignores an effective quota marked EffectiveQuotaActive=False and uses
// it again once the condition is True. It never compares validUntil with the clock.
func TestCohortEffectiveQuotasInvalid(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	base := utiltestingapi.MakeCohort("cohort-stale").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).
		EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().
			Name("dqo-stale").
			ResourceGroups(utiltestingapi.ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "500").Obj(),
			)).
			ValidUntil(time.Now().Add(-time.Hour)).
			Obj())
	valid := base.Clone().Condition(kueue.EffectiveQuotaActive, metav1.ConditionTrue, kueue.EffectiveQuotaActiveReasonReady, "").Obj()
	expired := base.Clone().Condition(kueue.EffectiveQuotaActive, metav1.ConditionFalse, kueue.EffectiveQuotaActiveReasonExpired, "").Obj()

	c := newCohort("cohort-stale")
	steps := []struct {
		name             string
		cohort           *kueue.Cohort
		wantNominal      int64
		wantOrchestrator string
	}{
		{name: "valid", cohort: valid, wantNominal: 500_000, wantOrchestrator: "dqo-stale"},
		{name: "expired", cohort: expired, wantNominal: 10_000, wantOrchestrator: ""},
		{name: "valid again", cohort: valid, wantNominal: 500_000, wantOrchestrator: "dqo-stale"},
		{name: "validUntil passed without the condition", cohort: base.Clone().Obj(), wantNominal: 500_000, wantOrchestrator: "dqo-stale"},
	}
	for _, step := range steps {
		if err := c.updateCohort(step.cohort, nil); err != nil {
			t.Fatalf("%s: unexpected error updating cohort: %v", step.name, err)
		}
		if q := c.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(step.wantNominal)) {
			t.Errorf("%s: nominal quota = %v, want %d", step.name, q.Nominal, step.wantNominal)
		}
		if got := string(c.DynamicQuotaOrchestrator); got != step.wantOrchestrator {
			t.Errorf("%s: DynamicQuotaOrchestrator = %q, want %q", step.name, got, step.wantOrchestrator)
		}
	}
}

// The Cohort effective quota is its own quota source, so it gets the same sizes
// as the ClusterQueue one, plus one past what a Quantity holds.
func TestCohortEffectiveQuotasPastInt64(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	specOnly := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).Obj()
	effective1E := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).
		EffectiveQuotas(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1E").Obj(),
		).Obj()
	effectiveOversized := utiltestingapi.MakeCohort("test-cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj(),
		).
		EffectiveQuotas(
			*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "9223372036854775808").Obj(),
		).Obj()

	c := newCohort("test-cohort")
	if err := c.updateCohort(specOnly, nil); err != nil {
		t.Fatalf("updateCohort() = %v", err)
	}
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	// 1E of CPU is 10^18 cores, which is 10^21 milliCPU and past int64.
	if err := c.updateCohort(effective1E, nil); err != nil {
		t.Fatalf("updateCohort() = %v", err)
	}
	if got := c.resourceNode.Quotas[fr].Nominal.String(); got != "1000000000000000000000" {
		t.Errorf("effective quota = %s, want 1000000000000000000000", got)
	}

	// Rewriting the same effective quota reaches the same amount.
	if err := c.updateCohort(effective1E, nil); err != nil {
		t.Fatalf("updateCohort() = %v", err)
	}
	if got := c.resourceNode.Quotas[fr].Nominal.String(); got != "1000000000000000000000" {
		t.Errorf("rewritten effective quota = %s, want 1000000000000000000000", got)
	}

	// Status does not go through the spec webhook, so an effective quota past
	// what a Quantity holds reaches the cache. It is bounded, not taken.
	if err := c.updateCohort(effectiveOversized, nil); err != nil {
		t.Fatalf("updateCohort() = %v", err)
	}
	if got := c.resourceNode.Quotas[fr].Nominal.String(); got != "9223372036854775807000" {
		t.Errorf("oversized effective quota = %s, want 9223372036854775807000", got)
	}

	if err := c.updateCohort(specOnly, nil); err != nil {
		t.Fatalf("updateCohort() = %v", err)
	}
	if q := c.resourceNode.Quotas[fr]; !q.Nominal.Equal(resources.NewAmount(10000)) {
		t.Errorf("after clearing the effective quota = %s, want 10000", q.Nominal)
	}
}
