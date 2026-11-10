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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
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

// TestIncompleteRedistributionCondition verifies that a failed participant
// status write never leaves a previous Distributed=True result in place.
// This test intentionally does not assert that intermediate effective quotas
// are capacity-safe; protecting admission is tracked separately in #18548.
func TestIncompleteRedistributionCondition(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	for name, tc := range map[string]struct {
		receiverName  string
		previousFalse bool
	}{
		"receiver first":                {receiverName: "cq-a"},
		"donor first":                   {receiverName: "cq-b"},
		"previously false before write": {receiverName: "cq-a", previousFalse: true},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)

			cp := utiltestingalpha.MakeCapacityProvider("cp").
				ControllerName("example.com/test-provider").
				OrchestratedFlavors("default").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueuealpha.CapacityProviderCapacitySynchronized,
					Status:             metav1.ConditionTrue,
					Reason:             "Synchronized",
					ObservedGeneration: 1,
				}).
				Capacity(utiltestingalpha.MakeNormalizedCapacity().Flavors(
					utiltestingalpha.MakeNormalizedCapacityFlavor("default").
						Resource(corev1.ResourceCPU, "100").Obj(),
				).Obj()).Obj()

			orchestrator := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
				DiscoveryProvider(cp.Name, nil).
				SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root").Obj()
			root := utiltestingapi.MakeCohort("root").Obj()
			a := utiltestingapi.MakeClusterQueue("cq-a").Cohort("root").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()
			b := utiltestingapi.MakeClusterQueue("cq-b").Cohort("root").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj()

			donorName := "cq-b"
			if tc.receiverName == "cq-b" {
				donorName = "cq-a"
			}
			injectedConflict := apierrors.NewConflict(
				kueue.Resource("clusterqueues"), donorName,
				errors.New("injected concurrent status write"),
			)
			failDonor := false
			objects := []client.Object{cp, orchestrator, root, a, b}
			cl := utiltesting.NewClientBuilder().
				WithObjects(objects...).
				WithStatusSubresource(objects...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						if _, ok := obj.(*kueue.ClusterQueue); ok && sub == "status" && failDonor && obj.GetName() == donorName {
							return injectedConflict
						}
						return c.SubResource(sub).Update(ctx, obj, opts...)
					},
				}).Build()
			r := NewReconciler(cl)
			req := reconcile.Request{Name: orchestrator.Name}

			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("initial distribution: %v", err)
			}
			var receiver kueue.ClusterQueue
			if err := cl.Get(ctx, client.ObjectKey{Name: tc.receiverName}, &receiver); err != nil {
				t.Fatal(err)
			}
			receiver.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("3")
			if err := cl.Update(ctx, &receiver); err != nil {
				t.Fatal(err)
			}
			if tc.previousFalse {
				var previous kueuealpha.DynamicQuotaOrchestrator
				if err := cl.Get(ctx, client.ObjectKeyFromObject(orchestrator), &previous); err != nil {
					t.Fatal(err)
				}
				apimeta.SetStatusCondition(&previous.Status.Conditions, metav1.Condition{
					Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
					Status:  metav1.ConditionFalse,
					Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
					Message: "previously deactivated",
				})
				if err := cl.Status().Update(ctx, &previous); err != nil {
					t.Fatal(err)
				}
			}

			failDonor = true
			if _, err := r.Reconcile(ctx, req); !errors.Is(err, injectedConflict) {
				t.Fatalf("redistribution error = %v, want injected conflict", err)
			}
			var actual kueuealpha.DynamicQuotaOrchestrator
			if err := cl.Get(ctx, client.ObjectKeyFromObject(orchestrator), &actual); err != nil {
				t.Fatal(err)
			}
			condition := apimeta.FindStatusCondition(actual.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
			if condition == nil || condition.Status != metav1.ConditionUnknown ||
				condition.Reason != kueuealpha.DynamicQuotaOrchestratorReasonDistributionIncomplete {
				t.Fatalf("Distributed condition after interrupted update = %+v, want Unknown/DistributionIncomplete", condition)
			}

			failDonor = false
			if _, err := r.Reconcile(ctx, req); err != nil {
				t.Fatalf("retry distribution: %v", err)
			}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(orchestrator), &actual); err != nil {
				t.Fatal(err)
			}
			condition = apimeta.FindStatusCondition(actual.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
			if condition == nil || condition.Status != metav1.ConditionTrue ||
				condition.Reason != kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed {
				t.Fatalf("Distributed condition after successful retry = %+v, want True/QuotasDistributed", condition)
			}
		})
	}
}

// TestPreWriteDistributionErrorPreservesFalse covers an error from subtree
// discovery before any quota status write. An unrelated DQO must still be
// allowed to take over retained quotas of this deactivated owner.
func TestPreWriteDistributionErrorPreservesFalse(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	ctx, _ := utiltesting.ContextWithLog(t)
	provider := makeSynchronizedProvider("cp", "flavor", 1)
	orchestrator := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
		DiscoveryProvider(provider.Name, nil).
		SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root").
		Condition(metav1.Condition{
			Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
			Status:  metav1.ConditionFalse,
			Reason:  kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured,
			Message: "previously deactivated",
		}).Obj()
	root := utiltestingapi.MakeCohort("root").Obj()
	queue := utiltestingapi.MakeClusterQueue("cq").Cohort("root").ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("flavor-00").
			Resource(corev1.ResourceCPU, "1").Obj(),
	).Obj()
	objects := []client.Object{provider, orchestrator, root, queue}
	listError := errors.New("injected DQO listing failure")
	blockList := true
	cl := utiltesting.NewClientBuilder().
		WithObjects(objects...).
		WithStatusSubresource(objects...).
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				if _, ok := list.(*kueuealpha.DynamicQuotaOrchestratorList); ok && blockList {
					return listError
				}
				return c.List(ctx, list, opts...)
			},
		}).Build()

	r := NewReconciler(cl)
	req := reconcile.Request{Name: orchestrator.Name}
	if _, err := r.Reconcile(ctx, req); !errors.Is(err, listError) {
		t.Fatalf("Reconcile error = %v, want injected listing error", err)
	}

	var actual kueuealpha.DynamicQuotaOrchestrator
	if err := cl.Get(ctx, client.ObjectKeyFromObject(orchestrator), &actual); err != nil {
		t.Fatal(err)
	}
	condition := apimeta.FindStatusCondition(actual.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	if condition == nil || condition.Status != metav1.ConditionFalse ||
		condition.Reason != kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured {
		t.Fatalf("Distributed condition after pre-write error = %+v, want retained False/Misconfigured", condition)
	}
	var persistedQueue kueue.ClusterQueue
	if err := cl.Get(ctx, client.ObjectKeyFromObject(queue), &persistedQueue); err != nil {
		t.Fatal(err)
	}
	if persistedQueue.Status.EffectiveQuotas != nil {
		t.Fatalf("unexpected quota write before the injected listing failure: %+v", persistedQueue.Status.EffectiveQuotas)
	}

	blockList = false
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("recovery after listing error: %v", err)
	}
	if err := cl.Get(ctx, client.ObjectKeyFromObject(orchestrator), &actual); err != nil {
		t.Fatal(err)
	}
	condition = apimeta.FindStatusCondition(actual.Status.Conditions, kueuealpha.DynamicQuotaOrchestratorDistributed)
	if condition == nil || condition.Status != metav1.ConditionTrue ||
		condition.Reason != kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed {
		t.Fatalf("Distributed condition after recovery = %+v, want True/QuotasDistributed", condition)
	}
}
