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
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

func testPG(wlName, groupID, topology string, count int32, parentGroupID string) *schedulingv1beta1.PodGroup {
	pg := newVPG(virtualGroupName("pg", wlName, groupID), "default", topology, count)
	if parentGroupID != "" {
		pg.Spec.ParentCompositePodGroupName = new(virtualGroupName("cpg", wlName, parentGroupID))
	}
	return pg
}

func testCPG(wlName, groupID, topology string, groupCount int32, parentGroupID string) *schedulingv1alpha3.CompositePodGroup {
	cpg := newVCPG(virtualGroupName("cpg", wlName, groupID), "default", topology, groupCount)
	if parentGroupID != "" {
		cpg.Spec.ParentCompositePodGroupName = new(virtualGroupName("cpg", wlName, parentGroupID))
	}
	return cpg
}

func testLeafName(wlName, groupID string) string {
	return virtualGroupName("pg", wlName, groupID)
}

func TestBuildVirtualPodGroups(t *testing.T) {
	cases := map[string]struct {
		workload               *kueue.Workload
		wantPodGroups          []*schedulingv1beta1.PodGroup
		wantCompositePodGroups []*schedulingv1alpha3.CompositePodGroup
		wantUnsupportable      *UnsupportableReason
		wantErr                string
		checkFn                func(t *testing.T, res *WorkloadVirtualPodGroups)
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
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-plain", "p.main", "", 3, ""),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				wantLeaf := testLeafName("wl-plain", "p.main")
				for i := range 3 {
					if got := res.LeafPGForPod("main", i); got != wantLeaf {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, wantLeaf, got)
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
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-single", "p.main", "cloud.com/rack", 4, ""),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				wantLeaf := testLeafName("wl-single", "p.main")
				for i := range 4 {
					if got := res.LeafPGForPod("main", i); got != wantLeaf {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, wantLeaf, got)
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
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-multi", "s.psworkers-l2-s0", "kubernetes.io/hostname", 2, "s.psworkers-l1-s0"),
				testPG("wl-multi", "s.psworkers-l2-s1", "kubernetes.io/hostname", 2, "s.psworkers-l1-s0"),
				testPG("wl-multi", "s.psworkers-l2-s2", "kubernetes.io/hostname", 2, "s.psworkers-l1-s1"),
				testPG("wl-multi", "s.psworkers-l2-s3", "kubernetes.io/hostname", 2, "s.psworkers-l1-s1"),
			},
			wantCompositePodGroups: []*schedulingv1alpha3.CompositePodGroup{
				testCPG("wl-multi", "s.psworkers-l0-s0", "cloud.com/block", 2, ""),
				testCPG("wl-multi", "s.psworkers-l1-s0", "cloud.com/rack", 2, "s.psworkers-l0-s0"),
				testCPG("wl-multi", "s.psworkers-l1-s1", "cloud.com/rack", 2, "s.psworkers-l0-s0"),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				wantLeaves := []string{
					testLeafName("wl-multi", "s.psworkers-l2-s0"),
					testLeafName("wl-multi", "s.psworkers-l2-s0"),
					testLeafName("wl-multi", "s.psworkers-l2-s1"),
					testLeafName("wl-multi", "s.psworkers-l2-s1"),
					testLeafName("wl-multi", "s.psworkers-l2-s2"),
					testLeafName("wl-multi", "s.psworkers-l2-s2"),
					testLeafName("wl-multi", "s.psworkers-l2-s3"),
					testLeafName("wl-multi", "s.psworkers-l2-s3"),
				}
				for i, want := range wantLeaves {
					if got := res.LeafPGForPod("workers", i); got != want {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, want, got)
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
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-grouped", "p.launcher", "cloud.com/block", 1, "g.podsetgroup.group-a"),
				testPG("wl-grouped", "p.worker", "cloud.com/block", 4, "g.podsetgroup.group-a"),
			},
			wantCompositePodGroups: []*schedulingv1alpha3.CompositePodGroup{
				testCPG("wl-grouped", "g.podsetgroup.group-a", "cloud.com/block", 2, ""),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				if got, want := res.LeafPGForPod("launcher", 0), testLeafName("wl-grouped", "p.launcher"); got != want {
					t.Errorf("expected leaf PG for launcher to be %q, got %q", want, got)
				}
				for i := range 4 {
					if got, want := res.LeafPGForPod("worker", i), testLeafName("wl-grouped", "p.worker"); got != want {
						t.Errorf("expected leaf PG for worker %d to be %q, got %q", i, want, got)
					}
				}
			},
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
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-combined", "s.psworkers-l1-s0", "kubernetes.io/hostname", 2, "s.psworkers-l0-s0"),
				testPG("wl-combined", "s.psworkers-l1-s1", "kubernetes.io/hostname", 2, "s.psworkers-l0-s0"),
				testPG("wl-combined", "s.psworkers-l1-s2", "kubernetes.io/hostname", 2, "s.psworkers-l0-s1"),
				testPG("wl-combined", "s.psworkers-l1-s3", "kubernetes.io/hostname", 2, "s.psworkers-l0-s1"),
			},
			wantCompositePodGroups: []*schedulingv1alpha3.CompositePodGroup{
				testCPG("wl-combined", "g.podset.workers", "cloud.com/block", 2, ""),
				testCPG("wl-combined", "s.psworkers-l0-s0", "cloud.com/rack", 2, "g.podset.workers"),
				testCPG("wl-combined", "s.psworkers-l0-s1", "cloud.com/rack", 2, "g.podset.workers"),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				wantLeaves := []string{
					testLeafName("wl-combined", "s.psworkers-l1-s0"),
					testLeafName("wl-combined", "s.psworkers-l1-s0"),
					testLeafName("wl-combined", "s.psworkers-l1-s1"),
					testLeafName("wl-combined", "s.psworkers-l1-s1"),
					testLeafName("wl-combined", "s.psworkers-l1-s2"),
					testLeafName("wl-combined", "s.psworkers-l1-s2"),
					testLeafName("wl-combined", "s.psworkers-l1-s3"),
					testLeafName("wl-combined", "s.psworkers-l1-s3"),
				}
				for i, want := range wantLeaves {
					if got := res.LeafPGForPod("workers", i); got != want {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, want, got)
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
			wantPodGroups: func() []*schedulingv1beta1.PodGroup {
				var pgs []*schedulingv1beta1.PodGroup
				for s := range 9 {
					pgs = append(pgs, testPG("wl-limit", fmt.Sprintf("s.pslarge-l1-s%d", s), "cloud.com/rack", 2, "s.pslarge-l0-s0"))
				}
				return pgs
			}(),
			wantCompositePodGroups: []*schedulingv1alpha3.CompositePodGroup{
				testCPG("wl-limit", "s.pslarge-l0-s0", "cloud.com/block", 9, ""),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				for i := range 18 {
					wantLeaf := testLeafName("wl-limit", fmt.Sprintf("s.pslarge-l1-s%d", i/2))
					if got := res.LeafPGForPod("large", i); got != wantLeaf {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, wantLeaf, got)
					}
				}
			},
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
			wantPodGroups: func() []*schedulingv1beta1.PodGroup {
				pgs := []*schedulingv1beta1.PodGroup{
					testPG("wl-jobset-sliced", "p.launcher", "", 1, "r.root"),
				}
				for s := range 8 {
					pgs = append(pgs, testPG("wl-jobset-sliced", fmt.Sprintf("s.psworkers-l2-s%d", s), "kubernetes.io/hostname", 1, fmt.Sprintf("s.psworkers-l1-s%d", s/2)))
				}
				return pgs
			}(),
			wantCompositePodGroups: func() []*schedulingv1alpha3.CompositePodGroup {
				cpgs := []*schedulingv1alpha3.CompositePodGroup{
					testCPG("wl-jobset-sliced", "r.root", "", 3, ""),
					testCPG("wl-jobset-sliced", "s.psworkers-l0-s0", "cloud.com/block", 2, "r.root"),
					testCPG("wl-jobset-sliced", "s.psworkers-l0-s1", "cloud.com/block", 2, "r.root"),
				}
				for s := range 4 {
					cpgs = append(cpgs, testCPG("wl-jobset-sliced", fmt.Sprintf("s.psworkers-l1-s%d", s), "cloud.com/rack", 2, fmt.Sprintf("s.psworkers-l0-s%d", s/2)))
				}
				return cpgs
			}(),
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				if got, want := res.LeafPGForPod("launcher", 0), testLeafName("wl-jobset-sliced", "p.launcher"); got != want {
					t.Errorf("expected leaf PG for launcher to be %q, got %q", want, got)
				}
				for i := range 8 {
					want := testLeafName("wl-jobset-sliced", fmt.Sprintf("s.psworkers-l2-s%d", i))
					if got := res.LeafPGForPod("workers", i); got != want {
						t.Errorf("expected leaf PG for worker %d to be %q, got %q", i, want, got)
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
		},
		"zero-count PodSet alongside non-zero PodSet": {
			workload: utiltesting.MakeWorkload("wl-zero-mixed", "default").
				PodSets(
					*utiltesting.MakePodSet("empty", 0).Obj(),
					*utiltesting.MakePodSet("main", 3).Obj(),
				).Obj(),
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-zero-mixed", "p.main", "", 3, ""),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				wantLeaf := testLeafName("wl-zero-mixed", "p.main")
				for i := range 3 {
					if got := res.LeafPGForPod("main", i); got != wantLeaf {
						t.Errorf("expected leaf PG for replica %d to be %q, got %q", i, wantLeaf, got)
					}
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
			wantPodGroups: []*schedulingv1beta1.PodGroup{
				testPG("wl-root-name", "s.psroot-l1-s0", "cloud.com/rack", 2, "s.psroot-l0-s0"),
				testPG("wl-root-name", "s.psroot-l1-s1", "cloud.com/rack", 2, "s.psroot-l0-s0"),
				testPG("wl-root-name", "p.other", "", 1, "r.root"),
			},
			wantCompositePodGroups: []*schedulingv1alpha3.CompositePodGroup{
				testCPG("wl-root-name", "r.root", "", 2, ""),
				testCPG("wl-root-name", "s.psroot-l0-s0", "cloud.com/block", 2, "r.root"),
			},
			checkFn: func(t *testing.T, res *WorkloadVirtualPodGroups) {
				if got, want := res.LeafPGForPod("other", 0), testLeafName("wl-root-name", "p.other"); got != want {
					t.Errorf("expected leaf PG for other to be %q, got %q", want, got)
				}
				if got, want := res.LeafPGForPod("root", 0), testLeafName("wl-root-name", "s.psroot-l1-s0"); got != want {
					t.Errorf("expected leaf PG for root 0 to be %q, got %q", want, got)
				}
				if got, want := res.LeafPGForPod("root", 1), testLeafName("wl-root-name", "s.psroot-l1-s0"); got != want {
					t.Errorf("expected leaf PG for root 1 to be %q, got %q", want, got)
				}
				if got, want := res.LeafPGForPod("root", 2), testLeafName("wl-root-name", "s.psroot-l1-s1"); got != want {
					t.Errorf("expected leaf PG for root 2 to be %q, got %q", want, got)
				}
				if got, want := res.LeafPGForPod("root", 3), testLeafName("wl-root-name", "s.psroot-l1-s1"); got != want {
					t.Errorf("expected leaf PG for root 3 to be %q, got %q", want, got)
				}
			},
		},
	}

	cmpOpts := cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.SortSlices(func(a, b *schedulingv1beta1.PodGroup) bool {
			return a.Name < b.Name
		}),
		cmpopts.SortSlices(func(a, b *schedulingv1alpha3.CompositePodGroup) bool {
			return a.Name < b.Name
		}),
		cmpopts.IgnoreFields(schedulingv1beta1.PodGroup{}, "TypeMeta", "Status"),
		cmpopts.IgnoreFields(schedulingv1alpha3.CompositePodGroup{}, "TypeMeta", "Status"),
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "CreationTimestamp"),
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

			if res == nil {
				if tc.wantPodGroups != nil || tc.wantCompositePodGroups != nil {
					t.Fatalf("expected non-nil result, got nil")
				}
				return
			}
			if diff := cmp.Diff(tc.wantPodGroups, res.PodGroups, cmpOpts); diff != "" {
				t.Errorf("unexpected PodGroups (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCompositePodGroups, res.CompositePodGroups, cmpOpts); diff != "" {
				t.Errorf("unexpected CompositePodGroups (-want,+got):\n%s", diff)
			}
			if tc.checkFn != nil {
				tc.checkFn(t, res)
			}
		})
	}
}
