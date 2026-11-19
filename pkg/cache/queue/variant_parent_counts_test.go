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
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// makeVariantInfo returns a Variant of the Parent named parentName, or a
// regular Workload when parentName is empty.
func makeVariantInfo(log logr.Logger, now time.Time, name, parentName string) *workload.Info {
	wl := utiltestingapi.MakeWorkload(name, defaultNamespace).
		Queue("lq").
		Creation(now)
	if parentName != "" {
		wl = wl.ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), parentName, "uid-"+parentName)
	}
	return workload.NewInfo(log, wl.Obj())
}

func TestVariantParentCountsExcess(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		disableConcurrentAdmission bool
		// Each entry is the Parent name of a pending workload; an empty name
		// is a regular Workload.
		active           []string
		inadmissible     []string
		inflight         []string
		wantActive       int
		wantInadmissible int
	}{
		"regular workloads only": {
			active:       []string{"", ""},
			inadmissible: []string{""},
		},
		"all Variants of a Parent active": {
			active:     []string{"a", "a", "a"},
			wantActive: 2,
		},
		"all Variants of a Parent inadmissible": {
			inadmissible:     []string{"a", "a", "a"},
			wantInadmissible: 2,
		},
		"Parent with active and inadmissible Variants counts as active": {
			active:           []string{"a"},
			inadmissible:     []string{"a", "a"},
			wantInadmissible: 2,
		},
		"Parent with an inflight Variant counts as active": {
			inadmissible:     []string{"a", "a"},
			inflight:         []string{"a"},
			wantInadmissible: 2,
		},
		"inflight and active Variants of the same Parent": {
			active:     []string{"a"},
			inflight:   []string{"a"},
			wantActive: 1,
		},
		"two Parents and a regular workload": {
			active:           []string{"a", "a", "b", ""},
			inadmissible:     []string{"b", "b"},
			wantActive:       1,
			wantInadmissible: 2,
		},
		"gate disabled": {
			disableConcurrentAdmission: true,
			active:                     []string{"a", "a"},
			inadmissible:               []string{"a"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, !tc.disableConcurrentAdmission)
			_, log := utiltesting.ContextWithLog(t)
			counts := newVariantParentCounts()
			for i, parent := range tc.active {
				counts.addActive(makeVariantInfo(log, now, fmt.Sprintf("active-%d", i), parent))
			}
			for i, parent := range tc.inadmissible {
				counts.addInadmissible(makeVariantInfo(log, now, fmt.Sprintf("inadmissible-%d", i), parent))
			}
			for i, parent := range tc.inflight {
				// Mirror the Pop flow: the workload leaves the active bucket.
				info := makeVariantInfo(log, now, fmt.Sprintf("inflight-%d", i), parent)
				counts.addActive(info)
				counts.moveActiveToInflight(info)
			}

			gotActive, gotInadmissible := counts.excess()
			if gotActive != tc.wantActive || gotInadmissible != tc.wantInadmissible {
				t.Errorf("excess() = (%d, %d), want (%d, %d)", gotActive, gotInadmissible, tc.wantActive, tc.wantInadmissible)
			}
		})
	}
}

