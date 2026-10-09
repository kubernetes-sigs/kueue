//go:build !exclude_scheduler_library

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

package was

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestVirtualPodGroupName(t *testing.T) {
	tests := map[string]struct {
		wlName         string
		wantPrefix     string
		checkLen       bool
		forbidContains []string
	}{
		"short name": {
			wlName:     "wl-1",
			wantPrefix: "virtual-pg-wl-1-",
		},
		"long name capped to maxPodNameLength": {
			wlName:   strings.Repeat("a", 300),
			checkLen: true,
		},
		"long name with dot and hyphen at truncation boundary stripped": {
			wlName:         strings.Repeat("a", 235) + ".-" + strings.Repeat("b", 50),
			checkLen:       true,
			forbidContains: []string{".-", "--"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := VirtualPodGroupName(tc.wlName)
			if tc.wantPrefix != "" && !strings.HasPrefix(got, tc.wantPrefix) {
				t.Errorf("VirtualPodGroupName() = %q, want prefix %q", got, tc.wantPrefix)
			}
			if tc.checkLen && len(got) > maxPodNameLength {
				t.Errorf("VirtualPodGroupName() length = %d, exceeds max %d", len(got), maxPodNameLength)
			}
			for _, forbidden := range tc.forbidContains {
				if strings.Contains(got, forbidden) {
					t.Errorf("VirtualPodGroupName() = %q contains forbidden substring %q", got, forbidden)
				}
			}
		})
	}
}

func TestBuildVirtualPodGroup(t *testing.T) {
	cases := map[string]struct {
		workload     *kueue.Workload
		wantMinCount int32
		wantErr      error
	}{
		"nil workload": {
			workload: nil,
			wantErr:  errors.New("workload must not be nil"),
		},
		"zero pod count workload": {
			workload: utiltesting.MakeWorkload("wl-zero", "default").
				PodSets(*utiltesting.MakePodSet("empty", 0).Obj()).
				Obj(),
			wantErr: errors.New(`workload "wl-zero" has non-positive total pods count 0`),
		},
		"overflow pod count workload": {
			workload: utiltesting.MakeWorkload("wl-overflow", "default").
				PodSets(
					*utiltesting.MakePodSet("set1", math.MaxInt32).Obj(),
					*utiltesting.MakePodSet("set2", 1).Obj(),
				).
				Obj(),
			wantErr: errors.New(`workload "wl-overflow" total pods count 2147483648 exceeds max int32`),
		},
		"single podset workload": {
			workload: utiltesting.MakeWorkload("wl-single", "default").
				PodSets(*utiltesting.MakePodSet("main", 4).Obj()).
				Obj(),
			wantMinCount: 4,
		},
		"multi podset workload": {
			workload: utiltesting.MakeWorkload("wl-multi", "default").
				PodSets(
					*utiltesting.MakePodSet("driver", 1).Obj(),
					*utiltesting.MakePodSet("workers", 8).Obj(),
				).
				Obj(),
			wantMinCount: 9,
		},
		"long workload name truncated": {
			workload: utiltesting.MakeWorkload(strings.Repeat("a", 300), "default").
				PodSets(*utiltesting.MakePodSet("main", 2).Obj()).
				Obj(),
			wantMinCount: 2,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			pg, err := BuildVirtualPodGroup(tc.workload)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("got nil error, want %v", tc.wantErr)
				}
				if diff := cmp.Diff(tc.wantErr.Error(), err.Error()); diff != "" {
					t.Errorf("unexpected error (-want,+got):\n%s", diff)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if pg == nil {
				t.Fatal("expected non-nil PodGroup")
			}
			if pg.Namespace != tc.workload.Namespace {
				t.Errorf("got namespace %q, want %q", pg.Namespace, tc.workload.Namespace)
			}

			if diff := cmp.Diff(VirtualPodGroupName(tc.workload.Name), pg.Name); diff != "" {
				t.Errorf("unexpected name (-want,+got):\n%s", diff)
			}

			if pg.Spec.SchedulingPolicy.Gang == nil {
				t.Fatal("expected non-nil gang policy")
			}
			if diff := cmp.Diff(tc.wantMinCount, pg.Spec.SchedulingPolicy.Gang.MinCount); diff != "" {
				t.Errorf("unexpected minCount (-want,+got):\n%s", diff)
			}
			if pg.Spec.SchedulingConstraints != nil {
				t.Errorf("expected nil scheduling constraints, got: %+v", pg.Spec.SchedulingConstraints)
			}
		})
	}
}

func TestBuildVirtualPodGroup_DistinctLongNames(t *testing.T) {
	wl1 := utiltesting.MakeWorkload(strings.Repeat("a", 300)+"1", "default").
		PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
		Obj()
	wl2 := utiltesting.MakeWorkload(strings.Repeat("a", 300)+"2", "default").
		PodSets(*utiltesting.MakePodSet("main", 1).Obj()).
		Obj()

	pg1, err := BuildVirtualPodGroup(wl1)
	if err != nil {
		t.Fatalf("unexpected error for wl1: %v", err)
	}
	pg2, err := BuildVirtualPodGroup(wl2)
	if err != nil {
		t.Fatalf("unexpected error for wl2: %v", err)
	}

	if pg1.Name == pg2.Name {
		t.Errorf("collision on long names: %q == %q", pg1.Name, pg2.Name)
	}
}
