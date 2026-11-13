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
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// makeVariantInfo returns a Variant of the Parent with parentUID, or a regular
// Workload when parentUID is empty.
func makeVariantInfo(log logr.Logger, now time.Time, name, parentUID string) *workload.Info {
	wl := utiltestingapi.MakeWorkload(name, defaultNamespace).
		Queue("lq").
		Creation(now)
	if parentUID != "" {
		wl = wl.ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-"+parentUID, parentUID)
	}
	return workload.NewInfo(log, wl.Obj())
}

func TestVariantParentCountsExcess(t *testing.T) {
	now := time.Now()
	tests := map[string]struct {
		disableConcurrentAdmission bool
		// Each entry is the Parent UID of a pending workload; an empty UID is
		// a regular Workload.
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
			for i, uid := range tc.active {
				counts.addActive(makeVariantInfo(log, now, fmt.Sprintf("active-%d", i), uid))
			}
			for i, uid := range tc.inadmissible {
				counts.addInadmissible(makeVariantInfo(log, now, fmt.Sprintf("inadmissible-%d", i), uid))
			}
			for i, uid := range tc.inflight {
				// Mirror the Pop flow: the workload leaves the active bucket.
				info := makeVariantInfo(log, now, fmt.Sprintf("inflight-%d", i), uid)
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
		})
	}
}
