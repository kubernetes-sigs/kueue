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
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestPreviousOwnerSubtreeCoverage(t *testing.T) {
	cohortRoot := kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.CohortSubtreeRootRefKind, Name: "old-root"}
	queueRoot := kueuealpha.CapacityDistributionSubtreeRootRef{Kind: kueuealpha.ClusterQueueSubtreeRootRefKind, Name: "root-cq"}
	tests := map[string]struct {
		root    kueuealpha.CapacityDistributionSubtreeRootRef
		kind    kueuealpha.SubtreeRootRefKind
		name    string
		objects []client.Object
		covered bool
	}{
		"queue under old root": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeClusterQueue("target").Cohort("old-root").Obj()}, covered: true,
		},
		"cohort under old root": {
			root: cohortRoot, kind: kueuealpha.CohortSubtreeRootRefKind, name: "child",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeCohort("child").Parent("old-root").Obj()}, covered: true,
		},
		"queue moved outside old root": {
			root: cohortRoot,
			kind: kueuealpha.ClusterQueueSubtreeRootRefKind,
			name: "target",
			objects: []client.Object{
				utiltestingapi.MakeCohort("old-root").Obj(),
				utiltestingapi.MakeCohort("new-root").Obj(),
				utiltestingapi.MakeClusterQueue("target").Cohort("new-root").Obj(),
			},
			covered: false,
		},
		"missing root is inconclusive": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeClusterQueue("target").Obj()}, covered: true,
		},
		"missing target is inconclusive": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj()}, covered: true,
		},
		"missing intermediate is inconclusive": {
			root: cohortRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeClusterQueue("target").Cohort("missing").Obj()}, covered: true,
		},
		"cycle is inconclusive": {
			root: cohortRoot,
			kind: kueuealpha.CohortSubtreeRootRefKind,
			name: "child",
			objects: []client.Object{
				utiltestingapi.MakeCohort("old-root").Obj(),
				utiltestingapi.MakeCohort("child").Parent("other").Obj(),
				utiltestingapi.MakeCohort("other").Parent("child").Obj(),
			},
			covered: true,
		},
		"queue root covers itself": {
			root: queueRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "root-cq",
			objects: []client.Object{utiltestingapi.MakeClusterQueue("root-cq").Obj()}, covered: true,
		},
		"queue root does not cover another queue": {
			root: queueRoot, kind: kueuealpha.ClusterQueueSubtreeRootRefKind, name: "target",
			objects: []client.Object{utiltestingapi.MakeClusterQueue("root-cq").Obj(), utiltestingapi.MakeClusterQueue("target").Obj()}, covered: false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			r := NewReconciler(utiltesting.NewClientBuilder().WithObjects(tc.objects...).Build())
			got, err := r.subtreeCovers(ctx, tc.root, tc.kind, tc.name)
			if err != nil || got != tc.covered {
				t.Fatalf("subtreeCovers() = %t, %v; want %t, nil", got, err, tc.covered)
			}
		})
	}
}

func TestForeignOwnerWithSameNameIsProtected(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	r := NewReconciler(utiltesting.NewClientBuilder().Build())
	orchestrator := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").Obj()
	for _, ref := range []kueue.EffectiveQuotaStatusOrchestratorRef{
		{APIGroup: "example.com", Kind: "Other", Name: "dqo"},
		{APIGroup: "example.com", Kind: dynamicQuotaOrchestratorKind, Name: "dqo"},
	} {
		msg, err := r.checkManagedConflict(
			ctx,
			orchestrator,
			kueuealpha.CapacityDistributionSubtreeRootRef{},
			kueuealpha.ClusterQueueSubtreeRootRefKind,
			"target",
			&kueue.EffectiveQuotaStatus{OrchestratorRef: ref},
			nil,
		)
		if err != nil || msg == "" {
			t.Errorf("foreign owner %+v: message %q, error %v; want conflict", ref, msg, err)
		}
	}
}

