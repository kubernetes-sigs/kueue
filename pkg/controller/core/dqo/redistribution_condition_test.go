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

	for _, tc := range []struct {
		name         string
		receiverName string
	}{
		{name: "receiver first", receiverName: "cq-a"},
		{name: "donor first", receiverName: "cq-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
