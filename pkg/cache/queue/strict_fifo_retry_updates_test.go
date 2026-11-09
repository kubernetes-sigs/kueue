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

package queue

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestStrictFIFONoFitClusterQueueUpdates(t *testing.T) {
	cases := map[string]struct {
		oldCohort kueue.CohortReference
		updated   *kueue.ClusterQueue
		retry     bool
		inflight  bool
		wantReady sets.Set[kueue.ClusterQueueReference]
	}{
		"quota update releases the retained head": {
			updated: utiltestingapi.MakeClusterQueue("changed").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			retry:     true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed"),
		},
		"effective quota update releases the retained head": {
			updated: utiltestingapi.MakeClusterQueue("changed").
				QueueingStrategy(kueue.StrictFIFO).
				EffectiveQuotas(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			retry:     true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed"),
		},
		"quota update invalidates an inflight evaluation": {
			updated: utiltestingapi.MakeClusterQueue("changed").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			retry:     true,
			inflight:  true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed"),
		},
		"quota update releases borrowing siblings in nested cohorts": {
			oldCohort: "old-leaf",
			updated: utiltestingapi.MakeClusterQueue("changed").
				QueueingStrategy(kueue.StrictFIFO).
				Cohort("old-leaf").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
				Obj(),
			retry:     true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed", "old-peer"),
		},
		"cohort move releases both old and new borrowing trees": {
			oldCohort: "old-leaf",
			updated:   utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Cohort("new-leaf").Obj(),
			retry:     true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed", "old-peer", "new-peer"),
		},
		"cohort move invalidates inflight evaluations in both trees": {
			oldCohort: "old-leaf",
			updated:   utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Cohort("new-leaf").Obj(),
			retry:     true,
			inflight:  true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed", "old-peer", "new-peer"),
		},
		"leaving a cohort releases its remaining siblings": {
			oldCohort: "old-leaf",
			updated:   utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Obj(),
			retry:     true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed", "old-peer"),
		},
		"joining a cohort releases its new siblings": {
			updated:   utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Cohort("new-leaf").Obj(),
			retry:     true,
			wantReady: sets.New[kueue.ClusterQueueReference]("changed", "new-peer"),
		},
		"status only update preserves the wait": {
			oldCohort: "old-leaf",
			updated:   utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Cohort("old-leaf").Obj(),
		},
		"status only update preserves inflight retry control": {
			oldCohort: "old-leaf",
			updated:   utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Cohort("old-leaf").Obj(),
			inflight:  true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cl := testingclock.NewFakeClock(time.Now())
			client := utiltesting.NewClientBuilder().
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			m, requeuer := NewManagerForUnitTestsWithRequeuer(client, nil, WithClock(cl), WithPreemptionExpectations(preemptexpectations.New()))
			for _, cohort := range []*kueue.Cohort{
				utiltestingapi.MakeCohort("old-leaf").Parent("old-root").Obj(),
				utiltestingapi.MakeCohort("old-peer-leaf").Parent("old-root").Obj(),
				utiltestingapi.MakeCohort("new-leaf").Parent("new-root").Obj(),
				utiltestingapi.MakeCohort("new-peer-leaf").Parent("new-root").Obj(),
			} {
				m.AddOrUpdateCohort(ctx, cohort)
			}
			cqs := []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("changed").QueueingStrategy(kueue.StrictFIFO).Cohort(tc.oldCohort).Obj(),
				utiltestingapi.MakeClusterQueue("old-peer").QueueingStrategy(kueue.StrictFIFO).Cohort("old-peer-leaf").Obj(),
				utiltestingapi.MakeClusterQueue("new-peer").QueueingStrategy(kueue.StrictFIFO).Cohort("new-peer-leaf").Obj(),
				utiltestingapi.MakeClusterQueue("unrelated").QueueingStrategy(kueue.StrictFIFO).Obj(),
			}
			for _, cq := range cqs {
				if err := m.AddClusterQueue(ctx, cq); err != nil {
					t.Fatal(err)
				}
				lq := utiltestingapi.MakeLocalQueue(cq.Name, "ns").ClusterQueue(cq.Name).Obj()
				if err := m.AddLocalQueue(ctx, lq); err != nil {
					t.Fatal(err)
				}
				wl := utiltestingapi.MakeWorkload(cq.Name, "ns").Queue(kueue.LocalQueueName(cq.Name)).Generation(1).Request(corev1.ResourceCPU, "2").Obj()
				if err := client.Create(ctx, wl); err != nil {
					t.Fatal(err)
				}
				if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
					t.Fatal(err)
				}
			}
			requeuer.ProcessRequeues(ctx)
			heads := m.Heads(ctx)
			if len(heads) != len(cqs) {
				t.Fatalf("Heads() returned %d heads, want %d", len(heads), len(cqs))
			}
			if !tc.inflight {
				for i := range heads {
					m.RequeueWorkload(ctx, &heads[i].Info, RequeueReasonNoFit, "")
				}
				for _, cq := range cqs {
					if head := m.PopFrom(kueue.ClusterQueueReference(cq.Name)); head != nil {
						t.Fatalf("unchanged NoFit head %s retried before the update", head.Obj.Name)
					}
				}
			}
			updated := tc.updated.DeepCopy()
			updated.Status.PendingWorkloads++
			if err := m.UpdateClusterQueue(updated, tc.retry); err != nil {
				t.Fatal(err)
			}
			if tc.inflight {
				for i := range heads {
					m.RequeueWorkload(ctx, &heads[i].Info, RequeueReasonNoFit, "")
				}
			}
			// Neither the queue clock nor the batched requeuer advances here.
			for _, cq := range cqs {
				cqName := kueue.ClusterQueueReference(cq.Name)
				got := m.PopFrom(cqName)
				if tc.wantReady.Has(cqName) {
					if got == nil || got.Obj.Name != cq.Name {
						t.Errorf("PopFrom(%s) = %v, want its retained head", cqName, got)
					}
				} else if got != nil {
					t.Errorf("unaffected queue %s retried %s before its fallback", cqName, got.Obj.Name)
				}
			}
		})
	}
}

