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
	"github.com/google/go-cmp/cmp/cmpopts"

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

func TestVirtualGroupName_LongNamePreservesHashSuffix(t *testing.T) {
	longWlName := strings.Repeat("a", 253)
	name1 := virtualGroupName("pg", longWlName, "group1")
	name2 := virtualGroupName("pg", longWlName, "group2")

	if len(name1) > maxPodNameLength {
		t.Errorf("name1 length %d exceeds maxPodNameLength %d", len(name1), maxPodNameLength)
	}
	if len(name2) > maxPodNameLength {
		t.Errorf("name2 length %d exceeds maxPodNameLength %d", len(name2), maxPodNameLength)
	}
	if name1 == name2 {
		t.Errorf("expected distinct names for distinct group IDs, got identical %q", name1)
	}
}

func TestBuildVirtualPodGroups(t *testing.T) {
	cases := map[string]struct {
		workload          *kueue.Workload
		wantPodGroups     int
		wantCPGs          int
		wantUnsupportable *UnsupportableReason
		wantErr           string
		checkFn           func(t *testing.T, res *WorkloadVirtualPodGroups)
	}{
		"unsupported preferred topology": {
			workload: utiltesting.MakeWorkload("wl-pref", "default").
				PodSets(
					*utiltesting.MakePodSet("main", 4).
						PreferredTopologyRequest("cloud.com/rack").
						Obj(),
				).Obj(),
			wantUnsupportable: new(ReasonPreferredTopologyNotSupported),
		},
		"plain non-TAS workload": {
			workload: utiltesting.MakeWorkload("wl-plain", "default").
				PodSets(
					*utiltesting.MakePodSet("main", 3).Obj(),
				).Obj(),
			wantPodGroups: 1,
			wantCPGs:      0,
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				pg := res.PodGroups[0]
				if pg.Spec.SchedulingPolicy.Gang == nil || pg.Spec.SchedulingPolicy.Gang.MinCount != 3 {
					t.Errorf("unexpected gang policy: %+v", pg.Spec.SchedulingPolicy.Gang)
				}
				if pg.Spec.SchedulingConstraints != nil {
					t.Errorf("expected no scheduling constraints, got: %+v", pg.Spec.SchedulingConstraints)
				}
				for i := range 3 {
					if got := res.LeafPGForPod("main", i); got != pg.Name {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, pg.Name, got)
					}
				}
			},
		},
		"single-level required topology": {
			workload: utiltesting.MakeWorkload("wl-single", "default").
				PodSets(
					*utiltesting.MakePodSet("main", 4).
						RequiredTopologyRequest("cloud.com/rack").
						Obj(),
				).Obj(),
			wantPodGroups: 1,
			wantCPGs:      0,
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				pg := res.PodGroups[0]
				if pg.Spec.SchedulingPolicy.Gang == nil || pg.Spec.SchedulingPolicy.Gang.MinCount != 4 {
					t.Errorf("unexpected gang policy: %+v", pg.Spec.SchedulingPolicy.Gang)
				}
				if len(pg.Spec.SchedulingConstraints.Topology) != 1 || pg.Spec.SchedulingConstraints.Topology[0].Key != "cloud.com/rack" {
					t.Errorf("unexpected topology constraint: %+v", pg.Spec.SchedulingConstraints)
				}
				for i := range 4 {
					if got := res.LeafPGForPod("main", i); got != pg.Name {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, pg.Name, got)
					}
				}
			},
		},
		"multi-level TAS slices": {
			workload: utiltesting.MakeWorkload("wl-multi", "default").
				PodSets(
					*utiltesting.MakePodSet("workers", 8).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 8},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 4},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "kubernetes.io/hostname", Size: 2},
						).
						Obj(),
				).Obj(),
			wantPodGroups: 4,
			wantCPGs:      3,
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				for i := range 8 {
					if got := res.LeafPGForPod("workers", i); got == "" {
						t.Errorf("expected non-empty leaf PG for replica %d", i)
					}
				}
			},
		},
		"podset group name sharing": {
			workload: utiltesting.MakeWorkload("wl-grouped", "default").
				PodSets(
					*utiltesting.MakePodSet("launcher", 1).
						RequiredTopologyRequest("cloud.com/block").
						PodSetGroup("group-a").
						Obj(),
					*utiltesting.MakePodSet("worker", 4).
						RequiredTopologyRequest("cloud.com/block").
						PodSetGroup("group-a").
						Obj(),
				).Obj(),
			wantPodGroups: 2,
			wantCPGs:      1,
		},
		"multi-level slices with single-level required topology": {
			workload: utiltesting.MakeWorkload("wl-combined", "default").
				PodSets(
					*utiltesting.MakePodSet("workers", 8).
						RequiredTopologyRequest("cloud.com/block").
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 4},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "kubernetes.io/hostname", Size: 2},
						).
						Obj(),
				).Obj(),
			wantPodGroups: 4,
			wantCPGs:      3,
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				for i := range 8 {
					if got := res.LeafPGForPod("workers", i); got == "" {
						t.Errorf("expected non-empty leaf PG for replica %d", i)
					}
				}
			},
		},
		"supports >8 children per group": {
			workload: utiltesting.MakeWorkload("wl-limit", "default").
				PodSets(
					*utiltesting.MakePodSet("large", 18).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 18},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 2},
						).
						Obj(),
				).Obj(),
			wantPodGroups: 9,
			wantCPGs:      1,
		},
		"exceeds hierarchy depth (>4 levels)": {
			workload: utiltesting.MakeWorkload("wl-depth", "default").
				PodSets(
					*utiltesting.MakePodSet("workers", 8).
						RequiredTopologyRequest("cloud.com/datacenter").
						PodSetGroup("group-a").
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 4},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 2},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "kubernetes.io/hostname", Size: 1},
						).
						Obj(),
					*utiltesting.MakePodSet("other", 1).Obj(),
				).Obj(),
			wantUnsupportable: new(ReasonHierarchyDepthExceeded),
		},
		"JobSet with launcher and 3-level sliced workers produces valid depth-4 hierarchy": {
			workload: utiltesting.MakeWorkload("wl-jobset-sliced", "default").
				PodSets(
					*utiltesting.MakePodSet("launcher", 1).Obj(),
					*utiltesting.MakePodSet("workers", 8).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 4},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 2},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "kubernetes.io/hostname", Size: 1},
						).
						Obj(),
				).Obj(),
			wantPodGroups: 9,
			wantCPGs:      7,
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				if got := res.LeafPGForPod("launcher", 0); got == "" {
					t.Errorf("expected non-empty leaf PG for launcher")
				}
				for i := range 8 {
					if got := res.LeafPGForPod("workers", i); got == "" {
						t.Errorf("expected non-empty leaf PG for worker %d", i)
					}
				}
			},
		},
		"nil workload": {
			workload: nil,
			wantErr:  "workload must not be nil",
		},
		"zero total pod count": {
			workload: utiltesting.MakeWorkload("wl-zero", "default").
				PodSets(*utiltesting.MakePodSet("main", 0).Obj()).
				Obj(),
			wantErr: "has non-positive total pods count 0",
		},
		"zero-count PodSet alongside non-zero PodSet": {
			workload: utiltesting.MakeWorkload("wl-zero-mixed", "default").
				PodSets(
					*utiltesting.MakePodSet("empty", 0).Obj(),
					*utiltesting.MakePodSet("main", 3).Obj(),
				).Obj(),
			wantPodGroups: 1,
			wantCPGs:      0,
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				if len(res.PodGroups) != 1 || res.PodGroups[0].Spec.SchedulingPolicy.Gang.MinCount != 3 {
					t.Errorf("unexpected pod group: %+v", res.PodGroups)
				}
			},
		},
		"invalid slice size non-positive": {
			workload: utiltesting.MakeWorkload("wl-invalid-size", "default").
				PodSets(
					*utiltesting.MakePodSet("main", 4).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 0},
						).
						Obj(),
				).Obj(),
			wantErr: "has non-positive size 0",
		},
		"invalid slice size not evenly divisible": {
			workload: utiltesting.MakeWorkload("wl-indivisible", "default").
				PodSets(
					*utiltesting.MakePodSet("main", 12).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 6},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 4},
						).
						Obj(),
				).Obj(),
			wantErr: "must evenly divide parent size",
		},
		"podset named root does not collide with root composite podgroup": {
			workload: utiltesting.MakeWorkload("wl-root-name", "default").
				PodSets(
					*utiltesting.MakePodSet("root", 4).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 4},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 2},
						).
						Obj(),
					*utiltesting.MakePodSet("other", 1).Obj(),
				).Obj(),
			wantPodGroups: 3,
			wantCPGs:      2,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := BuildVirtualPodGroups(tc.workload)

			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}

			if tc.wantUnsupportable != nil {
				if err == nil {
					t.Fatalf("expected unsupportable error %s, got nil", *tc.wantUnsupportable)
				}
				var unsuppErr *ErrUnsupportableWorkload
				if !errors.As(err, &unsuppErr) {
					t.Fatalf("expected ErrUnsupportableWorkload, got: %v", err)
				}
				if diff := cmp.Diff(*tc.wantUnsupportable, unsuppErr.Reason); diff != "" {
					t.Errorf("unexpected unsupportable reason (-want,+got):\n%s", diff)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.wantPodGroups, len(res.PodGroups), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected PodGroups count (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCPGs, len(res.CompositePodGroups), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("unexpected CompositePodGroups count (-want,+got):\n%s", diff)
			}
			if tc.checkFn != nil {
				tc.checkFn(t, res)
			}
		})
	}
}
