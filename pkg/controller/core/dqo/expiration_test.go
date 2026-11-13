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
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
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

var errInjectedStatusUpdate = errors.New("injected status update failure")

func TestDynamicQuotaOrchestratorEffectiveQuotaExpiration(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.DynamicQuotaOrchestration, true)
	// The fake client stores timestamps with second precision.
	now := time.Now().Truncate(time.Second)
	const ttl = 10 * time.Minute

	// Under a provider with 1 CPU of gpu-00, the effective quota of a single
	// ClusterQueue is 1 CPU.
	dqo := utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
		DiscoveryProvider("cp", nil).
		SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq").
		EffectiveQuotaTTL(ttl)
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu-00").Resource(corev1.ResourceCPU, "4").Obj())
	oneCPU := utiltestingapi.MakeEffectiveQuotaStatus().
		ResourceGroups(utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu-00").Resource(corev1.ResourceCPU, "1").Obj()))

	cases := map[string]struct {
		clock              time.Time
		dqo                *kueuealpha.DynamicQuotaOrchestrator
		provider           *kueuealpha.CapacityProvider
		cohorts            []*kueue.Cohort
		clusterQueues      []*kueue.ClusterQueue
		failStatusUpdateOf string
		// statusUpdateDuration is how far the clock moves during each status update.
		statusUpdateDuration time.Duration
		wantErr              error
		wantRequeueAfter     time.Duration
		wantClusterQueues    map[string]*kueue.EffectiveQuotaStatus
		wantCohorts          map[string]*kueue.EffectiveQuotaStatus
	}{
		"successful distribution sets validUntil to now plus the TTL and requeues at half of the TTL": {
			clock:             now,
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"without a TTL, validUntil is not set and there is no requeue": {
			clock: now,
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
				DiscoveryProvider("cp", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq").
				Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Obj()},
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().Obj()},
		},
		"removing the TTL clears validUntil": {
			clock: now,
			dqo: utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").
				DiscoveryProvider("cp", nil).
				SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "cq").
				Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(5 * time.Minute)).Obj()).Obj()},
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().Obj()},
		},
		"unchanged quota with more than half of the TTL left is kept": {
			clock:             now.Add(4 * time.Minute),
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()).Obj()},
			wantRequeueAfter:  time.Minute,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"requeue firing just before half of the TTL is left keeps validUntil": {
			clock:             now.Add(ttl/2 - time.Nanosecond),
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()).Obj()},
			wantRequeueAfter:  time.Nanosecond,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"unchanged quota with exactly half of the TTL left is renewed": {
			clock:             now.Add(ttl / 2),
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()).Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl/2 + ttl)).Obj()},
		},
		"unchanged quota with less than half of the TTL left is renewed": {
			clock:             now.Add(6 * time.Minute),
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()).Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(6*time.Minute + ttl)).Obj()},
		},
		"expired quota is renewed when distribution succeeds again": {
			clock:             now,
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(-time.Minute)).Obj()).Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"unchanged quota without validUntil gets it": {
			clock:             now,
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().Obj()).Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"changed quota is rewritten with a fresh validUntil": {
			clock:    now,
			dqo:      dqo.Obj(),
			provider: makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues: []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(
				utiltestingapi.MakeEffectiveQuotaStatus().
					ResourceGroups(utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu-00").Resource(corev1.ResourceCPU, "2").Obj())).
					ValidUntil(now.Add(8 * time.Minute)).
					Obj(),
			).Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"quota taken over from another orchestrator gets a fresh validUntil": {
			clock:             now,
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().Name("deleted-dqo").ValidUntil(now.Add(8 * time.Minute)).Obj()).Obj()},
			wantRequeueAfter:  ttl / 2,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"sub-second now writes validUntil in whole seconds and still requeues": {
			clock:             now.Add(700 * time.Millisecond),
			dqo:               dqo.Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Obj()},
			wantRequeueAfter:  ttl/2 - 700*time.Millisecond,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"time spent on status updates shortens the requeue": {
			clock:                now,
			dqo:                  dqo.Obj(),
			provider:             makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:        []*kueue.ClusterQueue{cq.Obj()},
			statusUpdateDuration: 10 * time.Second,
			wantRequeueAfter:     ttl/2 - 20*time.Second,
			wantClusterQueues:    map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"each target gets validUntil from the time it is written": {
			clock:    now,
			dqo:      dqo.Clone().SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root").Obj(),
			provider: makeSynchronizedProvider("cp", "gpu", 1),
			cohorts:  []*kueue.Cohort{utiltestingapi.MakeCohort("root").Obj()},
			clusterQueues: []*kueue.ClusterQueue{
				cq.Clone().Cohort("root").Obj(),
			},
			statusUpdateDuration: 10 * time.Second,
			// The ClusterQueue is written first, then the Cohort, then the DQO.
			wantRequeueAfter:  ttl/2 - 30*time.Second,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
			wantCohorts:       map[string]*kueue.EffectiveQuotaStatus{"root": utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(10*time.Second + ttl)).Obj()},
		},
		"renewal already due after the status updates requeues immediately": {
			clock:                now,
			dqo:                  dqo.Obj(),
			provider:             makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:        []*kueue.ClusterQueue{cq.Obj()},
			statusUpdateDuration: ttl / 4,
			wantRequeueAfter:     time.Nanosecond,
			wantClusterQueues:    map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(ttl)).Obj()},
		},
		"validUntil further away than a shortened TTL is pulled in": {
			clock:             now,
			dqo:               dqo.Clone().EffectiveQuotaTTL(time.Minute).Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(24 * time.Hour)).Obj()).Obj()},
			wantRequeueAfter:  30 * time.Second,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(time.Minute)).Obj()},
		},
		"requeue follows the earliest validUntil in the subtree": {
			clock:    now,
			dqo:      dqo.Clone().SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root").Obj(),
			provider: makeSynchronizedProvider("cp", "gpu", 1),
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").EffectiveQuotaStatus(utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(ttl)).Obj()).Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				cq.Clone().Cohort("root").EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(8 * time.Minute)).Obj()).Obj(),
			},
			wantRequeueAfter:  3 * time.Minute,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(8 * time.Minute)).Obj()},
			wantCohorts:       map[string]*kueue.EffectiveQuotaStatus{"root": utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(ttl)).Obj()},
		},
		"not renewed while the provider is not ready": {
			clock: now,
			dqo:   dqo.Obj(),
			provider: utiltestingalpha.MakeCapacityProvider("cp").
				OrchestratedFlavors("gpu-00").
				Condition(metav1.Condition{
					Type:   kueuealpha.CapacityProviderCapacitySynchronized,
					Status: metav1.ConditionFalse,
					Reason: "SourceUnavailable",
				}).
				Obj(),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()).Obj()},
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()},
		},
		"not renewed when the subtree root is missing": {
			clock:             now,
			dqo:               dqo.Clone().SubtreeRoot(kueuealpha.ClusterQueueSubtreeRootRefKind, "missing-cq").Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()).Obj()},
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()},
		},
		"not renewed after capacityDistribution is removed": {
			clock:             now,
			dqo:               utiltestingalpha.MakeDynamicQuotaOrchestrator("dqo").DiscoveryProvider("cp", nil).Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()).Obj()},
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()},
		},
		"not renewed while the DQO is being deleted": {
			clock:             now,
			dqo:               dqo.Clone().DeletionTimestamp(now).Finalizers("kueue.x-k8s.io/test").Obj(),
			provider:          makeSynchronizedProvider("cp", "gpu", 1),
			clusterQueues:     []*kueue.ClusterQueue{cq.Clone().EffectiveQuotaStatus(oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()).Obj()},
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{"cq": oneCPU.Clone().ValidUntil(now.Add(2 * time.Minute)).Obj()},
		},
		"a failed status update keeps earlier writes and returns the error without a timed requeue": {
			clock:    now,
			dqo:      dqo.Clone().SubtreeRoot(kueuealpha.CohortSubtreeRootRefKind, "root").Obj(),
			provider: makeSynchronizedProvider("cp", "gpu", 1),
			cohorts:  []*kueue.Cohort{utiltestingapi.MakeCohort("root").Obj()},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("cq-ok").
					Cohort("root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu-00").Resource(corev1.ResourceCPU, "4").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("cq-fail").
					Cohort("root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu-00").Resource(corev1.ResourceCPU, "4").Obj()).
					Obj(),
			},
			failStatusUpdateOf: "cq-fail",
			wantErr:            errInjectedStatusUpdate,
			wantClusterQueues: map[string]*kueue.EffectiveQuotaStatus{
				"cq-ok": utiltestingapi.MakeEffectiveQuotaStatus().
					ResourceGroups(utiltestingapi.ResourceGroup(*utiltestingapi.MakeFlavorQuotas("gpu-00").Resource(corev1.ResourceCPU, "500m").Obj())).
					ValidUntil(now.Add(ttl)).
					Obj(),
				"cq-fail": nil,
			},
			wantCohorts: map[string]*kueue.EffectiveQuotaStatus{"root": utiltestingapi.MakeEffectiveQuotaStatus().ValidUntil(now.Add(ttl)).Obj()},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tc.dqo, tc.provider}
			for _, cohort := range tc.cohorts {
				objs = append(objs, cohort)
			}
			for _, clusterQueue := range tc.clusterQueues {
				objs = append(objs, clusterQueue)
			}
			fakeClock := testingclock.NewFakeClock(tc.clock)
			cl := utiltesting.NewClientBuilder().
				WithObjects(objs...).
				WithStatusSubresource(objs...).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceUpdate: func(ctx context.Context, c client.Client, subResource string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						if obj.GetName() == tc.failStatusUpdateOf {
							return errInjectedStatusUpdate
						}
						fakeClock.Step(tc.statusUpdateDuration)
						return c.SubResource(subResource).Update(ctx, obj, opts...)
					},
				}).
				Build()
			r := NewReconciler(cl, WithClock(fakeClock))
			ctx, _ := utiltesting.ContextWithLog(t)

			result, err := r.Reconcile(ctx, reconcile.Request{Name: tc.dqo.Name})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Reconcile() error = %v, want %v", err, tc.wantErr)
			}
			if result.RequeueAfter != tc.wantRequeueAfter {
				t.Errorf("RequeueAfter = %v, want %v", result.RequeueAfter, tc.wantRequeueAfter)
			}
			for name, want := range tc.wantClusterQueues {
				var got kueue.ClusterQueue
				if err := cl.Get(ctx, types.NamespacedName{Name: name}, &got); err != nil {
					t.Fatalf("Failed to get ClusterQueue %s: %v", name, err)
				}
				if diff := cmp.Diff(want, got.Status.EffectiveQuotas, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected effectiveQuotas for ClusterQueue %s (-want,+got):\n%s", name, diff)
				}
			}
			for name, want := range tc.wantCohorts {
				var got kueue.Cohort
				if err := cl.Get(ctx, types.NamespacedName{Name: name}, &got); err != nil {
					t.Fatalf("Failed to get Cohort %s: %v", name, err)
				}
				if diff := cmp.Diff(want, got.Status.EffectiveQuotas, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected effectiveQuotas for Cohort %s (-want,+got):\n%s", name, diff)
				}
			}
		})
	}
}