func TestStrictFIFONoFitClusterQueueUpdateWakesHeads(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cl := testingclock.NewFakeClock(time.Now())
	wl := utiltestingapi.MakeWorkload("head", "ns").Queue("lq").Generation(1).Obj()
	client := utiltesting.NewClientBuilder().WithObjects(wl).
		WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
	m, requeuer := NewManagerForUnitTestsWithRequeuer(client, nil, WithClock(cl), WithPreemptionExpectations(preemptexpectations.New()))
	if err := m.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.StrictFIFO).Obj()); err != nil {
		t.Fatal(err)
	}
	if err := m.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
		t.Fatal(err)
	}
	if err := m.AddOrUpdateWorkload(ctx, log, wl); err != nil {
		t.Fatal(err)
	}
	requeuer.ProcessRequeues(ctx)
	go m.CleanUpOnContext(ctx)
	heads := m.Heads(ctx)
	if len(heads) != 1 {
		t.Fatalf("initial Heads() returned %d heads, want 1", len(heads))
	}
	m.RequeueWorkload(ctx, &heads[0].Info, RequeueReasonNoFit, "")
	done := make(chan []Head, 1)
	go func() { done <- m.Heads(ctx) }()
	if err := wait.PollUntilContextCancel(ctx, time.Millisecond, true, func(context.Context) (bool, error) { return cl.HasWaiters(), nil }); err != nil {
		t.Fatal("Heads did not enter its timed wait: ", err)
	}
	updated := utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.StrictFIFO).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).Obj()
	if err := m.UpdateClusterQueue(updated, true); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if len(got) != 1 || got[0].Obj.Name != wl.Name {
			t.Fatalf("updated Heads() = %v, want the retained head", got)
		}
	case <-ctx.Done():
		t.Fatal("quota update did not wake Heads without advancing the fallback clock or batch")
	}
}