func TestPendingResourcesCountEachParentOnce(t *testing.T) {
	type step struct {
		name string
		do   func(logr.Logger, *ClusterQueue)
		// wantCPU and wantCPUGateOff are the pending CPU after the step with
		// the ConcurrentAdmission gate enabled and disabled.
		wantCPU        string
		wantCPUGateOff string
	}
	now := time.Now()
	variant := func(log logr.Logger, i int, cpu string) *workload.Info {
		return workload.NewInfo(log, utiltestingapi.MakeWorkload(fmt.Sprintf("variant-%d", i), defaultNamespace).
			Queue("lq").
			Creation(now.Add(time.Duration(i)*time.Second)).
			ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "uid-parent").
			Request(corev1.ResourceCPU, cpu).
			Obj())
	}
	var popped *workload.Info
	steps := []step{
		{
			name:           "three Variants of one Parent and one regular Workload pending",
			do:             func(logr.Logger, *ClusterQueue) {},
			wantCPU:        "3",
			wantCPUGateOff: "5",
		},
		{
			name: "one Variant checked out",
			do: func(_ logr.Logger, cq *ClusterQueue) {
				popped = cq.Pop()
			},
			wantCPU:        "3",
			wantCPUGateOff: "5",
		},
		{
			name: "the checked-out Variant requeued as inadmissible",
			do: func(log logr.Logger, cq *ClusterQueue) {
				cq.requeueIfNotPresent(log, popped, false, RequeueReasonGeneric, "")
			},
			wantCPU:        "3",
			wantCPUGateOff: "5",
		},
		{
			// The Parent's requests are taken from the Variant updated last.
			name: "one Variant in the heap updated to request 2 CPU",
			do: func(log logr.Logger, cq *ClusterQueue) {
				cq.PushOrUpdate(variant(log, 2, "2"))
			},
			wantCPU:        "4",
			wantCPUGateOff: "6",
		},
		{
			// The inflight Variants are the only Variants of the Parent left, and
			// request different CPU.
			name: "the inadmissible Variant deleted and the other two checked out",
			do: func(log logr.Logger, cq *ClusterQueue) {
				cq.Delete(log, workload.Key(popped.Obj))
				cq.Pop()
				cq.Pop()
			},
			wantCPU:        "4",
			wantCPUGateOff: "5",
		},
		{
			name: "the checked-out Variants deleted",
			do: func(log logr.Logger, cq *ClusterQueue) {
				cq.Delete(log, workload.NewReference(defaultNamespace, "variant-1"))
				cq.Delete(log, workload.NewReference(defaultNamespace, "variant-2"))
			},
			wantCPU:        "2",
			wantCPUGateOff: "2",
		},
	}
	for _, gateOn := range []bool{true, false} {
		t.Run(fmt.Sprintf("ConcurrentAdmission=%t", gateOn), func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, gateOn)
			ctx, log := utiltesting.ContextWithLog(t)
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))
			for i := range 3 {
				cq.PushOrUpdate(variant(log, i, "1"))
			}
			cq.PushOrUpdate(workload.NewInfo(log, utiltestingapi.MakeWorkload("regular", defaultNamespace).
				Queue("lq").
				Creation(now.Add(3*time.Second)).
				Request(corev1.ResourceCPU, "2").
				Obj()))
			for _, s := range steps {
				s.do(log, cq)
				want := s.wantCPU
				if !gateOn {
					want = s.wantCPUGateOff
				}
				wantAmount := resources.AmountFromQuantity(corev1.ResourceCPU, resource.MustParse(want))
				if got := cq.pendingResources()[corev1.ResourceCPU]; !got.Equal(wantAmount) {
					t.Errorf("After %q: pending CPU = %sm, want %sm (%s CPU)", s.name, got, wantAmount, want)
				}
			}
		})
	}
}

func TestPendingCountsEachParentOnce(t *testing.T) {
	tests := map[string]struct {
		disableConcurrentAdmission bool
		wantActive                 int
		wantInadmissible           int
	}{
		"gate enabled": {
			wantActive:       2,
			wantInadmissible: 0,
		},
		"gate disabled": {
			disableConcurrentAdmission: true,
			wantActive:                 3,
			wantInadmissible:           1,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, !tc.disableConcurrentAdmission)
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now()
			cq := newClusterQueueImpl(ctx, nil, defaultOrdering, testingclock.NewFakeClock(now))

			// Three Variants of one Parent and one regular Workload.
			for i := range 3 {
				cq.PushOrUpdate(makeVariantInfo(log, now.Add(time.Duration(i)*time.Second), fmt.Sprintf("variant-%d", i), "parent"))
			}
			cq.PushOrUpdate(makeVariantInfo(log, now.Add(3*time.Second), "regular", ""))

			// Check one Variant out and requeue another one as inadmissible.
			if inflight := cq.Pop(); inflight == nil {
				t.Fatal("expected to pop a workload")
			}
			cq.requeueIfNotPresent(log, cq.Pop(), false, RequeueReasonGeneric, "")

			active, inadmissible := cq.Pending()
			if active != tc.wantActive || inadmissible != tc.wantInadmissible {
				t.Errorf("Pending() = (%d, %d), want (%d, %d)", active, inadmissible, tc.wantActive, tc.wantInadmissible)
			}
			if got, want := cq.PendingTotal(), tc.wantActive+tc.wantInadmissible; got != want {
				t.Errorf("PendingTotal() = %d, want %d", got, want)
			}
			// The LocalQueue counts include the inflight Variant in active.
			lqActive, lqInadmissible := cq.PendingInLocalQueue(utilqueue.NewLocalQueueReference(defaultNamespace, "lq"))
			if lqActive != tc.wantActive || lqInadmissible != tc.wantInadmissible {
				t.Errorf("PendingInLocalQueue() = (%d, %d), want (%d, %d)", lqActive, lqInadmissible, tc.wantActive, tc.wantInadmissible)
			}
		})
	}
}
