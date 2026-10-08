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
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

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
			wantUnsupportable: new(ReasonPreferredTopology),
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
		"exceeds child limit (>8 children)": {
			workload: utiltesting.MakeWorkload("wl-limit", "default").
				PodSets(
					*utiltesting.MakePodSet("large", 18).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 18},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 2},
						).
						Obj(),
				).Obj(),
			wantUnsupportable: new(ReasonChildLimitExceeded),
		},
		"exceeds hierarchy depth (>4 levels)": {
			workload: utiltesting.MakeWorkload("wl-depth", "default").
				PodSets(
					*utiltesting.MakePodSet("workers", 8).
						SliceRequiredTopologyConstraints(
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/block", Size: 8},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/rack", Size: 4},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "cloud.com/row", Size: 2},
							kueue.PodsetSliceRequiredTopologyConstraint{Topology: "kubernetes.io/hostname", Size: 1},
						).
						Obj(),
					*utiltesting.MakePodSet("other", 1).Obj(),
				).Obj(),
			wantUnsupportable: new(ReasonHierarchyDepthExceeded),
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