func TestRetargetedQueueRootAllowsTakeover(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	cpA := makeSynchronizedProvider("cp-a", "flavor", 1)
	cpB := makeSynchronizedProvider("cp-b", "flavor", 1)
	dqoA := utiltestingalpha.MakeDynamicQuotaOrchestrator("owner-a").DiscoveryProvider(cpA.Name, nil).
		SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "x").Obj()
	dqoB := utiltestingalpha.MakeDynamicQuotaOrchestrator("owner-b").DiscoveryProvider(cpB.Name, nil).
		SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "x").Obj()
	queue := func(name string) *kueue.ClusterQueue {
		return utiltestingapi.MakeClusterQueue(name).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor-00").Resource(corev1.ResourceCPU, "1").Obj()).Obj()
	}
	x, y := queue("x"), queue("y")
	objects := []client.Object{cpA, cpB, dqoA, x, y}
	statusObjects := append(append([]client.Object{}, objects...), dqoB)
	cl := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(statusObjects...).Build()
	r := NewReconciler(cl)
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: dqoA.Name}); err != nil {
		t.Fatal(err)
	}
	var latest kueuealpha.DynamicQuotaOrchestrator
	if err := cl.Get(ctx, client.ObjectKeyFromObject(dqoA), &latest); err != nil {
		t.Fatal(err)
	}
	latest.Spec.CapacityDistribution.SubtreeRootQuotaRef.Name = y.Name
	if err := cl.Update(ctx, &latest); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: dqoA.Name}); err != nil {
		t.Fatal(err)
	}
	if err := cl.Create(ctx, dqoB); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reconcile(ctx, reconcile.Request{Name: dqoB.Name}); err != nil {
		t.Fatal(err)
	}
	var actual kueue.ClusterQueue
	if err := cl.Get(ctx, client.ObjectKeyFromObject(x), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.EffectiveQuotas == nil || actual.Status.EffectiveQuotas.OrchestratorRef.Name != dqoB.Name {
		t.Fatalf("x effective quotas = %+v, want owner-b", actual.Status.EffectiveQuotas)
	}
}

func TestOwnershipLookupFailureRetriesWithoutChangingDistribution(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	ctx, _ := utiltesting.ContextWithLog(t)
	provider := makeSynchronizedProvider("cp", "flavor", 1)
	current := utiltestingalpha.MakeDynamicQuotaOrchestrator("current").
		DiscoveryProvider(provider.Name, nil).
		SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "new-root").
		Condition(metav1.Condition{Type: kueuealpha.DynamicQuotaOrchestratorDistributed, Status: metav1.ConditionTrue, Reason: kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed, Message: "previously distributed"}).
		Obj()
	previous := utiltestingalpha.MakeDynamicQuotaOrchestrator("previous").
		DiscoveryProvider(provider.Name, nil).
		SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "old-root").Obj()
	queue := utiltestingapi.MakeClusterQueue("target").Cohort("new-root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor-00").Resource(corev1.ResourceCPU, "1").Obj()).
		EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().Name("previous").Obj()).Obj()
	objects := []client.Object{provider, current, previous, queue, utiltestingapi.MakeCohort("old-root").Obj(), utiltestingapi.MakeCohort("new-root").Obj()}
	lookupErr := errors.New("injected owner-root lookup failure")
	failLookup := true
	cl := utiltesting.NewClientBuilder().WithObjects(objects...).WithStatusSubresource(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, ok := obj.(*kueue.Cohort); ok && key.Name == "old-root" && failLookup {
					return lookupErr
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).Build()
	r := NewReconciler(cl)
	req := reconcile.Request{Name: current.Name}
	if _, err := r.Reconcile(ctx, req); !errors.Is(err, lookupErr) {
		t.Fatalf("Reconcile error = %v, want injected lookup error", err)
	}
	var got kueuealpha.DynamicQuotaOrchestrator
	if err := cl.Get(ctx, client.ObjectKeyFromObject(current), &got); err != nil {
		t.Fatal(err)
	}
	condition := apimeta.FindStatusCondition(got.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	if condition == nil || condition.Status != metav1.ConditionTrue || condition.Reason != kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed {
		t.Fatalf("Distributed after lookup failure = %+v, want previous True condition", condition)
	}
	var actual kueue.ClusterQueue
	if err := cl.Get(ctx, client.ObjectKeyFromObject(queue), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.EffectiveQuotas == nil || actual.Status.EffectiveQuotas.OrchestratorRef.Name != "previous" {
		t.Fatalf("queue ownership changed after lookup failure: %+v", actual.Status.EffectiveQuotas)
	}
	failLookup = false
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(queue), &actual); err != nil {
		t.Fatal(err)
	}
	if actual.Status.EffectiveQuotas == nil || actual.Status.EffectiveQuotas.OrchestratorRef.Name != "current" {
		t.Fatalf("queue ownership after retry = %+v, want current", actual.Status.EffectiveQuotas)
	}
}
