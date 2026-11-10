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

package dqo

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestClusterQueueMovedToAnotherDQOSubtree(t *testing.T) {
	testMovedToAnotherDQOSubtree(t, false)
}

func TestCohortMovedToAnotherDQOSubtree(t *testing.T) {
	testMovedToAnotherDQOSubtree(t, true)
}

func testMovedToAnotherDQOSubtree(t *testing.T, moveCohort bool) {
	t.Helper()
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	provider := func(name, flavor string) *kueuealpha.CapacityProvider {
		return utiltestingalpha.MakeCapacityProvider(name).
			OrchestratedFlavors(flavor).
			Condition(metav1.Condition{
				Type:   kueuealpha.CapacityProviderCapacitySynchronized,
				Status: metav1.ConditionTrue,
				Reason: kueuealpha.CapacityProviderReasonSynchronized,
			}).
			Capacity(utiltestingalpha.MakeNormalizedCapacity().
				Flavors(utiltestingalpha.MakeNormalizedCapacityFlavor(flavor).
					Resource(corev1.ResourceCPU, "100").Obj()).
				Obj()).
			Obj()
	}
	parent := kueue.CohortReference("team-a")
	if moveCohort {
		parent = "squad"
	}
	objs := []client.Object{
		utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-a").DiscoveryProvider("cp-a", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "team-a").Obj(),
		utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo-b").DiscoveryProvider("cp-b", nil).
			SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "team-b").Obj(),
		provider("cp-a", "pool-a"),
		provider("cp-b", "pool-b"),
		utiltestingapi.MakeCohort("org").Obj(),
		utiltestingapi.MakeCohort("team-a").Parent("org").Obj(),
		utiltestingapi.MakeCohort("team-b").Parent("org").Obj(),
		utiltestingapi.MakeClusterQueue("cq-a1").Cohort("team-a").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("pool-a").Resource(corev1.ResourceCPU, "50").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("cq-moved").Cohort(parent).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("pool-a").Resource(corev1.ResourceCPU, "50").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("cq-b1").Cohort("team-b").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("pool-b").Resource(corev1.ResourceCPU, "50").Obj()).Obj(),
	}
	if moveCohort {
		objs = append(objs, utiltestingapi.MakeCohort("squad").Parent("team-a").Obj())
	}
	cl := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...).Build()
	r := NewReconciler(cl)
	ctx, _ := utiltesting.ContextWithLog(t)
	reconcileBoth := func() {
		t.Helper()
		for _, name := range []string{"dqo-a", "dqo-b"} {
			if _, err := r.Reconcile(ctx, reconcile.Request{Name: name}); err != nil {
				t.Fatalf("Reconcile(%s): %v", name, err)
			}
		}
	}

	// Steady state: dqo-a owns cq-a1 and cq-moved, dqo-b owns cq-b1.
	reconcileBoth()

	// Move the queue or its parent and point the queue at the destination pool.
	var cq kueue.ClusterQueue
	if err := cl.Get(ctx, types.NamespacedName{Name: "cq-moved"}, &cq); err != nil {
		t.Fatal(err)
	}
	if moveCohort {
		var squad kueue.Cohort
		if err := cl.Get(ctx, client.ObjectKey{Name: "squad"}, &squad); err != nil {
			t.Fatal(err)
		}
		squad.Spec.ParentName = "team-b"
		if err := cl.Update(ctx, &squad); err != nil {
			t.Fatal(err)
		}
	} else {
		cq.Spec.CohortName = "team-b"
	}
	cq.Spec.ResourceGroups = []kueue.ResourceGroup{utiltestingapi.ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("pool-b").Resource(corev1.ResourceCPU, "50").Obj())}
	if err := cl.Update(ctx, &cq); err != nil {
		t.Fatal(err)
	}
	// Reconcile both DQOs after the move.
	reconcileBoth()

	var dqoB kueuealpha.DynamicQuotaOrchestrator
	if err := cl.Get(ctx, types.NamespacedName{Name: "dqo-b"}, &dqoB); err != nil {
		t.Fatal(err)
	}
	if c := apimeta.FindStatusCondition(dqoB.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed); c == nil || c.Status != metav1.ConditionTrue {
		t.Errorf("dqo-b Distributed = %+v, want True", c)
	}
	if err := cl.Get(ctx, types.NamespacedName{Name: "cq-moved"}, &cq); err != nil {
		t.Fatal(err)
	}
	expectQueue := func(name, owner, flavor, cpu string) {
		t.Helper()
		var actual kueue.ClusterQueue
		if err := cl.Get(ctx, client.ObjectKey{Name: name}, &actual); err != nil {
			t.Fatal(err)
		}
		quotas := actual.Status.EffectiveQuotas
		if quotas == nil || len(quotas.ResourceGroups) != 1 || len(quotas.ResourceGroups[0].Flavors) != 1 || len(quotas.ResourceGroups[0].Flavors[0].Resources) != 1 {
			t.Fatalf("%s: unexpected effective quotas: %+v", name, quotas)
		}
		gotFlavor := quotas.ResourceGroups[0].Flavors[0]
		wantCPU := resource.MustParse(cpu)
		if quotas.OrchestratorRef.Name != owner || string(gotFlavor.Name) != flavor || gotFlavor.Resources[0].NominalQuota.Cmp(wantCPU) != 0 {
			t.Errorf("%s: effective quotas = %+v, want owner %s, flavor %s, cpu %s", name, quotas, owner, flavor, cpu)
		}
	}
	expectQueue("cq-a1", "dqo-a", "pool-a", "100")
	expectQueue("cq-moved", "dqo-b", "pool-b", "50")
	expectQueue("cq-b1", "dqo-b", "pool-b", "50")
	if moveCohort {
		var squad kueue.Cohort
		if err := cl.Get(ctx, client.ObjectKey{Name: "squad"}, &squad); err != nil {
			t.Fatal(err)
		}
		if squad.Status.EffectiveQuotas == nil || squad.Status.EffectiveQuotas.OrchestratorRef.Name != "dqo-b" {
			t.Fatalf("squad effective quotas = %+v, want dqo-b ownership", squad.Status.EffectiveQuotas)
		}
	}

	var cp kueuealpha.CapacityProvider
	if err := cl.Get(ctx, client.ObjectKey{Name: "cp-b"}, &cp); err != nil {
		t.Fatal(err)
	}
	cp.Status.Capacity.Flavors[0].Resources[corev1.ResourceCPU] = resource.MustParse("40")
	if err := cl.Status().Update(ctx, &cp); err != nil {
		t.Fatal(err)
	}
	reconcileBoth()
	expectQueue("cq-moved", "dqo-b", "pool-b", "20")
	expectQueue("cq-b1", "dqo-b", "pool-b", "20")
}
