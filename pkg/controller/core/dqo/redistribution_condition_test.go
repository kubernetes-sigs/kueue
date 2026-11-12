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
	"slices"
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
			var attemptedWrites, successfulWrites []string
			objects := []client.Object{cp, orchestrator, root, a, b}
			cl := utiltesting.NewClientBuilder().
				WithObjects(objects...).
				WithStatusSubresource(objects...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, sub string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						if _, ok := obj.(*kueue.ClusterQueue); ok && sub == "status" && failDonor {
							attemptedWrites = append(attemptedWrites, obj.GetName())
							if obj.GetName() == donorName {
								return injectedConflict
							}
							if err := c.SubResource(sub).Update(ctx, obj, opts...); err != nil {
								return err
							}
							successfulWrites = append(successfulWrites, obj.GetName())
							return nil
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

			var donor kueue.ClusterQueue
			if err := cl.Get(ctx, client.ObjectKey{Name: donorName}, &donor); err != nil {
				t.Fatal(err)
			}
			if donor.Status.EffectiveQuotas == nil {
				t.Fatal("initial donor effective quotas not populated")
			}
			previousDonorQuota := donor.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota.DeepCopy()

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

			// The fake client lists queues by name. Verify both attempted writes
			// and the one successful write, regardless of whether the conflict
			// happened before or after the receiver's status update.
			if !slices.Equal(attemptedWrites, []string{"cq-a", "cq-b"}) {
				t.Fatalf("status write attempts = %v, want [cq-a cq-b]", attemptedWrites)
			}
			if !slices.Equal(successfulWrites, []string{tc.receiverName}) {
				t.Fatalf("successful status writes = %v, want [%s]", successfulWrites, tc.receiverName)
			}
			// Capacity is 100 CPUs and the receiver's nominal weight changed
			// from 1 to 3. Its allocation becomes 75 while the failed donor
			// write must leave the previously persisted allocation untouched.
			for _, check := range []struct {
				name string
				want resource.Quantity
			}{
				{tc.receiverName, resource.MustParse("75")},
				{donorName, previousDonorQuota},
			} {
				var persisted kueue.ClusterQueue
				if err := cl.Get(ctx, client.ObjectKey{Name: check.name}, &persisted); err != nil {
					t.Fatal(err)
				}
				if persisted.Status.EffectiveQuotas == nil {
					t.Fatalf("effective quotas missing for %s", check.name)
				}
				got := persisted.Status.EffectiveQuotas.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota
				if got.Cmp(check.want) != 0 {
					t.Errorf("%s quota after interrupted update = %s, want %s", check.name, got.String(), check.want.String())
				}
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

// TestPreWriteDistributionErrorPreservesCondition verifies that read errors
// preceding quota updates do not overwrite the previous Distributed condition.
func TestPreWriteDistributionErrorPreservesCondition(t *testing.T) {
	for name, tc := range map[string]struct {
		status metav1.ConditionStatus
		reason string
	}{
		"previously distributed": {metav1.ConditionTrue, kueuealpha.DynamicQuotaOrchestratorReasonQuotasDistributed},
		"previously deactivated": {metav1.ConditionFalse, kueuealpha.DynamicQuotaOrchestratorReasonMisconfigured},
	} {
		t.Run(name, func(t *testing.T) {
			testPreWriteDistributionErrorPreservesCondition(t, tc.status, tc.reason)
		})
	}
}

func testPreWriteDistributionErrorPreservesCondition(t *testing.T, previousStatus metav1.ConditionStatus, previousReason string) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)

	ctx, _ := utiltesting.ContextWithLog(t)
	provider := makeSynchronizedProvider("cp", "flavor", 1)
	orchestrator := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
		DiscoveryProvider(provider.Name, nil).
		SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root").
		Condition(metav1.Condition{
			Type:    kueuealpha.DynamicQuotaOrchestratorDistributed,
			Status:  previousStatus,
			Reason:  previousReason,
			Message: "previously reported",
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
	if condition == nil || condition.Status != previousStatus || condition.Reason != previousReason {
		t.Fatalf("Distributed condition after pre-write error = %+v, want retained %s/%s", condition, previousStatus, previousReason)
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