func TestStrictFIFONoFitRequeueEffectiveInputs(t *testing.T) {
	runtimeWL := utiltestingapi.MakeWorkload("head", "ns").Queue("lq").Generation(1).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).RuntimeClass("runtime").Request(corev1.ResourceCPU, "2").Obj()).Obj()
	defaultedWL := utiltestingapi.MakeWorkload("head", "ns").Queue("lq").Generation(1).Obj()
	draWL := utiltestingapi.MakeWorkload("head", "ns").Queue("lq").Generation(1).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).PodSpec(corev1.PodSpec{
			ResourceClaims: []corev1.PodResourceClaim{{Name: "rc", ResourceClaimTemplateName: new("claim-template")}},
		}).Obj()).Obj()
	cases := map[string]struct {
		wl                  *kueue.Workload
		runtimeClass        *nodev1.RuntimeClass
		replacementRuntime  *nodev1.RuntimeClass
		limitRange          *corev1.LimitRange
		updatedLimitRange   *corev1.LimitRange
		preprocessedCharges map[kueue.PodSetReference]corev1.ResourceList
		wantRequests        corev1.ResourceList
		wantRetry           bool
	}{
		"runtime class recreation changes effective requests without changing generation": {
			wl:                 runtimeWL,
			runtimeClass:       utiltesting.MakeRuntimeClass("runtime", "handler").PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).Obj(),
			replacementRuntime: utiltesting.MakeRuntimeClass("runtime", "handler").PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}).Obj(),
			wantRequests:       corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
			wantRetry:          true,
		},
		"limit range changes effective requests without changing generation": {
			wl:                defaultedWL,
			limitRange:        utiltesting.MakeLimitRange("limits", "ns").WithValue("DefaultRequest", corev1.ResourceCPU, "4").Obj(),
			updatedLimitRange: utiltesting.MakeLimitRange("limits", "ns").WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj(),
			wantRequests:      corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			wantRetry:         true,
		},
		"unchanged effective requests retain the wait": {
			wl:           runtimeWL,
			runtimeClass: utiltesting.MakeRuntimeClass("runtime", "handler").PodOverhead(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}).Obj(),
			wantRequests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
		},
		"DRA preprocessing is preserved and does not cause a false input change": {
			wl:                  draWL,
			preprocessedCharges: map[kueue.PodSetReference]corev1.ResourceList{kueue.DefaultPodSetName: {"example.com/gpu": resource.MustParse("1")}},
			wantRequests:        corev1.ResourceList{"example.com/gpu": resource.MustParse("1")},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)
			ctx, log := utiltesting.ContextWithLog(t)
			cl := testingclock.NewFakeClock(time.Now())
			wl := tc.wl.DeepCopy()
			client := utiltesting.NewClientBuilder().WithObjects(wl).
				WithIndex(&corev1.LimitRange{}, indexer.LimitRangeHasContainerOrPodType, indexer.IndexLimitRangeHasContainerOrPodType).Build()
			if tc.runtimeClass != nil {
				if err := client.Create(ctx, tc.runtimeClass.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.limitRange != nil {
				if err := client.Create(ctx, tc.limitRange.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			m, requeuer := NewManagerForUnitTestsWithRequeuer(client, nil, WithClock(cl), WithPreemptionExpectations(preemptexpectations.New()))
			m.SetDRAReconcileChannel(make(chan event.TypedGenericEvent[*kueue.Workload], 1))
			if err := m.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").QueueingStrategy(kueue.StrictFIFO).Obj()); err != nil {
				t.Fatal(err)
			}
			if err := m.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj()); err != nil {
				t.Fatal(err)
			}
			var options []workload.InfoOption
			if tc.preprocessedCharges != nil {
				options = append(options, workload.WithPreprocessedDRAResources(tc.preprocessedCharges, nil))
			}
			if err := m.AddOrUpdateWorkload(ctx, log, wl, options...); err != nil {
				t.Fatal(err)
			}
			requeuer.ProcessRequeues(ctx)
			heads := m.Heads(ctx)
			if len(heads) != 1 {
				t.Fatalf("Heads() returned %d heads, want 1", len(heads))
			}
			if tc.replacementRuntime != nil {
				// RuntimeClass overhead is immutable; a delete/recreate is a real
				// external-input change without a Workload generation change.
				if err := client.Delete(ctx, tc.runtimeClass.DeepCopy()); err != nil {
					t.Fatal(err)
				}
				if err := client.Create(ctx, tc.replacementRuntime.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.updatedLimitRange != nil {
				if err := client.Delete(ctx, tc.limitRange.DeepCopy()); err != nil {
					t.Fatal(err)
				}
				if err := client.Create(ctx, tc.updatedLimitRange.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}
			// This is the resource event's push while the old inputs are still
			// checked out. Requeue must compare what was actually evaluated.
			if err := m.AddOrUpdateWorkload(ctx, log, wl, options...); err != nil {
				t.Fatal(err)
			}
			if !m.RequeueWorkload(ctx, &heads[0].Info, RequeueReasonNoFit, "") {
				t.Fatal("RequeueWorkload rejected the checked-out head")
			}
			if heads[0].Obj.Generation != wl.Generation {
				t.Fatalf("generation changed from %d to %d", wl.Generation, heads[0].Obj.Generation)
			}
			wantRequests := resources.NewRequestsFromResourceList(tc.wantRequests)
			if got := heads[0].TotalRequests; len(got) != 1 || !resources.Equal(got[0].Requests, wantRequests) {
				t.Fatalf("requeued requests = %v, want %v", got, wantRequests)
			}
			got := m.PopFrom("cq")
			if tc.wantRetry {
				if got == nil || got.Obj.Name != wl.Name {
					t.Fatalf("PopFrom() = %v, want immediate retry with changed effective inputs", got)
				}
			} else if got != nil {
				t.Fatalf("unchanged evaluated inputs retried before fallback: %s", got.Obj.Name)
			}
		})
	}
}
