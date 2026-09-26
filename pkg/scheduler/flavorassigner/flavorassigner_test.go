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

package flavorassigner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

var (
	statusComparer = cmp.Comparer(func(a, b Status) bool {
		if a.err != nil || b.err != nil {
			return errors.Is(a.err, b.err) || errors.Is(b.err, a.err)
		}
		return cmp.Equal(a.reasons, b.reasons, cmpopts.SortSlices(func(x, y string) bool {
			return x < y
		}))
	})
)

// flexAssertConsidered compares Considered Flavors
// with the actual assignment, but with the rule:
//
//   - if actual has ANY Fit for that podset -> we don't require that all wanted
//     flavors appear; it's OK if actual only returned the "winning" flavor;
//   - if actual has NO Fit -> we require that all wanted flavors appear.
func flexAssertConsidered(t *testing.T, want, got Assignment, opts ...cmp.Option) {
	wantByName := make(map[string]PodSetAssignment, len(want.PodSets))
	for _, ps := range want.PodSets {
		wantByName[string(ps.Name)] = ps
	}
	gotByName := make(map[string]PodSetAssignment, len(got.PodSets))
	for _, ps := range got.PodSets {
		gotByName[string(ps.Name)] = ps
	}

	for name, wantPS := range wantByName {
		gotPS, ok := gotByName[name]
		if !ok {
			t.Errorf("podset %q: missing in actual assignment", name)
			continue
		}

		if len(wantPS.FlavorAssignmentAttempts) == 0 {
			continue
		}

		assertPodSetConsideredFlexible(t, name, wantPS.FlavorAssignmentAttempts, gotPS.FlavorAssignmentAttempts, opts...)
	}
}

func assertPodSetConsideredFlexible(t *testing.T, podSetName string, want, got []FlavorAssignmentAttempt, opts ...cmp.Option) {
	wantByFlavor := make(map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt, len(want))
	for _, wa := range want {
		wantByFlavor[wa.Flavor] = wa
	}

	gotByFlavor := make(map[kueue.ResourceFlavorReference]FlavorAssignmentAttempt, len(got))
	hasFit := false
	for _, ga := range got {
		gotByFlavor[ga.Flavor] = ga
		if ga.Mode == Fit {
			hasFit = true
		}
	}

	if hasFit {
		for flavor, ga := range gotByFlavor {
			wa, ok := wantByFlavor[flavor]
			if !ok {
				t.Errorf("podset %q: unexpected flavor %q in FlavorAssignmentAttempts (fit case), got=%#v", podSetName, flavor, ga)
				continue
			}

			if ga.Mode == Fit && isDoesNotProvideOnly(wa) {
				continue
			}

			if diff := cmp.Diff(wa, ga, opts...); diff != "" {
				t.Errorf("podset %q: flavor %q mismatch (fit case) (-want +got):\n%s", podSetName, flavor, diff)
			}
		}

		return
	}

	for flavor, wa := range wantByFlavor {
		ga, ok := gotByFlavor[flavor]
		if !ok {
			t.Errorf("podset %q: expected flavor %q in FlavorAssignmentAttempts (no-fit case)", podSetName, flavor)
			continue
		}
		if diff := cmp.Diff(wa, ga, opts...); diff != "" {
			t.Errorf("podset %q: flavor %q mismatch (no-fit case) (-want +got):\n%s", podSetName, flavor, diff)
		}
	}
}

func isDoesNotProvideOnly(at FlavorAssignmentAttempt) bool {
	if at.Mode != NoFit {
		return false
	}
	if len(at.Reasons) == 0 {
		return false
	}
	for _, r := range at.Reasons {
		if !strings.Contains(r, "does not provide resource") {
			return false
		}
	}
	return true
}

type simulationResultForFlavor struct {
	preemptionPossiblity     preemptioncommon.PreemptionPossibility
	borrowingAfterSimulation int
}

type testOracle struct {
	simulationResult map[resources.FlavorResource]simulationResultForFlavor
}

func (f *testOracle) SimulatePreemption(
	ctx context.Context,
	cq *schdcache.ClusterQueueSnapshot,
	wl workload.Info,
	fr resources.FlavorResource,
	quantity resources.Amount,
) (preemptioncommon.PreemptionPossibility, int) {
	if f.simulationResult != nil {
		if result, ok := f.simulationResult[fr]; ok {
			return result.preemptionPossiblity, result.borrowingAfterSimulation
		}
	}
	return preemptioncommon.Preempt, 0
}

func TestAssignFlavors(t *testing.T) {
	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"default": utiltestingapi.MakeResourceFlavor("default").Obj(),
		"one":     utiltestingapi.MakeResourceFlavor("one").NodeLabel("type", "one").Obj(),
		"two":     utiltestingapi.MakeResourceFlavor("two").NodeLabel("type", "two").Obj(),
		"three":   utiltestingapi.MakeResourceFlavor("three").NodeLabel("type", "three").Obj(),
		"b_one":   utiltestingapi.MakeResourceFlavor("b_one").NodeLabel("b_type", "one").Obj(),
		"b_two":   utiltestingapi.MakeResourceFlavor("b_two").NodeLabel("b_type", "two").Obj(),
		"tainted": utiltestingapi.MakeResourceFlavor("tainted").
			Taint(corev1.Taint{
				Key:    "instance",
				Value:  "spot",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj(),
		"taint_and_toleration": utiltestingapi.MakeResourceFlavor("taint_and_toleration").
			Taint(corev1.Taint{
				Key:    "instance",
				Value:  "spot",
				Effect: corev1.TaintEffectNoSchedule,
			}).
			Toleration(corev1.Toleration{
				Key:      "instance",
				Operator: corev1.TolerationOpEqual,
				Value:    "spot",
				Effect:   corev1.TaintEffectNoSchedule,
			}).
			Obj(),
		"label-x-a":  utiltestingapi.MakeResourceFlavor("label-x-a").NodeLabel("x", "a").Obj(),
		"label-xy-b": utiltestingapi.MakeResourceFlavor("label-xy-b").NodeLabel("x", "b").NodeLabel("y", "k").Obj(),
		"tas-a":      utiltestingapi.MakeResourceFlavor("tas-a").TopologyName("tas-topo-a").Obj(),
		"tas-b":      utiltestingapi.MakeResourceFlavor("tas-b").TopologyName("tas-topo-b").Obj(),
	}

	cases := map[string]struct {
		wlPods                     []kueue.PodSet
		wlReclaimablePods          []kueue.ReclaimablePod
		counts                     []int32
		clusterQueue               kueue.ClusterQueue
		clusterQueueUsage          resources.FlavorResourceQuantities
		secondaryClusterQueue      *kueue.ClusterQueue
		secondaryClusterQueueUsage resources.FlavorResourceQuantities
		wantRepMode                FlavorAssignmentMode
		wantAssignment             Assignment
		enableFairSharing          bool
		simulationResult           map[resources.FlavorResource]simulationResultForFlavor
		preemptWorkloadSlice       *workload.Info
		featureGates               map[featuregate.Feature]bool
		infoOptions                []workload.InfoOption
		flavorScanState            *workload.FlavorScanState
		topologies                 []*kueue.Topology
	}{
		"single flavor, fits": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "2Mi").
						Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count:                    1,
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{{Flavor: "default", Mode: Fit}},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Mi),
				}}},
			},
		},
		"single flavor, fits tainted flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "spot",
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tainted").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tainted", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "tainted", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "tainted", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
		},
		"single flavor, fits tainted flavor with toleration": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("taint_and_toleration").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "taint_and_toleration", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Count: 1,
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "taint_and_toleration", Mode: Fit},
					},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "taint_and_toleration", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
		},
		"single flavor, used resources, doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "default", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor default, 1 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "default",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor default, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"multiple resource groups, fits": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("b_one").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("b_two").
						Resource(corev1.ResourceMemory, "5Gi").
						Obj(),
				).
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "b_one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)"},
							NoFitReason: "ExceedsMaxQuota",
						},
						{Flavor: "two", Mode: Fit},
						{Flavor: "b_one", Mode: Fit},
						{
							Flavor:      "b_two",
							Mode:        NoFit,
							Reasons:     []string{"flavor b_two does not provide resource memory"},
							NoFitReason: "NoMatchingFlavor",
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:      resources.NewAmount(3_000),
					{Flavor: "b_one", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
				}}},
			},
		},
		"multiple flavors, leader worker set, leader and workers request the same resources fits": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("worker", 4).
					Request(corev1.ResourceCPU, "2").
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "1").
					PodSetGroup("group1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "9").
						Obj(),
				).
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("8"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (9) > maximum capacity (4)"},
								NoFitReason: "ExceedsMaxQuota",
							},
							{Flavor: "two", Mode: Fit},
						},
						Count: 4,
					},
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (9) > maximum capacity (4)"},
								NoFitReason: "ExceedsMaxQuota",
							},
							{Flavor: "two", Mode: Fit},
						},
						Count: 1,
					}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(9_000),
				}}},
			},
		},
		"multiple flavors, leader worker set, workers request GPU, leader does not request GPU, fits": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("worker", 4).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					Request("example.com/gpu", "1").
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1").
					PodSetGroup("group1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "10").
						Resource(corev1.ResourceMemory, "10").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "5").
						Resource("example.com/gpu", "4").
						Obj(),
				).
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
							"example.com/gpu":     {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("4"),
							"example.com/gpu":     resource.MustParse("4"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:  "one",
								Mode:    NoFit,
								Reasons: []string{"flavor one does not provide resource example.com/gpu"},
							},
							{Flavor: "two", Mode: Fit},
						},
						Count: 4,
					},
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:  "one",
								Mode:    NoFit,
								Reasons: []string{"flavor one does not provide resource example.com/gpu"},
							},
							{Flavor: "two", Mode: Fit},
						},
						Count: 1,
					}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(5_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(5),
					{Flavor: "two", Resource: "example.com/gpu"}:     resources.NewAmount(4),
				}}},
			},
		},
		"multiple flavors, leader worker set, workers request GPU, leader does not request GPU, does not fit, without group it would fit": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("worker", 4).
					Request(corev1.ResourceCPU, "1").
					Request("example.com/gpu", "1").
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "1").
					PodSetGroup("group1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Resource("example.com/gpu", "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "5").
						Resource("example.com/gpu", "0").
						Obj(),
				).
				Obj(),
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "worker",
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
							"example.com/gpu":  resource.MustParse("4"),
						},
						Status: *NewStatus(
							"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (5) > maximum capacity (4)",
							"insufficient quota for example.com/gpu in flavor two, previously considered podsets requests (0) + current podset request (4) > maximum capacity (0)",
						),
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (5) > maximum capacity (4)"},
								NoFitReason: "ExceedsMaxQuota",
							},
							{
								Flavor: "two",
								Mode:   NoFit,
								Reasons: []string{
									"insufficient quota for example.com/gpu in flavor two, previously considered podsets requests (0) + current podset request (4) > maximum capacity (0)",
								},
								NoFitReason: "ExceedsMaxQuota",
							},
						},
						Count: 4,
					},
					{
						Name: "leader",
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
						Status: *NewStatus(
							"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (5) > maximum capacity (4)",
							"insufficient quota for example.com/gpu in flavor two, previously considered podsets requests (0) + current podset request (4) > maximum capacity (0)",
						),
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (5) > maximum capacity (4)"},
								NoFitReason: "ExceedsMaxQuota",
							},
							{
								Flavor: "two",
								Mode:   NoFit,
								Reasons: []string{
									"insufficient quota for example.com/gpu in flavor two, previously considered podsets requests (0) + current podset request (4) > maximum capacity (0)",
								},
								NoFitReason: "ExceedsMaxQuota",
							},
						},
						Count: 1,
					}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"multiple resource groups, one could fit with preemption, other doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "3").
						Obj(),
				).ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("b_one").
					Resource(corev1.ResourceMemory, "1Mi").
					Obj(),
			).Obj(),

			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
			},

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Status: *NewStatus(
						"insufficient quota for memory in flavor b_one, previously considered podsets requests (0) + current podset request (10Mi) > maximum capacity (1Mi)",
					),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "b_one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for memory in flavor b_one, previously considered podsets requests (0) + current podset request (10Mi) > maximum capacity (1Mi)"},
							NoFitReason: "ExceedsMaxQuota",
						},
					},
					Count: 1,
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"multiple resource groups with multiple resources, fits": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Request("example.com/gpu", "3").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "15Mi").
						Obj(),
				).ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("b_one").
					Resource("example.com/gpu", "4").
					Obj(),
				*utiltestingapi.MakeFlavorQuotas("b_two").
					Resource("example.com/gpu", "2").
					Obj(),
			).Obj(),

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						"example.com/gpu":     {Name: "b_one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
						"example.com/gpu":     resource.MustParse("3"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "b_one", Mode: Fit},
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)"},
							NoFitReason: "ExceedsMaxQuota",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(3_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
					{Flavor: "b_one", Resource: "example.com/gpu"}:   resources.NewAmount(3),
				}}},
			},
		},
		"multiple resource groups with multiple resources, fits with different modes": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Request("example.com/gpu", "3").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "15Mi").
						Obj(),
				).ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("b_one").
					Resource("example.com/gpu", "4").
					Obj(),
			).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("b_one").
						Resource("example.com/gpu", "0").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "b_one", Resource: "example.com/gpu"}: resources.NewAmount(2),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "two", Resource: corev1.ResourceMemory}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Preempt, TriedFlavorIdx: -1},
						"example.com/gpu":     {Name: "b_one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
						"example.com/gpu":     resource.MustParse("3"),
					},
					Status: *NewStatus(
						"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)",
						"insufficient unused quota for memory in flavor two, 5Mi more needed",
						"insufficient unused quota for example.com/gpu in flavor b_one, 1 more needed",
					),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "b_one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for example.com/gpu in flavor b_one, 1 more needed"},
						},
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)"},
							NoFitReason: "ExceedsMaxQuota",
						},
						{
							Flavor:                "two",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for memory in flavor two, 5Mi more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(3_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
					{Flavor: "b_one", Resource: "example.com/gpu"}:   resources.NewAmount(3),
				}}},
			},
		},
		"multiple resources in a group, doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "5Mi").
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Status: *NewStatus(
						"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)",
						"insufficient quota for memory in flavor two, previously considered podsets requests (0) + current podset request (10Mi) > maximum capacity (5Mi)",
					),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)"},
							NoFitReason: "ExceedsMaxQuota",
						},
						{
							Flavor:      "two",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for memory in flavor two, previously considered podsets requests (0) + current podset request (10Mi) > maximum capacity (5Mi)"},
							NoFitReason: "ExceedsMaxQuota",
						},
					},
					Count: 1,
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"multiple flavors, fits while skipping tainted flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tainted").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "tainted",
							Mode:        NoFit,
							Reasons:     []string{"untolerated taint {instance spot NoSchedule <nil>} in flavor tainted"},
							NoFitReason: "NoMatchingFlavor",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				}}},
			},
		},
		"multiple flavors, fits a node selector": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					// ignored:foo should get ignored
					NodeSelector(map[string]string{"type": "two", "ignored1": "foo"}).
					RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									// this expression should get ignored
									Key:      "ignored2",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"bar"},
								},
							},
						},
					}).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						})...,
					).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"flavor one doesn't match node affinity"},
							NoFitReason: "NoMatchingFlavor",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
		},
		"multiple flavors, fits with node affinity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					NodeSelector(map[string]string{"ignored1": "foo"}).
					RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "type",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"two"},
								},
							},
						},
					}).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU:    "1",
							corev1.ResourceMemory: "1Mi",
						})...,
					).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).Obj(),

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Mi"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"flavor one doesn't match node affinity"},
							NoFitReason: "NoMatchingFlavor",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Mi),
				}}},
			},
		},
		"multiple flavors, node affinity fits any flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "ignored2",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"bar"},
								},
							},
						},
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									// although this terms selects two
									// the first term practically matches
									// any flavor; and since the terms
									// are ORed, any flavor can be selected.
									Key:      "cpuType",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"two"},
								},
							},
						},
					}).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						})...,
					).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
		},
		"multiple flavors with different label keys, selector only uses flavor's own keys": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					NodeSelector(map[string]string{"x": "a", "y": "g"}).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						})...,
					).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("label-x-a").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("label-xy-b").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "label-x-a", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "label-x-a", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
		},
		"labelless flavor in group with labeled flavor, workload uses labeled selector": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					NodeSelector(map[string]string{"type": "two"}).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						})...,
					).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "default", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
		},
		"multiple flavors, doesn't fit node affinity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
						{
							MatchExpressions: []corev1.NodeSelectorRequirement{
								{
									Key:      "type",
									Operator: corev1.NodeSelectorOpIn,
									Values:   []string{"three"},
								},
							},
						},
					}).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						})...,
					).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Status: *NewStatus(
						"flavor one doesn't match node affinity",
						"flavor two doesn't match node affinity",
					),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"flavor one doesn't match node affinity"},
							NoFitReason: "NoMatchingFlavor",
						},
						{
							Flavor:      "two",
							Mode:        NoFit,
							Reasons:     []string{"flavor two doesn't match node affinity"},
							NoFitReason: "NoMatchingFlavor",
						},
					},
					Count: 1,
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "NoMatchingFlavor",
			},
		},
		"multiple specs, fit different flavors": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).
					Request(corev1.ResourceCPU, "5").
					Obj(),
				*utiltestingapi.MakePodSet("worker", 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (5) > maximum capacity (4)"},
								NoFitReason: "ExceedsMaxQuota",
							},
							{Flavor: "two", Mode: Fit},
						},
						Count: 1,
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{Flavor: "one", Mode: Fit},
						},
						Count: 1,
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
				}}},
			},
		},
		"multiple specs, fits borrowing": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("driver", 1).
					Request(corev1.ResourceCPU, "4").
					Request(corev1.ResourceMemory, "1Gi").
					Obj(),
				*utiltestingapi.MakePodSet("worker", 1).
					Request(corev1.ResourceCPU, "6").
					Request(corev1.ResourceMemory, "4Gi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").BorrowingLimit("98").Append().
						Resource(corev1.ResourceMemory, "2Gi").
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "198").
						Resource(corev1.ResourceMemory, "198Gi").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{Flavor: "default", Mode: Fit, Borrow: 1},
						},
						Count: 1,
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("6"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{Flavor: "default", Mode: Fit, Borrow: 1},
						},
						Count: 1,
					},
				},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(10_000),
					{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(5 * utiltesting.Gi),
				}}},
			},
		},
		"not enough space to borrow": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("0").Append().
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(9_000),
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus("insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (2) > maximum capacity (1)"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (2) > maximum capacity (1)"},
							NoFitReason: "ExceedsMaxQuota",
						},
					},
					Count: 1,
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"past max, but can preempt in ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").BorrowingLimit("8").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(9_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "98").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(9_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 1 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"past min, but can preempt in ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 1 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"past min, but can preempt in cohort and ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "3").
						Obj(),
				).Cohort("test-cohort").Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "7").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(8_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 2 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"can only preempt flavors that match affinity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Containers(
						utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{corev1.ResourceCPU: "2"})...,
					).NodeSelector(map[string]string{"type": "two"}).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus(
						"flavor one doesn't match node affinity",
						"insufficient unused quota for cpu in flavor two, 1 more needed",
					),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"flavor one doesn't match node affinity"},
							NoFitReason: "NoMatchingFlavor",
						},
						{
							Flavor:                "two",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor two, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"each podset requires preemption on a different flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("launcher", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
				*utiltestingapi.MakePodSet("workers", 10).
					Request(corev1.ResourceCPU, "1").
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "spot",
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("tainted").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}:     resources.NewAmount(3_000),
				{Flavor: "tainted", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "launcher",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Status: *NewStatus(
							"insufficient unused quota for cpu in flavor one, 1 more needed",
							"untolerated taint {instance spot NoSchedule <nil>} in flavor tainted",
						),
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:                "one",
								Mode:                  Preempt,
								PreemptionPossibility: new(preemptioncommon.Preempt),
								Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
							},
							{
								Flavor:      "tainted",
								Mode:        NoFit,
								Reasons:     []string{"untolerated taint {instance spot NoSchedule <nil>} in flavor tainted"},
								NoFitReason: "NoMatchingFlavor",
							},
						},
						Count: 1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "tainted", Mode: Preempt, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
						Status: *NewStatus(
							"insufficient quota for cpu in flavor one, previously considered podsets requests (2) + current podset request (10) > maximum capacity (4)",
							"insufficient unused quota for cpu in flavor tainted, 3 more needed",
						),
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (2) + current podset request (10) > maximum capacity (4)"},
								NoFitReason: "ExceedsMaxQuota",
							},
							{
								Flavor:                "tainted",
								Mode:                  Preempt,
								PreemptionPossibility: new(preemptioncommon.Preempt),
								Reasons:               []string{"insufficient unused quota for cpu in flavor tainted, 3 more needed"},
							},
						},
						Count: 10,
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:     resources.NewAmount(2_000),
					{Flavor: "tainted", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
				}}},
			},
		},
		"resource not listed in clusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request("example.com/gpu", "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						"example.com/gpu": resource.MustParse("2"),
					},
					Status: *NewStatus("resource example.com/gpu unavailable in ClusterQueue"),
					Count:  1,
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "NoMatchingFlavor",
			},
		},
		"zero resource request not in clusterQueue should succeed": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Request("example.com/gpu", "0").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
						"example.com/gpu":  resource.MustParse("0"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "default", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}}},
			},
			wantRepMode: Fit,
		},
		"zero resource request defined in clusterQueue should get flavor assigned": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Request("example.com/gpu", "0").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						Resource("example.com/gpu", "4").
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						"example.com/gpu":  &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
						"example.com/gpu":  resource.MustParse("0"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "default", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
					{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
			wantRepMode: Fit,
		},
		"zero-count PodSet prefers a flavor with capacity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).
					Request("example.com/gpu", "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("example.com/gpu", "4").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		"zero-count workers probe independently of a running head": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("head", 1).Request(corev1.ResourceCPU, "2").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).
					Request(corev1.ResourceCPU, "4").Request("example.com/gpu", "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "4").Resource("example.com/gpu", "1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "8").Resource("example.com/gpu", "8").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "head",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
							"example.com/gpu":  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("0"),
							"example.com/gpu":  resource.MustParse("0"),
						},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(0),
					{Flavor: "two", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
		},
		"zero-count replacement probe includes earlier PodSet requests instead of quota deltas": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("head", 1).Request(corev1.ResourceCPU, "2").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).Request(corev1.ResourceCPU, "4").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "4").Obj()).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			preemptWorkloadSlice: &workload.Info{
				TotalRequests: []workload.PodSetResources{
					{
						Name:     "head",
						Count:    2,
						Requests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 4_000}),
						Flavors:  map[corev1.ResourceName]kueue.ResourceFlavorReference{corev1.ResourceCPU: "one"},
					},
					{
						Name:     "workers",
						Requests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{corev1.ResourceCPU: 0}),
						Flavors:  map[corev1.ResourceName]kueue.ResourceFlavorReference{corev1.ResourceCPU: "one"},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				ZeroCountFlavorFallback: "Assigned flavor one to zero-count PodSets [workers] for resources [cpu] in ClusterQueue test-clusterqueue. " +
					"No considered flavor could satisfy one pod per PodSet: " +
					"insufficient quota for cpu in flavor one, previously considered podsets requests (2) + current podset request (4) > maximum capacity (4). " +
					"Review capacity and flavor constraints before scaling up.",
				PodSets: []PodSetAssignment{
					{
						Name: "head",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(-2_000),
				}}},
			},
		},
		"all-zero PodSet group probes one pod and its resources per member": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 0).Request(corev1.ResourceCPU, "1").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).Request(corev1.ResourceCPU, "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1").Resource(corev1.ResourcePods, "2").Obj(),
				*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "2").Resource(corev1.ResourcePods, "1").Obj(),
				*utiltestingapi.MakeFlavorQuotas("three").Resource(corev1.ResourceCPU, "2").Resource(corev1.ResourcePods, "2").Obj(),
			).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:  {Name: "three", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourcePods: {Name: "three", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0"), corev1.ResourcePods: resource.MustParse("0")},
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:  {Name: "three", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourcePods: {Name: "three", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0"), corev1.ResourcePods: resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "three", Resource: corev1.ResourceCPU}:  resources.NewAmount(0),
					{Flavor: "three", Resource: corev1.ResourcePods}: resources.NewAmount(0),
				}}},
			},
		},
		"reclaimed workers do not make a mixed-count group wait for a busy flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 1).
					Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "8").Obj(),
				).Obj(),
			wlReclaimablePods: []kueue.ReclaimablePod{{Name: "workers", Count: 1}},
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(100_000),
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							"example.com/gpu": {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
					{Flavor: "one", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
		},
		"mixed-count PodSet group uses actual requests": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).
					Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "8").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							"example.com/gpu": {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
					{Flavor: "one", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
		},
		"positive-count PodSet group checks increased worker requests": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 1).
					Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "8").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							"example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("1")},
						Count:    1,
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
					{Flavor: "two", Resource: "example.com/gpu"}:  resources.NewAmount(1),
				}}},
			},
		},
		"mixed-count PodSet group includes all positive-count replicas": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 2).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).
					Request(corev1.ResourceCPU, "4").Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "10").Resource("example.com/gpu", "8").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "20").Resource("example.com/gpu", "8").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")},
						Count:    2,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
							"example.com/gpu":  {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0"), "example.com/gpu": resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(8_000),
					{Flavor: "one", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
		},
		"mixed-count PodSet group charges only actual pod slots": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 2).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).
					Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "8").Resource(corev1.ResourcePods, "2").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "8").Resource(corev1.ResourcePods, "3").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
							corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("2"), corev1.ResourceCPU: resource.MustParse("8")},
						Count:    2,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
							"example.com/gpu":   {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("0"), "example.com/gpu": resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(2),
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(8_000),
					{Flavor: "one", Resource: "example.com/gpu"}:   resources.NewAmount(0),
				}}},
			},
		},
		"mixed-count PodSet group ignores capacity for zero-count workers": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).
					Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "100").Resource("example.com/gpu", "0").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							"example.com/gpu": {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
					{Flavor: "one", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
		},
		"mixed-count PodSet group skips a flavor too small for the leader": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					Request(corev1.ResourceCPU, "4").PodSetGroup("ranks").Obj(),
				*utiltestingapi.MakePodSet("workers", 0).
					Request("example.com/gpu", "1").PodSetGroup("ranks").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "4").Resource("example.com/gpu", "0").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "leader",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						Count:    1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							"example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
					},
				},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
					{Flavor: "two", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}}},
			},
		},
		"zero-count PodSet probes transformed resource requests": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).
					Request("example.com/gpu", "2").Obj(),
			},
			infoOptions: []workload.InfoOption{workload.WithResourceTransformations([]configapi.ResourceTransformation{{
				Input:    "example.com/gpu",
				Strategy: new(configapi.Replace),
				Outputs:  corev1.ResourceList{"quota.example.com/gpu": resource.MustParse("3")},
			}})},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("quota.example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("quota.example.com/gpu", "6").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"quota.example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{"quota.example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "quota.example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		// A third flavor keeps the recorded index off the end of the list, so the
		// assertion distinguishes the index of the flavor the probe settled on from
		// the index of the last flavor the scan looked at.
		"zero-count PodSet retains its probe with explicit counts": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).
					Request("example.com/gpu", "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("example.com/gpu", "4").Obj(),
					*utiltestingapi.MakeFlavorQuotas("three").Resource("example.com/gpu", "4").Obj(),
				).Obj(),
			counts:      []int32{0},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: 1},
					},
					Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		// The second pass of the case above: resuming after the flavor the probe
		// settled on must land on "three" and then wrap to -1, so the flavor the probe
		// skipped is reachable again rather than excluded for good.
		"zero-count PodSet resumes the flavor scan after a probe skip": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).
					Request("example.com/gpu", "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("example.com/gpu", "4").Obj(),
					*utiltestingapi.MakeFlavorQuotas("three").Resource("example.com/gpu", "4").Obj(),
				).Obj(),
			counts: []int32{0},
			flavorScanState: &workload.FlavorScanState{
				LastTriedFlavorIndexes: []map[corev1.ResourceName]int{{"example.com/gpu": 1}},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"example.com/gpu": {Name: "three", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "three", Resource: "example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		"zero-count PodSet prefers a flavor with pod capacity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourcePods, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourcePods, "4").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{corev1.ResourcePods: resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourcePods}: resources.NewAmount(0),
				}}},
			},
		},
		"zero-count PodSet uses potential capacity even when quota is exhausted": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).
					Request("example.com/gpu", "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("example.com/gpu", "4").Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "two", Resource: "example.com/gpu"}: resources.NewAmount(4),
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		"fully reclaimed PodSet prefers a flavor with capacity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request("example.com/gpu", "1").Obj(),
			},
			wlReclaimablePods: []kueue.ReclaimablePod{{Name: kueue.DefaultPodSetName, Count: 1}},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("example.com/gpu", "4").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"example.com/gpu": {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		"zero-count PodSet falls back when no flavor has capacity": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 0).
					Request("example.com/gpu", "1").Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource("example.com/gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource("example.com/gpu", "0").Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				ZeroCountFlavorFallback: "Assigned flavor one to zero-count PodSets [main] for resources [example.com/gpu] in ClusterQueue test-clusterqueue. " +
					"No considered flavor could satisfy one pod per PodSet: " +
					"insufficient quota for example.com/gpu in flavor one, previously considered podsets requests (0) + current podset request (1) > maximum capacity (0), " +
					"insufficient quota for example.com/gpu in flavor two, previously considered podsets requests (0) + current podset request (1) > maximum capacity (0). " +
					"Review capacity and flavor constraints before scaling up.",
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						"example.com/gpu": {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{"example.com/gpu": resource.MustParse("0")},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "example.com/gpu"}: resources.NewAmount(0),
				}}},
			},
		},
		"num pods fit": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "3").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "default", Mode: Fit},
					},
					Count: 3,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(3),
					{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(3_000),
				}}},
			},
			wantRepMode: Fit,
		},
		"num pods don't fit": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "2").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Status: *NewStatus("insufficient quota for pods in flavor default, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "default",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for pods in flavor default, previously considered podsets requests (0) + current podset request (3) > maximum capacity (2)"},
							NoFitReason: "ExceedsMaxQuota",
						},
					},
					Count: 3,
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"with reclaimable pods; reclaimablePods on": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wlReclaimablePods: []kueue.ReclaimablePod{
				{
					Name:  kueue.DefaultPodSetName,
					Count: 2,
				},
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "3").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "default", Mode: Fit},
					},
					Count: 3,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(3),
					{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(3_000),
				}}},
			},
			wantRepMode: Fit,
		},
		"with reclaimable pods; reclaimablePods off": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wlReclaimablePods: []kueue.ReclaimablePod{
				{
					Name:  kueue.DefaultPodSetName,
					Count: 2,
				},
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "5").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("5"),
					},
					Count: 5,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(5),
					{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(5_000),
				}}},
			},
			wantRepMode: Fit,
			featureGates: map[featuregate.Feature]bool{
				features.ReclaimablePods: false,
			}},
		"preempt before try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 1 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: "pods"}: resources.NewAmount(1),
				}}},
			},
		},
		"preempt before try next flavor; using WhenCanBorrow=MayStopSearch,WhenCanPreempt=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 1 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: "pods"}: resources.NewAmount(1),
				}}},
			},
		},
		"preempt try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "two", Resource: "pods"}: resources.NewAmount(1),
				}}},
			},
		},
		"borrow try next flavor, found the first flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor, WhenCanPreempt: kueue.TryNextFlavor}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").BorrowingLimit("1").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},
						{
							Flavor:      "two",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor two, previously considered podsets requests (0) + current podset request (9) > maximum capacity (1)"},
							NoFitReason: "ExceedsMaxQuota",
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}}},
			},
		},
		"borrow try next flavor, found the second flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor, WhenCanPreempt: kueue.TryNextFlavor}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").BorrowingLimit("1").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},

			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "two", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}}},
			},
		},
		"borrow before try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").BorrowingLimit("1").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: "pods"}: resources.NewAmount(1),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 10 more needed"),
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=MayStopSearch,WhenCanPreempt=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 10 more needed"),
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one, no borrowingLimit; WhenCanBorrow=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 10 more needed"),
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one, no borrowingLimit; WhenCanBorrow=MayStopSearch,WhenCanPreempt=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 10 more needed"),
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=TryNextFlavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=TryNextFlavor,WhenCanPreempt=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed, but borrowingLimit exceeds the quota available in the cohort": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "11").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				PodSets: []PodSetAssignment{
					{
						Name:   kueue.DefaultPodSetName,
						Status: *NewStatus("insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (12) > maximum capacity (11)"),
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("12"),
						},
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (12) > maximum capacity (11)"},
								NoFitReason: "ExceedsMaxQuota",
							},
						},
						Count: 1,
					},
				},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"lend try next flavor, found the second flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor},
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("1").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("0").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "two", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}}},
			},
		},
		"lend try next flavor, found the first flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor, WhenCanPreempt: kueue.TryNextFlavor}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("1").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").LendingLimit("0").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "one", Mode: Fit, Borrow: 1},

						{
							Flavor:      "two",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor two, previously considered podsets requests (0) + current podset request (9) > maximum capacity (1)"},
							NoFitReason: "ExceedsMaxQuota",
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}}},
			},
		},
		"cannot preempt in cohort (oracle returns None) for the first flavor, tries the second flavor (which fits)": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.NoCandidates),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
						},
						{Flavor: "two", Mode: Fit, Borrow: 1},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"cannot preempt in cohort (oracle returns None) for the first flavor, tries the second flavor (which fits); using deprecated WhenCanBorrow=MayStopSearch,WhenCanPreempt=MayStopSearch": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.NoCandidates),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
						},
						{Flavor: "two", Mode: Fit, Borrow: 1},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"preemption requiring borrowing is not attempted when the ClusterQueue doesn't allow it": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				PodSets: []PodSetAssignment{
					{
						Name:   kueue.DefaultPodSetName,
						Status: *NewStatus("insufficient unused quota for cpu in flavor one, 2 more needed"),
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: 1,
					},
				},
				NoFitReason: "WaitingForQuota",
			},
		},
		"ClusterQueue referencing a PreemptionConfig and borrowWithinCohort Never may preempt while borrowing": {
			featureGates: map[featuregate.Feature]bool{
				features.ConfigurablePreemptions: true,
			},
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, "test-preemption-config").
				Preemption(kueue.ClusterQueuePreemption{
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyNever,
					},
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			// The int is the height of the lowest cohort subtree that fits the request
			// after preemption, as returned by FindHeightOfLowestSubtreeThatFits: 0
			// means the ClusterQueue fits it on its own nominal quota, anything greater
			// means it only fits by borrowing from an ancestor. Here it is 1 because
			// the ClusterQueue has no nominal quota of its own, so the 2 CPU can only
			// come from test-cohort. That is what makes this case exercise the
			// borrowing relaxation granted by the PreemptionConfig.
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Reclaim, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 2 more needed"),
					Count:  1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}}},
			},
		},
		"ClusterQueue referencing a PreemptionConfig and borrowWithinCohort Never may not preempt while borrowing when ConfigurablePreemptions is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.ConfigurablePreemptions: false,
			},
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, "test-preemption-config").
				Preemption(kueue.ClusterQueuePreemption{
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyNever,
					},
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("2").Append().
						Obj(),
				).Cohort("test-cohort").
				Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").Append().
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				PodSets: []PodSetAssignment{
					{
						Name:   kueue.DefaultPodSetName,
						Status: *NewStatus("insufficient unused quota for cpu in flavor one, 2 more needed"),
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: 1,
					},
				},
				NoFitReason: "WaitingForQuota",
			},
		},
		"quota exhausted, but can preempt in cohort and ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("0").Append().
						Obj(),
				).Cohort("test-cohort").Obj(),
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "0").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 1 more needed"),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one, fair sharing enabled, reclaimWithinCohort=Any": {
			enableFairSharing: true,
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 10 more needed"),
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one, fair sharing enabled, reclaimWithinCohort=Any; using deprecated WhenCanBorrow=MayStopSearch,WhenCanPreempt=MayStopSearch": {
			enableFairSharing: true,
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 1},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: 1,
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: *NewStatus("insufficient unused quota for cpu in flavor one, 10 more needed"),
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:                "one",
							Mode:                  Preempt,
							PreemptionPossibility: new(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one, fair sharing enabled, reclaimWithinCohort=Never": {
			enableFairSharing: true,
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyNever}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Borrow:      1,
							Reasons:     []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
							NoFitReason: "WaitingForQuota",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"when borrowing while preemption is needed for flavor one, fair sharing enabled, reclaimWithinCohort=Never; using deprecated WhenCanBorrow=MayStopSearch,WhenCanPreempt=MayStopSearch": {
			enableFairSharing: true,
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyNever}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.MayStopSearch, WhenCanPreempt: kueue.MayStopSearch}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).Cohort("test-cohort").Obj(),
			secondaryClusterQueue: utiltestingapi.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Borrow:      1,
							Reasons:     []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
							NoFitReason: "WaitingForQuota",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}}},
			},
		},
		"workload slice preemption fits in the original workload resource flavor": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "3").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "2Gi").
						Obj(),
				).
				Obj(),
			preemptWorkloadSlice: &workload.Info{
				TotalRequests: []workload.PodSetResources{
					{
						Name: "main",
						Requests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    2000,
							corev1.ResourceMemory: 10 * utiltesting.Mi,
						}),
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "two",
							corev1.ResourceMemory: "two",
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor: "one",
							Mode:   NoFit,
							Reasons: []string{
								"could not assign one flavor since the original workload is assigned: two",
							},
							NoFitReason: "NoMatchingFlavor",
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(0),
				}}},
			},
		},
		"workload slice preemption does not fit in the original workload resource flavor": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "500m"). // <-- does not fit after scale-up.
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "2Gi").
						Obj(),
				).
				Obj(),
			preemptWorkloadSlice: &workload.Info{
				TotalRequests: []workload.PodSetResources{
					{
						Name: "main",
						Requests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
							corev1.ResourceCPU:    2000,
							corev1.ResourceMemory: 10 * utiltesting.Mi,
						}),
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU:    "one",
							corev1.ResourceMemory: "one",
						},
					},
				},
			},
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Count: 1,
					Status: *NewStatus(
						"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (1) > maximum capacity (500m)",
						"could not assign two flavor since the original workload is assigned: one",
						"could not assign two flavor since the original workload is assigned: one",
					),
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "one",
							Mode:        NoFit,
							Reasons:     []string{"insufficient quota for cpu in flavor one, previously considered podsets requests (0) + current podset request (1) > maximum capacity (500m)"},
							NoFitReason: "ExceedsMaxQuota",
						},
						{
							Flavor:      "two",
							Mode:        NoFit,
							Reasons:     []string{"could not assign two flavor since the original workload is assigned: one"},
							NoFitReason: "NoMatchingFlavor",
						},
					},
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"multi-podset, one fits and another fails, fitting podset attempts skipped in resolveNoFitReason": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("fitting-podset", 1).
					Request(corev1.ResourceCPU, "1").
					NodeSelector(map[string]string{"type": "one"}).
					Obj(),
				*utiltestingapi.MakePodSet("blocking-podset", 1).
					Request(corev1.ResourceCPU, "5").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "2").
						Obj(),
				).Obj(),
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				NoFitReason: "ExceedsMaxQuota",
				PodSets: []PodSetAssignment{
					{
						Name: "fitting-podset",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
						Count: 1,
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor: "one",
								Mode:   Fit,
							},
							{
								Flavor:      "two",
								Mode:        NoFit,
								NoFitReason: "NoMatchingFlavor",
							},
						},
					},
					{
						Name: "blocking-podset",
						Status: Status{
							reasons: []string{
								"insufficient quota for cpu in flavor one, previously considered podsets requests (1) + current podset request (5) > maximum capacity (2)",
								"insufficient quota for cpu in flavor two, previously considered podsets requests (0) + current podset request (5) > maximum capacity (2)",
							},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
						Count: 1,
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
							{
								Flavor:      "one",
								Mode:        NoFit,
								Borrow:      0,
								NoFitReason: "ExceedsMaxQuota",
								Reasons: []string{
									"insufficient quota for cpu in flavor one, previously considered podsets requests (1) + current podset request (5) > maximum capacity (2)",
								},
							},
							{
								Flavor:      "two",
								Mode:        NoFit,
								Borrow:      0,
								NoFitReason: "ExceedsMaxQuota",
								Reasons: []string{
									"insufficient quota for cpu in flavor two, previously considered podsets requests (0) + current podset request (5) > maximum capacity (2)",
								},
							},
						},
					},
				},
				Usage: workload.Usage{
					Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
						{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(1000),
					}},
				},
			},
		},
	}
	for name, tc := range cases {
		for _, unadmittedWorkloadsObservabilityEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gate_%t", name, unadmittedWorkloadsObservabilityEnabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, unadmittedWorkloadsObservabilityEnabled)
				ctx, log := utiltesting.ContextWithLog(t)
				for fg, val := range tc.featureGates {
					features.SetFeatureGateDuringTest(t, fg, val)
				}
				wlInfo := workload.NewInfo(log, &kueue.Workload{
					Spec: kueue.WorkloadSpec{
						PodSets: tc.wlPods,
					},
					Status: kueue.WorkloadStatus{
						ReclaimablePods: tc.wlReclaimablePods,
					},
				}, tc.infoOptions...)
				wlInfo.FlavorScanState = tc.flavorScanState

				cache := schdcache.New(utiltesting.NewFakeClient())
				if err := cache.AddClusterQueue(ctx, &tc.clusterQueue); err != nil {
					t.Fatalf("Failed to add CQ to cache")
				}
				if tc.secondaryClusterQueue != nil {
					if err := cache.AddClusterQueue(ctx, tc.secondaryClusterQueue); err != nil {
						t.Fatalf("Failed to add secondary CQ to cache")
					}
				}
				for _, rf := range resourceFlavors {
					cache.AddOrUpdateResourceFlavor(log, rf)
				}
				if tc.topologies != nil {
					for _, topology := range tc.topologies {
						cache.AddOrUpdateTopology(log, topology)
					}
				}

				if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort(tc.clusterQueue.Spec.CohortName).Obj()); err != nil {
					t.Fatalf("Failed to create a cohort")
				}

				snapshot, err := cache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				clusterQueue := snapshot.ClusterQueue(kueue.ClusterQueueReference(tc.clusterQueue.Name))

				if clusterQueue == nil {
					t.Fatalf("Failed to create CQ snapshot")
				}
				if tc.clusterQueueUsage != nil {
					clusterQueue.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.clusterQueueUsage}})
				}

				if tc.secondaryClusterQueue != nil {
					secondaryClusterQueue := snapshot.ClusterQueue(kueue.ClusterQueueReference(tc.secondaryClusterQueue.Name))
					if secondaryClusterQueue == nil {
						t.Fatalf("Failed to create secondary CQ snapshot")
					}
					secondaryClusterQueue.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.secondaryClusterQueueUsage}})
				}

				flvAssigner := New(
					wlInfo,
					clusterQueue,
					resourceFlavors,
					tc.enableFairSharing,
					&testOracle{simulationResult: tc.simulationResult},
					tc.preemptWorkloadSlice,
					configapi.QuotaCheckBlockUndeclared,
					resources.NewResourceFormatter(),
					0,
				)
				assignment := flvAssigner.AssignFlavors(ctx, log, nil)
				if repMode := assignment.RepresentativeMode(); repMode != tc.wantRepMode {
					t.Errorf("e.assignFlavors(_).RepresentativeMode()=%s, want %s", repMode, tc.wantRepMode)
				}

				var cmpOpts []cmp.Option
				if !unadmittedWorkloadsObservabilityEnabled {
					cmpOpts = append(cmpOpts, cmpopts.IgnoreFields(Assignment{}, "NoFitReason"))
					cmpOpts = append(cmpOpts, cmpopts.IgnoreFields(FlavorAssignmentAttempt{}, "NoFitReason"))
				}

				if diff := cmp.Diff(tc.wantAssignment, assignment,
					append(cmpOpts,
						cmpopts.EquateEmpty(),
						cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{}),
						statusComparer, cmpopts.IgnoreFields(Assignment{}, "FlavorScanState"),
						cmpopts.IgnoreFields(PodSetAssignment{}, "FlavorAssignmentAttempts"),
					)...,
				); diff != "" {
					t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
				}

				flexAssertConsidered(t, tc.wantAssignment, assignment, cmpOpts...)
			})
		}
	}
}

// We have 3 flavors: uno, due, tre. Each has 10 compute and 10 gpu.
// These FlavorResources are provided by test-clusterqueue, and made
// available to its Cohort.
func TestAssignFlavors_ReclaimBeforePriorityPreemption(t *testing.T) {
	type rfMap = map[corev1.ResourceName]kueue.ResourceFlavorReference
	cases := map[string]struct {
		workloadRequests       *utiltestingapi.PodSetWrapper
		testClusterQueueUsage  resources.FlavorResourceQuantities
		otherClusterQueueUsage resources.FlavorResourceQuantities
		flavorFungibility      *kueue.FlavorFungibility
		wantMode               FlavorAssignmentMode
		wantAssigment          rfMap
		simulationResult       map[resources.FlavorResource]simulationResultForFlavor
	}{
		"Select first flavor which fits": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: resources.NewAmount(1),
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: resources.NewAmount(1),
			},
			wantMode:      Fit,
			wantAssigment: rfMap{"gpu": "tre"},
		},
		"Select first flavor where gpu reclamation is possible": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: resources.NewAmount(1),
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: resources.NewAmount(1),
				{Flavor: "tre", Resource: "gpu"}: resources.NewAmount(1),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "uno", Resource: "gpu"}: {preemptioncommon.Preempt, 0},
				{Flavor: "due", Resource: "gpu"}: {preemptioncommon.Reclaim, 0},
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "due"},
		},
		"Select first flavor when flavor fungibility is disabled": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: resources.NewAmount(1),
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: resources.NewAmount(1),
				{Flavor: "tre", Resource: "gpu"}: resources.NewAmount(1),
			},
			flavorFungibility: &kueue.FlavorFungibility{
				WhenCanPreempt: kueue.MayStopSearch,
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "uno"},
		},
		"Select first flavor when flavor fungibility is disabled; using deprecated WhenCanPreempt=MayStopSearch": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: resources.NewAmount(1),
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: resources.NewAmount(1),
				{Flavor: "tre", Resource: "gpu"}: resources.NewAmount(1),
			},
			flavorFungibility: &kueue.FlavorFungibility{
				WhenCanPreempt: kueue.MayStopSearch,
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "uno"},
		},
		"Select first flavor where priority based preemption is possible": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: resources.NewAmount(1),
				{Flavor: "due", Resource: "gpu"}: resources.NewAmount(1),
				{Flavor: "tre", Resource: "gpu"}: resources.NewAmount(1),
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "uno"},
		},
		"Select second flavor where gpu reclamation is possible, as compute Fits": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request("gpu", "10").Request("compute", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}:     resources.NewAmount(1),
				{Flavor: "uno", Resource: "compute"}: resources.NewAmount(1),
				{Flavor: "due", Resource: "compute"}: resources.NewAmount(1),
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: resources.NewAmount(1),
				{Flavor: "tre", Resource: "gpu"}: resources.NewAmount(1),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "uno", Resource: "gpu"}: {preemptioncommon.Preempt, 0},
				{Flavor: "due", Resource: "gpu"}: {preemptioncommon.Reclaim, 0},
				{Flavor: "tre", Resource: "gpu"}: {preemptioncommon.Reclaim, 0},
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "tre", "compute": "tre"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			log := testr.NewWithOptions(t, testr.Options{Verbosity: 2})
			resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"uno": utiltestingapi.MakeResourceFlavor("uno").Obj(),
				"due": utiltestingapi.MakeResourceFlavor("due").Obj(),
				"tre": utiltestingapi.MakeResourceFlavor("tre").Obj(),
			}
			testCq := *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Cohort("cohort").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.TryNextFlavor,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("uno").Resource("compute", "10").Resource("gpu", "10").Obj(),
					*utiltestingapi.MakeFlavorQuotas("due").Resource("compute", "10").Resource("gpu", "10").Obj(),
					*utiltestingapi.MakeFlavorQuotas("tre").Resource("compute", "10").Resource("gpu", "10").Obj(),
				).Obj()
			otherCq := *utiltestingapi.MakeClusterQueue("other-clusterqueue").
				Cohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("uno").Resource("compute", "0").Resource("gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("due").Resource("compute", "0").Resource("gpu", "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("tre").Resource("compute", "0").Resource("gpu", "0").Obj(),
				).Obj()

			wlInfo := workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						tc.workloadRequests.PodSet,
					},
				},
			})

			if tc.flavorFungibility != nil {
				testCq.Spec.FlavorFungibility = tc.flavorFungibility
			}

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &testCq); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			if err := cache.AddClusterQueue(ctx, &otherCq); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			otherClusterQueue := snapshot.ClusterQueue("other-clusterqueue")
			otherClusterQueue.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.otherClusterQueueUsage}})

			testClusterQueue := snapshot.ClusterQueue("test-clusterqueue")
			testClusterQueue.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.testClusterQueueUsage}})

			flvAssigner := New(wlInfo, testClusterQueue, resourceFlavors, false, &testOracle{tc.simulationResult}, nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			assignment := flvAssigner.AssignFlavors(ctx, log, nil)
			if gotRepMode := assignment.RepresentativeMode(); gotRepMode != tc.wantMode {
				t.Errorf("Unexpected RepresentativeMode. got %s, want %s", gotRepMode, tc.wantMode)
			}
			if len(assignment.PodSets[0].Flavors) != len(tc.wantAssigment) {
				t.Errorf("Wrong number of flavors. got %d, want %d", len(assignment.PodSets[0].Flavors), len(tc.wantAssigment))
			}
			for resourceName, wantFlavor := range tc.wantAssigment {
				if gotFlavor := assignment.PodSets[0].Flavors[resourceName].Name; gotFlavor != wantFlavor {
					t.Errorf("Unexpected flavor. got %s, want %s", gotFlavor, wantFlavor)
				}
			}
		})
	}
}

// Tests the case where the Cache's flavors and CQs flavors
// fall out of sync, so that the CQ has flavors which no-longer exist.
func TestAssignFlavors_DeletedFlavors(t *testing.T) {
	cases := map[string]struct {
		wlPods            []kueue.PodSet
		wlReclaimablePods []kueue.ReclaimablePod
		clusterQueue      kueue.ClusterQueue
		wantRepMode       FlavorAssignmentMode
		wantAssignment    Assignment
	}{
		"multiple flavors, skip missing ResourceFlavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("deleted-flavor").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("4").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("4").Append().
						Obj(),
				).Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "flavor", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3"),
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "deleted-flavor",
							Mode:        NoFit,
							Reasons:     []string{"flavor deleted-flavor not found"},
							NoFitReason: "NoMatchingFlavor",
						},
						{Flavor: "flavor", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "flavor", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				}}},
			},
		},
		"flavor not found": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("deleted-flavor").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("4").Append().
						Obj(),
				).Obj(),
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Status: *NewStatus("flavor deleted-flavor not found"),
					Count:  1,
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{
							Flavor:      "deleted-flavor",
							Mode:        NoFit,
							Reasons:     []string{"flavor deleted-flavor not found"},
							NoFitReason: "NoMatchingFlavor",
						},
					},
				}},
				Usage:       workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{}}},
				NoFitReason: "NoMatchingFlavor",
			},
		},
	}

	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/gate_%t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, enabled)
				ctx, _ := utiltesting.ContextWithLog(t)
				log := testr.NewWithOptions(t, testr.Options{
					Verbosity: 2,
				})
				wlInfo := workload.NewInfo(log, &kueue.Workload{
					Spec: kueue.WorkloadSpec{
						PodSets: tc.wlPods,
					},
					Status: kueue.WorkloadStatus{
						ReclaimablePods: tc.wlReclaimablePods,
					},
				})

				cache := schdcache.New(utiltesting.NewFakeClient())
				if err := cache.AddClusterQueue(ctx, &tc.clusterQueue); err != nil {
					t.Fatalf("Failed to add CQ to cache")
				}

				flavorMap := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
					"flavor":         utiltestingapi.MakeResourceFlavor("flavor").Obj(),
					"deleted-flavor": utiltestingapi.MakeResourceFlavor("deleted-flavor").Obj(),
				}

				// we have to add the deleted flavor to the cache before snapshot,
				// or else snapshot will fail
				for _, flavor := range flavorMap {
					cache.AddOrUpdateResourceFlavor(log, flavor)
				}
				snapshot, err := cache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				clusterQueue := snapshot.ClusterQueue(kueue.ClusterQueueReference(tc.clusterQueue.Name))
				if clusterQueue == nil {
					t.Fatalf("Failed to create CQ snapshot")
				}

				// and we delete it
				cache.DeleteResourceFlavor(log, flavorMap["deleted-flavor"])
				delete(flavorMap, "deleted-flavor")

				flvAssigner := New(wlInfo, clusterQueue, flavorMap, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
				assignment := flvAssigner.AssignFlavors(ctx, log, nil)
				if repMode := assignment.RepresentativeMode(); repMode != tc.wantRepMode {
					t.Errorf("e.assignFlavors(_).RepresentativeMode()=%s, want %s", repMode, tc.wantRepMode)
				}

				var cmpOpts []cmp.Option
				if !enabled {
					cmpOpts = append(cmpOpts, cmpopts.IgnoreFields(Assignment{}, "NoFitReason"))
					cmpOpts = append(cmpOpts, cmpopts.IgnoreFields(FlavorAssignmentAttempt{}, "NoFitReason"))
				}

				if diff := cmp.Diff(tc.wantAssignment, assignment,
					append(cmpOpts,
						cmpopts.EquateEmpty(),
						cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{}), statusComparer, cmpopts.IgnoreFields(Assignment{}, "FlavorScanState"),
					)...,
				); diff != "" {
					t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

// We have 3 flavors: one, two, three and a 3-level cohort hierarchy where
// each cohort get 4 units of CPU in a different flavor.
func TestAssignFlavors_Hierarchical(t *testing.T) {
	type rfMap = map[corev1.ResourceName]kueue.ResourceFlavorReference
	defaultTestClusterQueueFlavors := func() []kueue.FlavorQuotas {
		return []kueue.FlavorQuotas{
			*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "0").Obj(),
			*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "0").Obj(),
			*utiltestingapi.MakeFlavorQuotas("three").Resource(corev1.ResourceCPU, "0").Obj(),
		}
	}
	cases := map[string]struct {
		workloadRequests        *utiltestingapi.PodSetWrapper
		testClusterQueueFlavors []kueue.FlavorQuotas
		testClusterQueueUsage   resources.FlavorResourceQuantities
		otherClusterQueueUsage  resources.FlavorResourceQuantities
		flavorFungibility       *kueue.FlavorFungibility
		simulationResult        map[resources.FlavorResource]simulationResultForFlavor
		wantMode                FlavorAssignmentMode
		wantAssigment           rfMap
	}{
		"Select the top flavor": {
			workloadRequests:        utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "4"),
			testClusterQueueFlavors: defaultTestClusterQueueFlavors(),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			wantMode:      Fit,
			wantAssigment: rfMap{corev1.ResourceCPU: "three"},
		},
		"Select the first flavor which fits": {
			workloadRequests:        utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "4"),
			testClusterQueueFlavors: defaultTestClusterQueueFlavors(),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			wantMode:      Fit,
			wantAssigment: rfMap{corev1.ResourceCPU: "two"},
		},
		"Select deeper-borrowing flavor that fits when shallower-borrowing flavor has no preemption candidates": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "4"),
			testClusterQueueFlavors: []kueue.FlavorQuotas{
				*utiltestingapi.MakeFlavorQuotas("one").
					ResourceQuotaWrapper(corev1.ResourceCPU).
					NominalQuota("4").
					BorrowingLimit("0").
					Append().
					Obj(),
				*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("three").Resource(corev1.ResourceCPU, "0").Obj(),
			},
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			flavorFungibility: &kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "one", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 1},
			},
			wantMode:      Fit,
			wantAssigment: rfMap{corev1.ResourceCPU: "two"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			log := testr.NewWithOptions(t, testr.Options{Verbosity: 2})
			resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"one":   utiltestingapi.MakeResourceFlavor("one").Obj(),
				"two":   utiltestingapi.MakeResourceFlavor("two").Obj(),
				"three": utiltestingapi.MakeResourceFlavor("three").Obj(),
			}
			cohorts := []*kueue.Cohort{
				utiltestingapi.MakeCohort("three").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("three").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("two").
					Parent("three").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
				utiltestingapi.MakeCohort("one").
					Parent("two").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Obj()).Obj(),
			}
			testCq := *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				Cohort("one").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(tc.testClusterQueueFlavors...).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.TryNextFlavor,
				}).Obj()
			otherCq := *utiltestingapi.MakeClusterQueue("other-clusterqueue").
				Cohort("two").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("three").Resource(corev1.ResourceCPU, "0").Obj(),
			).Obj()

			wlInfo := workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						tc.workloadRequests.PodSet,
					},
				},
			})

			if tc.flavorFungibility != nil {
				testCq.Spec.FlavorFungibility = tc.flavorFungibility
			}
			cache := schdcache.New(utiltesting.NewFakeClient())
			for _, cohort := range cohorts {
				if err := cache.AddOrUpdateCohort(cohort); err != nil {
					t.Fatalf("Couldn't add Cohort to cache: %v", err)
				}
			}
			if err := cache.AddClusterQueue(ctx, &testCq); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			if err := cache.AddClusterQueue(ctx, &otherCq); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			otherClusterQueue := snapshot.ClusterQueue("other-clusterqueue")
			otherClusterQueue.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.otherClusterQueueUsage}})

			testClusterQueue := snapshot.ClusterQueue("test-clusterqueue")
			testClusterQueue.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.testClusterQueueUsage}})

			flvAssigner := New(wlInfo, testClusterQueue, resourceFlavors, false, &testOracle{tc.simulationResult}, nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			assignment := flvAssigner.AssignFlavors(ctx, log, nil)
			if gotRepMode := assignment.RepresentativeMode(); gotRepMode != tc.wantMode {
				t.Errorf("Unexpected RepresentativeMode. got %s, want %s", gotRepMode, tc.wantMode)
			}
			if len(assignment.PodSets[0].Flavors) != len(tc.wantAssigment) {
				t.Errorf("Wrong number of flavors. got %d, want %d", len(assignment.PodSets[0].Flavors), len(tc.wantAssigment))
			}
			for resourceName, wantFlavor := range tc.wantAssigment {
				if gotFlavor := assignment.PodSets[0].Flavors[resourceName].Name; gotFlavor != wantFlavor {
					t.Errorf("Unexpected flavor. got %s, want %s", gotFlavor, wantFlavor)
				}
			}
		})
	}
}

func TestIsPreferred(t *testing.T) {
	makePref := func(p kueue.FlavorFungibilityPreference) *kueue.FlavorFungibilityPreference {
		return &p
	}

	cases := map[string]struct {
		a             granularMode
		b             granularMode
		config        kueue.FlavorFungibility
		wantPreferred bool
	}{
		"feature gate disabled prioritises preemption": {
			a: granularMode{preemptionMode: fit, borrowingLevel: 0},
			b: granularMode{preemptionMode: preempt, borrowingLevel: 0},
			config: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
			},
			wantPreferred: true,
		},
		"explicit BorrowingOverPreemption prioritises borrowing distance": {
			a: granularMode{preemptionMode: preempt, borrowingLevel: 1},
			b: granularMode{preemptionMode: fit, borrowingLevel: 2},
			config: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     makePref(kueue.BorrowingOverPreemption),
			},
			wantPreferred: false,
		},
		"explicit PreemptionOverBorrowing prioritises lower preemption": {
			a: granularMode{preemptionMode: preempt, borrowingLevel: 1},
			b: granularMode{preemptionMode: fit, borrowingLevel: 2},
			config: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     makePref(kueue.PreemptionOverBorrowing),
			},
			wantPreferred: true,
		},
		"explicit PreemptionOverBorrowing breaks borrowing ties with preemption": {
			a: granularMode{preemptionMode: preempt, borrowingLevel: 1},
			b: granularMode{preemptionMode: fit, borrowingLevel: 1},
			config: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     makePref(kueue.PreemptionOverBorrowing),
			},
			wantPreferred: false,
		},
		"explicit PreemptionOverBorrowing rejects no-candidate flavor before borrowing comparison": {
			a: granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 1},
			b: granularMode{preemptionMode: fit, borrowingLevel: 2},
			config: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     makePref(kueue.PreemptionOverBorrowing),
			},
			wantPreferred: false,
		},
		"explicit PreemptionOverBorrowing prefers fit over shallower-borrowing no-candidate flavor": {
			a: granularMode{preemptionMode: fit, borrowingLevel: 2},
			b: granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 1},
			config: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     makePref(kueue.PreemptionOverBorrowing),
			},
			wantPreferred: true,
		},
		"no-candidate flavor remains preferable to no-fit flavor": {
			a:             granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 1},
			b:             granularMode{preemptionMode: noFit, borrowingLevel: 0},
			wantPreferred: true,
		},
		"no-fit flavor remains worse than no-candidate flavor": {
			a:             granularMode{preemptionMode: noFit, borrowingLevel: 0},
			b:             granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 1},
			wantPreferred: false,
		},
		"no-candidate flavors retain borrowing preference": {
			a: granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 1},
			b: granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 2},
			config: kueue.FlavorFungibility{
				Preference: makePref(kueue.PreemptionOverBorrowing),
			},
			wantPreferred: true,
		},
		"no-candidate flavors retain borrowing preference in reverse": {
			a: granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 2},
			b: granularMode{preemptionMode: noPreemptionCandidates, borrowingLevel: 1},
			config: kueue.FlavorFungibility{
				Preference: makePref(kueue.PreemptionOverBorrowing),
			},
			wantPreferred: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := isPreferred(tc.a, tc.b, tc.config); got != tc.wantPreferred {
				t.Fatalf("isPreferred(%+v, %+v, %+v)=%t, want %t", tc.a, tc.b, tc.config, got, tc.wantPreferred)
			}
		})
	}
}

func TestWorkloadsTopologyRequests_ErrorBranches(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	cases := map[string]struct {
		cq         schdcache.ClusterQueueSnapshot
		assignment Assignment
		workload   workload.Info
		wantErr    error
	}{
		"workload requires Topology, but there is no TAS cache information": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  1,
					Status: *NewStatus(),
				}},
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			wantErr: ErrNoTASCacheInformation,
		},
		"workload requires Topology, but there is no TAS flavor assigned": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": nil},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  1,
					Status: *NewStatus(),
				}},
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			wantErr: ErrNoTASFlavorAssigned,
		},
		"more than one TAS flavor assigned (onlyTASFlavor fails); RepresentativeMode must be NoFit": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
					"flavor-a": {},
					"flavor-b": {},
				},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "flavor-a", Mode: Fit, TriedFlavorIdx: 0},
						corev1.ResourceMemory: {Name: "flavor-b", Mode: Fit, TriedFlavorIdx: 0},
					},
					Count:  1,
					Status: *NewStatus(),
				}},
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			wantErr: &MultipleTASFlavorsAssignedError{Flavors: []kueue.ResourceFlavorReference{"flavor-a", "flavor-b"}},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tasReqs := tc.assignment.WorkloadsTopologyRequests(testr.New(t), &tc.workload, &tc.cq)
			if len(tasReqs) != 0 {
				t.Errorf("expected no TAS requests, got: %+v", tasReqs)
			}
			if tc.wantErr != nil && !errors.Is(tc.assignment.PodSets[0].Status.err, tc.wantErr) && !errors.Is(tc.wantErr, tc.assignment.PodSets[0].Status.err) {
				t.Errorf("got error %v, want error %v", tc.assignment.PodSets[0].Status.err, tc.wantErr)
			}
			// When TAS request build fails, the assignment should be unfit so the workload is not admitted.
			if got := tc.assignment.RepresentativeMode(); got != NoFit {
				t.Errorf("RepresentativeMode() = %v, want NoFit (workload must not be admitted when TAS request build fails)", got)
			}
		})
	}
}

func TestWorkloadsTopologyRequests_ElasticJobsValidation(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, true)

	// Create a mock TAS flavor snapshot
	tasFlavor := &schdcache.TASFlavorSnapshot{}

	cases := map[string]struct {
		cq         schdcache.ClusterQueueSnapshot
		assignment Assignment
		workload   workload.Info
		wantErr    error
	}{
		"required topology is rejected even with ElasticJobsViaWorkloadSlicesWithTAS enabled": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  2,
					Status: *NewStatus(),
				}},
				representativeMode: new(Fit),
				replaceWorkloadSlice: workload.NewInfo(log, &kueue.Workload{
					Status: kueue.WorkloadStatus{
						Admission: &kueue.Admission{
							PodSetAssignments: []kueue.PodSetAssignment{{
								Name:               kueue.DefaultPodSetName,
								TopologyAssignment: &kueue.TopologyAssignment{},
							}},
						},
					},
				}),
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Annotations: map[string]string{
					"kueue.x-k8s.io/elastic-job": "true",
				},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			wantErr: ErrElasticRequiredTopologyNotSupported,
		},
		"preferred topology is accepted with ElasticJobsViaWorkloadSlices": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  2,
					Status: *NewStatus(),
				}},
				representativeMode: new(Fit),
				replaceWorkloadSlice: workload.NewInfo(log, &kueue.Workload{
					Status: kueue.WorkloadStatus{
						Admission: &kueue.Admission{
							PodSetAssignments: []kueue.PodSetAssignment{{
								Name:               kueue.DefaultPodSetName,
								TopologyAssignment: &kueue.TopologyAssignment{},
							}},
						},
					},
				}),
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Annotations: map[string]string{
					"kueue.x-k8s.io/elastic-job": "true",
				},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			wantErr: nil,
		},
		"preferred topology is accepted for new elastic workload": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  2,
					Status: *NewStatus(),
				}},
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Annotations: map[string]string{
					"kueue.x-k8s.io/elastic-job": "true",
				},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			wantErr: nil,
		},
		"unconstrained topology is accepted with ElasticJobsViaWorkloadSlices": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  2,
					Status: *NewStatus(),
				}},
				replaceWorkloadSlice: workload.NewInfo(log, &kueue.Workload{
					Status: kueue.WorkloadStatus{
						Admission: &kueue.Admission{
							PodSetAssignments: []kueue.PodSetAssignment{{
								Name:               kueue.DefaultPodSetName,
								TopologyAssignment: &kueue.TopologyAssignment{},
							}},
						},
					},
				}),
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Annotations: map[string]string{
					"kueue.x-k8s.io/elastic-job": "true",
				},
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							UnconstrainedTopologyRequest().
							Obj(),
					},
				},
			}),
		},
		"required topology is accepted for regular jobs with ElasticJobsViaWorkloadSlicesWithTAS": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  2,
					Status: *NewStatus(),
				}},
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
		},
		"preferred topology is accepted for regular jobs with ElasticJobsViaWorkloadSlicesWithTAS": {
			cq: schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			},
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
					},
					Count:  2,
					Status: *NewStatus(),
				}},
			},
			workload: *workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							PreferredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tasReqs := tc.assignment.WorkloadsTopologyRequests(testr.New(t), &tc.workload, &tc.cq)
			if tc.wantErr != nil {
				if len(tasReqs) != 0 {
					t.Errorf("expected no TAS requests, got: %+v", tasReqs)
				}
				if !errors.Is(tc.assignment.PodSets[0].Status.err, tc.wantErr) && !errors.Is(tc.wantErr, tc.assignment.PodSets[0].Status.err) {
					t.Errorf("got error %v, want error %v", tc.assignment.PodSets[0].Status.err, tc.wantErr)
				}
				if got := tc.assignment.RepresentativeMode(); got != NoFit {
					t.Errorf("RepresentativeMode() = %v, want NoFit (workload must not be admitted when elastic job validation fails)", got)
				}
			} else {
				if tc.assignment.PodSets[0].Status.err != nil {
					t.Errorf("expected no error, got: %v", tc.assignment.PodSets[0].Status.err)
				}
				if got := tc.assignment.RepresentativeMode(); got != Fit {
					t.Errorf("RepresentativeMode() = %v, want Fit", got)
				}
			}
		})
	}
}

func TestAssignment_TotalRequestsFor(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	type fields struct {
		PodSets              []PodSetAssignment
		replaceWorkloadSlice *workload.Info
	}
	type args struct {
		wl *workload.Info
	}
	tests := map[string]struct {
		fields fields
		args   args
		want   resources.FlavorResourceQuantities
	}{
		"RegularWorkload": {
			// Regular workload without replacement workload-slice and with full admission (vs. partial).
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 2, // Assigned 2 pods.
					},
				},
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()). // Has 2 pods.
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj()),
			},
			want: resources.FlavorResourceQuantities{ // Want quantities for 2 pods.
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(2 * 1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(2 * 1048576),
			},
		},
		"WorkloadWithPartialAdmission": {
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 1, // Assigned 1 pod (partial assignment).
					},
				},
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()). // Has 2 pods.
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj()),
			},
			want: resources.FlavorResourceQuantities{ // Want quantity for 1 pod.
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(1048576),
			},
		},
		"WorkloadWithReplacement": {
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 3, // Assigned the full 3 pods.
					},
				},
				replaceWorkloadSlice: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj()),
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Obj()).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj()),
			},
			want: resources.FlavorResourceQuantities{
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(2 * 1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(2 * 1048576),
			},
		},
		"WorkloadWithReplacementAndPartialAdmission": {
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "default", Mode: Preempt, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("6"),
						},
						Count: 6, // Partially assigned 6 of the 10 pods.
					},
				},
				replaceWorkloadSlice: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()).
					Request(corev1.ResourceCPU, "1").
					Obj()),
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 10).Obj()).
					Request(corev1.ResourceCPU, "1").
					Obj()),
			},
			want: resources.FlavorResourceQuantities{ // Want the 4 pods added to the replaced 2.
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(4 * 1000),
			},
		},
		"WorkloadWithPodsQuota": {
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:  {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourcePods: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("2"),
							corev1.ResourcePods: resource.MustParse("2"),
						},
						Count: 2,
					},
				},
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()).
					Request(corev1.ResourceCPU, "1").
					Obj()),
			},
			want: resources.FlavorResourceQuantities{
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(2 * 1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(2),
			},
		},
		"WorkloadWithQuotaReservationAndPodsQuota": {
			// A workload taking a second pass gets its requests from its admission,
			// which already counts Pods.
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:  {Name: "default", Mode: Preempt, TriedFlavorIdx: -1},
							corev1.ResourcePods: {Name: "default", Mode: Preempt, TriedFlavorIdx: -1},
						},
						Count: 3,
					},
				},
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).Obj()).
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "3").
							Assignment(corev1.ResourcePods, "default", "3").
							Count(3).
							Obj()).
						Obj(), time.Now()).
					Obj()),
			},
			want: resources.FlavorResourceQuantities{
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(3 * 1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(3),
			},
		},
		"WorkloadWithZeroQuantityResourceNotInClusterQueueSkipsResourceWithoutFlavor": {
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 2,
					},
				},
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).Obj()).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Request("example.com/gpu", "0").
					Obj()),
			},
			want: resources.FlavorResourceQuantities{
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(2 * 1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(2 * 1048576),
			},
		},
		"WorkloadWithMultiplePodSetsOneUnchangedReplacement": {
			fields: fields{
				PodSets: []PodSetAssignment{
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 2, // Unchanged: was 2, still 2
					},
					{
						Name: "coordinator",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("2"),
							corev1.ResourceMemory: resource.MustParse("2Mi"),
						},
						Count: 3, // Changed: was 1, now 3
					},
				},
				replaceWorkloadSlice: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(
						*utiltestingapi.MakePodSet("worker", 2).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Mi").
							Obj(),
						*utiltestingapi.MakePodSet("coordinator", 1).
							Request(corev1.ResourceCPU, "2").
							Request(corev1.ResourceMemory, "2Mi").
							Obj(),
					).
					Obj()),
			},
			args: args{
				wl: workload.NewInfo(log, utiltestingapi.MakeWorkload("test", "default").
					PodSets(
						*utiltestingapi.MakePodSet("worker", 2).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Mi").
							Obj(),
						*utiltestingapi.MakePodSet("coordinator", 3).
							Request(corev1.ResourceCPU, "2").
							Request(corev1.ResourceMemory, "2Mi").
							Obj(),
					).
					Obj()),
			},
			want: resources.FlavorResourceQuantities{
				// worker: (2 - 2) * 1 CPU = 0 (no additional quota needed)
				// coordinator: (3 - 1) * 2 CPU = 4 (need 2 more pods worth of resources)
				// Total CPU: 0 + 4000m = 4000m
				// Total Memory: 0 + 4Mi = 4Mi
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(4 * 1000),
				resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(4 * 1048576),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			a := &Assignment{
				PodSets:              tt.fields.PodSets,
				replaceWorkloadSlice: tt.fields.replaceWorkloadSlice,
			}
			got := a.TotalRequestsFor(log, tt.args.wl)
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("TotalRequestsFor() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAssignment_ComputeTASNetUsage(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	tests := map[string]struct {
		assignment    Assignment
		wl            *workload.Info
		cq            *schdcache.ClusterQueueSnapshot
		prevAdmission *kueue.Admission
		want          workload.TASUsage
	}{
		"records actual pod requests when assignment requests differ": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:        {Name: "tas"},
						corev1.ResourceMemory:     {Name: "tas"},
						"example.com/logical-gpu": {Name: "quota"},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:        resource.MustParse("2"),
						corev1.ResourceMemory:     resource.MustParse("2Gi"),
						"example.com/logical-gpu": resource.MustParse("2"),
					},
					Count: 2,
					TopologyAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Values: []string{"node-a"},
							Count:  2,
						}},
					},
				}},
			},
			wl: workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							// In assignment requests, example.com/gpu is transformed to
							// example.com/logical-gpu and networking.example.com/vpc is excluded.
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Request("example.com/gpu", "1").
							Request("networking.example.com/vpc", "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			cq: &schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
					"tas": {},
				},
			},
			want: workload.TASUsage{
				"tas": []workload.TopologyDomainRequests{{
					Values: []string{"node-a"},
					SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{
						corev1.ResourceCPU:           resource.MustParse("1"),
						corev1.ResourceMemory:        resource.MustParse("1Gi"),
						"example.com/gpu":            resource.MustParse("1"),
						"networking.example.com/vpc": resource.MustParse("1"),
					}),
					Count: 2,
				}},
			},
		},
		"accounts for a domain the recomputed assignment moved to, when the previous admission held a different domain": {
			// A second pass replacing an unhealthy node moves the PodSet to a domain
			// nothing has accounted for yet. That claim has to reach the net usage,
			// otherwise the fits check never validates it and AddUsage never records it.
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "tas"},
						corev1.ResourceMemory: {Name: "tas"},
					},
					Count: 2,
					TopologyAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Values: []string{"node-b"},
							Count:  2,
						}},
					},
				}},
			},
			wl: workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			cq: &schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
					"tas": {},
				},
			},
			prevAdmission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 2).Obj()).
						Obj(),
				}},
			},
			want: workload.TASUsage{
				"tas": []workload.TopologyDomainRequests{{
					Values: []string{"node-b"},
					SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					}),
					Count: 2,
				}},
			},
		},
		"counts only the additional pods when the recomputed assignment grew an already admitted domain": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas"},
					},
					Count: 3,
					TopologyAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Values: []string{"node-a"},
							Count:  3,
						}},
					},
				}},
			},
			wl: workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
							Request(corev1.ResourceCPU, "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			cq: &schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
					"tas": {},
				},
			},
			prevAdmission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 1).Obj()).
						Obj(),
				}},
			},
			want: workload.TASUsage{
				"tas": []workload.TopologyDomainRequests{{
					Values: []string{"node-a"},
					SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					}),
					Count: 2,
				}},
			},
		},
		"skips usage already present in previous admission": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "tas"},
						corev1.ResourceMemory: {Name: "tas"},
						"example.com/gpu":     {Name: "quota"},
					},
					Count: 2,
					TopologyAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Values: []string{"node-a"},
							Count:  2,
						}},
					},
				}},
			},
			wl: workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "1").
							Request(corev1.ResourceMemory, "1Gi").
							Request("example.com/gpu", "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			cq: &schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
					"tas": {},
				},
			},
			prevAdmission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					// Same domain and count as the new assignment, so the snapshot
					// already accounts for all of it.
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 2).Obj()).
						Obj(),
				}},
			},
			want: workload.TASUsage{},
		},
		"skips a domain whose recomputed count is lower than what the previous admission already accounted for": {
			// Defensive path: current replacement behavior is expected to
			// produce equal or increased per-domain counts, but a negative
			// delta must not be recorded as usage, let alone subtracted.
			assignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tas"},
					},
					Count: 1,
					TopologyAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{{
							Values: []string{"node-a"},
							Count:  1,
						}},
					},
				}},
			},
			wl: workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
							Request(corev1.ResourceCPU, "1").
							RequiredTopologyRequest(corev1.LabelHostname).
							Obj(),
					},
				},
			}),
			cq: &schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
					"tas": {},
				},
			},
			prevAdmission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{{
					Name: kueue.DefaultPodSetName,
					// The previous admission held 3 pods in node-a; the
					// recomputed assignment only claims 1.
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-a"}, 3).Obj()).
						Obj(),
				}},
			},
			want: workload.TASUsage{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := tt.assignment.ComputeTASNetUsage(testr.New(t), tt.cq, tt.wl, tt.prevAdmission)

			if diff := cmp.Diff(tt.want, got, cmp.Comparer(resources.Equal)); diff != "" {
				t.Errorf("Unexpected TAS usage (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestAssignment_RequiresBorrowing(t *testing.T) {
	tests := map[string]struct {
		borrowing int
		want      bool
	}{
		"no borrowing": {
			borrowing: 0,
			want:      false,
		},
		"borrows at level 1": {
			borrowing: 1,
			want:      true,
		},
		"borrows at level 2": {
			borrowing: 2,
			want:      true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			a := &Assignment{Borrowing: tc.borrowing}
			if got := a.RequiresBorrowing(); got != tc.want {
				t.Errorf("RequiresBorrowing() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestWorkloadsTopologyRequests_ZeroCountPodSetSkipped verifies that count=0
// podSets (completed/reclaimable after preemption) are skipped in TAS request
// generation, preventing empty TopologyAssignment slices that cause CRD
// validation errors and infinite scheduling loops.
func TestWorkloadsTopologyRequests_ZeroCountPodSetSkipped(t *testing.T) {
	tasFlavor := &schdcache.TASFlavorSnapshot{}

	cases := map[string]struct {
		podSets        []PodSetAssignment
		wlPodSets      []kueue.PodSet
		wantTASPodSets []string // names of podSets that should have TAS requests
	}{
		"mixed: 1 completed (count=0) + 1 running (count=1)": {
			podSets: []PodSetAssignment{
				{Name: "completed-job", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 0, Status: *NewStatus()},
				{Name: "running-job", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 1, Status: *NewStatus()},
			},
			wlPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("completed-job", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
				*utiltestingapi.MakePodSet("running-job", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
			},
			wantTASPodSets: []string{"running-job"},
		},
		"all completed (count=0): no TAS requests": {
			podSets: []PodSetAssignment{
				{Name: "sampler-0", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 0, Status: *NewStatus()},
				{Name: "controller", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 0, Status: *NewStatus()},
			},
			wlPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("sampler-0", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
				*utiltestingapi.MakePodSet("controller", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
			},
			wantTASPodSets: nil,
		},
		"3 podSets: 2 completed + 1 running": {
			podSets: []PodSetAssignment{
				{Name: "sampler-0", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 0, Status: *NewStatus()},
				{Name: "sampler-1", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 1, Status: *NewStatus()},
				{Name: "controller", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 0, Status: *NewStatus()},
			},
			wlPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("sampler-0", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
				*utiltestingapi.MakePodSet("sampler-1", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
				*utiltestingapi.MakePodSet("controller", 1).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
			},
			wantTASPodSets: []string{"sampler-1"},
		},
		"all running: all get TAS requests": {
			podSets: []PodSetAssignment{
				{Name: "worker-0", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 2, Status: *NewStatus()},
				{Name: "worker-1", Flavors: ResourceAssignment{corev1.ResourceCPU: {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 3, Status: *NewStatus()},
			},
			wlPodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("worker-0", 2).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
				*utiltestingapi.MakePodSet("worker-1", 3).Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj(),
			},
			wantTASPodSets: []string{"worker-0", "worker-1"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cq := schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			}
			assignment := Assignment{PodSets: tc.podSets}
			wl := workload.NewInfo(log, &kueue.Workload{
				Spec: kueue.WorkloadSpec{PodSets: tc.wlPodSets},
			})

			tasReqs := assignment.WorkloadsTopologyRequests(testr.New(t), wl, &cq)

			// Collect which podSets got TAS requests
			var gotPodSets []string
			for _, flavorReqs := range tasReqs {
				for _, req := range flavorReqs {
					gotPodSets = append(gotPodSets, string(req.PodSet.Name))
				}
			}

			if diff := cmp.Diff(tc.wantTASPodSets, gotPodSets, cmpopts.SortSlices(func(a, b string) bool { return a < b }), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("TAS request podSets mismatch (-want +got):\n%s", diff)
			}

			// Verify no errors on count=0 podSets
			for _, ps := range assignment.PodSets {
				if ps.Count == 0 && ps.Status.IsError() {
					t.Errorf("count=0 podSet %q should not have error, got: %v", ps.Name, ps.Status.err)
				}
			}
		})
	}
}
func TestAssignFlavors_AllowedFlavors(t *testing.T) {
	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"f1":       utiltestingapi.MakeResourceFlavor("f1").Obj(),
		"f2":       utiltestingapi.MakeResourceFlavor("f2").Obj(),
		"occupied": utiltestingapi.MakeResourceFlavor("occupied").Obj(),
	}

	cq := *utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("f1").Resource(corev1.ResourceCPU, "10").Obj(),
			*utiltestingapi.MakeFlavorQuotas("f2").Resource(corev1.ResourceCPU, "10").Obj(),
			*utiltestingapi.MakeFlavorQuotas("occupied").Resource(corev1.ResourceCPU, "1").Obj(),
		).Obj()

	tests := map[string]struct {
		allowedFlavors []kueue.ResourceFlavorReference
		wantFlavor     kueue.ResourceFlavorReference
		wantRepMode    FlavorAssignmentMode
	}{
		"allow only f2": {
			allowedFlavors: []kueue.ResourceFlavorReference{"f2"},
			wantFlavor:     "f2",
			wantRepMode:    Fit,
		},
		"allow only f1": {
			allowedFlavors: []kueue.ResourceFlavorReference{"f1"},
			wantFlavor:     "f1",
			wantRepMode:    Fit,
		},
		"allow only the occupied flavor": {
			allowedFlavors: []kueue.ResourceFlavorReference{"occupied"},
			wantFlavor:     "",
			wantRepMode:    NoFit,
		},
		"allow f1 and f2": {
			allowedFlavors: []kueue.ResourceFlavorReference{"f1", "f2"},
			wantFlavor:     "f1", // first fit
			wantRepMode:    Fit,
		},
		"no constraints": {
			allowedFlavors: nil,
			wantFlavor:     "f1",
			wantRepMode:    Fit,
		},
		"allow non-existent": {
			allowedFlavors: []kueue.ResourceFlavorReference{"non-existent"},
			wantFlavor:     "",
			wantRepMode:    NoFit,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
			wlBuilder := utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "2").Obj())
			if tc.allowedFlavors != nil {
				wlBuilder = wlBuilder.AllowedFlavors(tc.allowedFlavors...)
			}
			wl := wlBuilder.Obj()

			wlInfo := workload.NewInfo(log, wl)

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &cq); err != nil {
				t.Fatalf("Failed to add CQ to cache: %v", err)
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cqSnapshot := snapshot.ClusterQueue(kueue.ClusterQueueReference(cq.Name))

			assigner := New(wlInfo, cqSnapshot, resourceFlavors, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			gotAssignment := assigner.AssignFlavors(ctx, log, nil)

			if gotAssignment.RepresentativeMode() != tc.wantRepMode {
				t.Errorf("RepresentativeMode() = %v, want %v", gotAssignment.RepresentativeMode(), tc.wantRepMode)
			}

			if tc.wantRepMode == Fit {
				psAssignment := gotAssignment.PodSets[0]
				gotFlavor := psAssignment.Flavors[corev1.ResourceCPU].Name
				if gotFlavor != tc.wantFlavor {
					t.Errorf("Assigned flavor = %v, want %v", gotFlavor, tc.wantFlavor)
				}
			}
		})
	}
}

// TestAssignFlavors_NoFitDueToCapacityAndLimits covers the NoFitReason that AssignFlavors
// resolves for itself before returning: every case here is blocked at quota time, by a
// structural mismatch or by a capacity limit.
//
// Reasons that only appear once topology placement has failed are not in scope, because
// AssignFlavors cannot know about them yet. The demotion that produces them belongs to
// TestAssignTopology and the aggregation to TestResolveNoFitReason.
func TestAssignFlavors_NoFitDueToCapacityAndLimits(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"flavor-a": utiltestingapi.MakeResourceFlavor("flavor-a").NodeLabel("type", "a").Obj(),
		"flavor-b": utiltestingapi.MakeResourceFlavor("flavor-b").
			Taint(corev1.Taint{
				Key:    "key",
				Value:  "val",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj(),
		"flavor-tas": utiltestingapi.MakeResourceFlavor("flavor-tas").TopologyName("topology-tas").Obj(),
	}

	cq := *utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "4").Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "4").Obj(),
		).Obj()

	tasCQ := utiltestingapi.MakeClusterQueue("cq-tas").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "2").Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "2").Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-tas").Resource(corev1.ResourceCPU, "2", "2").Obj(),
		).Obj()
	tasFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"flavor-a":   resourceFlavors["flavor-a"],
		"flavor-b":   resourceFlavors["flavor-b"],
		"flavor-tas": resourceFlavors["flavor-tas"],
	}

	sharedCQ := *utiltestingapi.MakeClusterQueue("shared-cq").
		Cohort("cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-shared").Resource(corev1.ResourceCPU, "2", "4").Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "4").Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-shared").Resource(corev1.ResourceMemory, "2Gi", "4Gi").Obj(),
		).Obj()

	siblingCQ := utiltestingapi.MakeClusterQueue("sibling").
		Cohort("cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-shared").Resource(corev1.ResourceCPU, "2", "2").Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "2").Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-shared").Resource(corev1.ResourceMemory, "2Gi", "2Gi").Obj(),
		).Obj()

	sharedFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"flavor-shared": utiltestingapi.MakeResourceFlavor("flavor-shared").NodeLabel("type", "shared").Obj(),
		"flavor-a":      utiltestingapi.MakeResourceFlavor("flavor-a").NodeLabel("type", "a").Obj(),
	}

	tests := map[string]struct {
		podSet             kueue.PodSet
		cq                 *kueue.ClusterQueue
		siblingCQs         []*kueue.ClusterQueue
		resourceFlavors    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		cqUsage            resources.FlavorResourceQuantities
		siblingCQUsage     map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities
		replaceWl          *workload.Info
		topologies         []*kueue.Topology
		allowedFlavors     []kueue.ResourceFlavorReference
		featureGates       map[featuregate.Feature]bool
		simulationResult   map[resources.FlavorResource]simulationResultForFlavor
		wantNoFitReason    string
		wantFlavorAttempts map[kueue.ResourceFlavorReference]string
	}{
		"succeeds to schedule on flavor-a": {
			podSet:          *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").Obj(),
			wantNoFitReason: "",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"insufficient quota": {
			podSet:          *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "3").Obj(),
			wantNoFitReason: "ExceedsMaxQuota",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "ExceedsMaxQuota",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"exceeds max capacity limits": {
			podSet:          *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "5").Obj(),
			wantNoFitReason: "ExceedsMaxQuota",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "ExceedsMaxQuota",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"taints mismatch": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(map[string]string{"type": "wrong"}).
				Obj(),
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "NoMatchingFlavor",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"succeeds to schedule on flavor-b with a tolerated taint": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(map[string]string{"type": "non-existent"}).
				Toleration(corev1.Toleration{Key: "key", Operator: corev1.TolerationOpEqual, Value: "val", Effect: corev1.TaintEffectNoSchedule}).
				Obj(),
			// flavor-b has no label keys so the workload's "type" selector is
			// irrelevant to it; it matches and the workload is admitted there.
			wantNoFitReason: "",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "NoMatchingFlavor",
			},
		},
		"node affinity mismatch when both flavors declare same key": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(map[string]string{"type": "non-existent"}).
				Obj(),
			resourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"flavor-a": utiltestingapi.MakeResourceFlavor("flavor-a").NodeLabel("type", "a").Obj(),
				"flavor-b": utiltestingapi.MakeResourceFlavor("flavor-b").NodeLabel("type", "b").Obj(),
			},
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "NoMatchingFlavor",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"flavor mismatch for workload slices": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "2").
				NodeSelector(map[string]string{"type": "a"}).
				Obj(),
			replaceWl: workload.NewInfo(log,
				utiltestingapi.MakeWorkload("wl-old", "ns").
					PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").Obj()).
					Admission(utiltestingapi.MakeAdmission("cq", "main").
						PodSets(kueue.PodSetAssignment{
							Name: "main",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "flavor-b",
							},
						}).Obj()).
					Obj(),
			),
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "NoMatchingFlavor",
			},
		},
		"prioritization of structural mismatch over capacity mismatch": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "3").
				Request(corev1.ResourceMemory, "20Gi").
				NodeSelector(map[string]string{"type": "wrong"}).
				Obj(),
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "NoMatchingFlavor",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"TAS not supported": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				RequiredTopologyRequest("rack").
				Obj(),
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
			},
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "NoMatchingFlavor",
			},
		},
		"TAS level not supported": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				RequiredTopologyRequest("block").
				Obj(),
			cq:              tasCQ,
			resourceFlavors: tasFlavors,
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("topology-tas").Levels("rack").Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
			},
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a":   "NoMatchingFlavor",
				"flavor-b":   "NoMatchingFlavor",
				"flavor-tas": "NoMatchingFlavor",
			},
		},
		"TAS only flavor mismatch": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				NodeSelector(map[string]string{"type": "wrong"}).
				Obj(),
			cq:              tasCQ,
			resourceFlavors: tasFlavors,
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("topology-tas").Levels("rack").Obj(),
			},
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
			},
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a":   "NoMatchingFlavor",
				"flavor-b":   "NoMatchingFlavor",
				"flavor-tas": "NoMatchingFlavor",
			},
		},
		"tas placement fails, cohort has no capacity": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
			},
			resourceFlavors: tasFlavors,
			cq:              tasCQ,
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("topology-tas").Levels("rack").Obj(),
			},
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "3").
				RequiredTopologyRequest("rack").
				Obj(),
			wantNoFitReason: "ExceedsMaxQuota",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a":   "NoMatchingFlavor",
				"flavor-b":   "NoMatchingFlavor",
				"flavor-tas": "ExceedsMaxQuota",
			},
		},
		"flavor not allowed by annotations": {
			podSet: *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").Obj(),
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: "flavor-a", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			allowedFlavors: []kueue.ResourceFlavorReference{"flavor-a"},
			featureGates: map[featuregate.Feature]bool{
				features.ConcurrentAdmission: true,
			},
			wantNoFitReason: "",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"insufficient capacity, cohort has available capacity (waiting for quota)": {
			podSet: *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "3").Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "2").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "2").Obj(),
				).Obj(),
			siblingCQs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("sibling").
					Cohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "2").Obj(),
						*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "2").Obj(),
					).Obj(),
			},
			siblingCQUsage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"sibling": {
					{Flavor: "flavor-a", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
					{Flavor: "flavor-b", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				},
			},
			wantNoFitReason: "WaitingForQuota",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "WaitingForQuota",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"exceeds cohort max capacity limits": {
			podSet: *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "5").Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "2").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "2").Obj(),
				).Obj(),
			siblingCQs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("sibling").
					Cohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "2").Obj(),
						*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "2").Obj(),
					).Obj(),
			},
			wantNoFitReason: "ExceedsMaxQuota",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "ExceedsMaxQuota",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"exceeds borrowing limit but cohort has capacity": {
			podSet: *utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "4").Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "2", "1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "2", "1").Obj(),
				).Obj(),
			siblingCQs: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("sibling").
					Cohort("cohort").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("flavor-a").Resource(corev1.ResourceCPU, "5", "5").Obj(),
						*utiltestingapi.MakeFlavorQuotas("flavor-b").Resource(corev1.ResourceCPU, "5", "5").Obj(),
					).Obj(),
			},
			wantNoFitReason: "ExceedsMaxQuota",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a": "ExceedsMaxQuota",
				"flavor-b": "NoMatchingFlavor",
			},
		},
		"flavor spanning multiple resource groups": {
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "3").
				Request(corev1.ResourceMemory, "3Gi").
				NodeSelector(map[string]string{"type": "a"}).
				Obj(),
			cq:         &sharedCQ,
			siblingCQs: []*kueue.ClusterQueue{siblingCQ},
			siblingCQUsage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"sibling": {
					{Flavor: "flavor-shared", Resource: corev1.ResourceCPU}:    resources.NewAmount(2_000),
					{Flavor: "flavor-shared", Resource: corev1.ResourceMemory}: resources.NewAmount(2 * utiltesting.Gi),
					{Flavor: "flavor-a", Resource: corev1.ResourceCPU}:         resources.NewAmount(2_000),
				},
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "flavor-shared", Resource: corev1.ResourceCPU}: {
					preemptionPossiblity: preemptioncommon.NoCandidates,
				},
				{Flavor: "flavor-shared", Resource: corev1.ResourceMemory}: {
					preemptionPossiblity: preemptioncommon.NoCandidates,
				},
				{Flavor: "flavor-a", Resource: corev1.ResourceCPU}: {
					preemptionPossiblity: preemptioncommon.NoCandidates,
				},
			},
			resourceFlavors: sharedFlavors,
			wantNoFitReason: "NoMatchingFlavor",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-shared": "NoMatchingFlavor",
				"flavor-a":      "WaitingForQuota",
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, true)
			for fg, val := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, val)
			}

			testCQ := cq
			if tc.cq != nil {
				testCQ = *tc.cq
			}
			testFlavors := resourceFlavors
			if tc.resourceFlavors != nil {
				testFlavors = tc.resourceFlavors
			}

			wlBuilder := utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(tc.podSet)
			if len(tc.allowedFlavors) > 0 {
				wlBuilder = wlBuilder.AllowedFlavors(tc.allowedFlavors...)
			}
			wl := wlBuilder.Obj()
			wlInfo := workload.NewInfo(log, wl)

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &testCQ); err != nil {
				t.Fatalf("Failed to add CQ to cache: %v", err)
			}
			for _, sibling := range tc.siblingCQs {
				if err := cache.AddClusterQueue(ctx, sibling); err != nil {
					t.Fatalf("Failed to add sibling CQ to cache: %v", err)
				}
			}
			for _, rf := range testFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, topology := range tc.topologies {
				cache.AddOrUpdateTopology(log, topology)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cqSnapshot := snapshot.ClusterQueue(kueue.ClusterQueueReference(testCQ.Name))
			if len(tc.cqUsage) > 0 {
				cqSnapshot.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.cqUsage}})
			}
			for siblingName, usage := range tc.siblingCQUsage {
				siblingSnapshot := snapshot.ClusterQueue(siblingName)
				if siblingSnapshot == nil {
					t.Fatalf("Sibling ClusterQueue %s not found in snapshot", siblingName)
				}
				siblingSnapshot.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: usage}})
			}

			assigner := New(
				wlInfo, cqSnapshot, testFlavors, false, &testOracle{simulationResult: tc.simulationResult},
				tc.replaceWl, configapi.QuotaCheckBlockUndeclared,
				resources.NewResourceFormatter(), 0,
			)
			gotAssignment := assigner.AssignFlavors(ctx, log, nil)

			if gotAssignment.NoFitReason != tc.wantNoFitReason {
				t.Errorf("gotAssignment.NoFitReason = %q, want %q", gotAssignment.NoFitReason, tc.wantNoFitReason)
			}

			if len(tc.wantFlavorAttempts) > 0 {
				for _, ps := range gotAssignment.PodSets {
					for _, att := range ps.FlavorAssignmentAttempts {
						if wantReason, ok := tc.wantFlavorAttempts[att.Flavor]; ok {
							if att.NoFitReason != wantReason {
								t.Errorf("attempt for flavor %q got reason %q, want %q", att.Flavor, att.NoFitReason, wantReason)
							}
						}
					}
				}
			}
		})
	}
}

// TestAssignFlavors_LeaderWorkerSetTASFlavor covers how AssignFlavors resolves flavors for
// PodSet groups where one member - e.g. an LWS leader - requests none of the group's
// managed resources. Such a PodSet has nothing of its own to match against, so it has to
// inherit the flavor its group resolved to, or it cannot be placed later.
//
// Only the resolved flavors are in scope. Whether a resolution is usable for topology is
// AssignTopology's judgement, and the rejections it raises are asserted in TestAssignTopology.
func TestAssignFlavors_LeaderWorkerSetTASFlavor(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)

	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"tas-a":   utiltestingapi.MakeResourceFlavor("tas-a").TopologyName("tas-topo-a").Obj(),
		"tas-b":   utiltestingapi.MakeResourceFlavor("tas-b").TopologyName("tas-topo-b").Obj(),
		"non-tas": utiltestingapi.MakeResourceFlavor("non-tas").Obj(),
	}
	topologies := []*kueue.Topology{
		utiltestingapi.MakeTopology("tas-topo-a").Levels(corev1.LabelHostname).Obj(),
		utiltestingapi.MakeTopology("tas-topo-b").Levels(corev1.LabelHostname).Obj(),
	}

	cases := map[string]struct {
		wlPods            []kueue.PodSet
		clusterQueue      kueue.ClusterQueue
		wantPodSetFlavors map[kueue.PodSetReference]ResourceAssignment
	}{
		"leader without a managed request infers the group's TAS flavor": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("worker", 1).
					Request("example.com/gpu-a", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-a").
						Resource("example.com/gpu-a", "4").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-b").
						Resource(corev1.ResourceMemory, "8Gi").
						Obj(),
				).Obj(),
			wantPodSetFlavors: map[kueue.PodSetReference]ResourceAssignment{
				"leader": {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}},
				"worker": {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}},
			},
		},
		"leader without a group is rejected (ClusterQueue-wide fallback is not supported)": {
			// Current behavior: a no-request PodSet without PodSetGroup does not inherit
			// a TAS flavor from ClusterQueue-wide state.
			// Potential future support: allow a ClusterQueue-wide fallback if requested.
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj(),
				*utiltestingapi.MakePodSet("worker", 1).
					Request("example.com/gpu-a", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-a").
						Resource("example.com/gpu-a", "4").
						Obj(),
				).Obj(),
			wantPodSetFlavors: map[kueue.PodSetReference]ResourceAssignment{
				"leader": {},
				"worker": {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}},
			},
		},
		"peers in the same group resolving to different TAS flavors are rejected": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("worker-a", 1).
					Request("example.com/gpu-a", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("worker-b", 1).
					Request("example.com/gpu-b", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-a").
						Resource("example.com/gpu-a", "4").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-b").
						Resource("example.com/gpu-b", "4").
						Obj(),
				).Obj(),
			wantPodSetFlavors: map[kueue.PodSetReference]ResourceAssignment{
				"leader":   {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}, "example.com/gpu-b": {Name: "tas-b", Mode: Fit, TriedFlavorIdx: -1}},
				"worker-a": {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}},
				"worker-b": {"example.com/gpu-b": {Name: "tas-b", Mode: Fit, TriedFlavorIdx: -1}},
			},
		},
		"multiple groups: leaders infer flavors from their own groups": {
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader1", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("worker1", 1).
					Request("example.com/gpu-a", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("leader2", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group2").
					Obj(),
				*utiltestingapi.MakePodSet("worker2", 1).
					Request("example.com/gpu-b", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group2").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-a").
						Resource("example.com/gpu-a", "4").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-b").
						Resource("example.com/gpu-b", "4").
						Obj(),
				).Obj(),
			wantPodSetFlavors: map[kueue.PodSetReference]ResourceAssignment{
				// Verify both leaders inferred their flavors from their groups
				"leader1": {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}},
				"worker1": {"example.com/gpu-a": {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1}},
				"leader2": {"example.com/gpu-b": {Name: "tas-b", Mode: Fit, TriedFlavorIdx: -1}},
				"worker2": {"example.com/gpu-b": {Name: "tas-b", Mode: Fit, TriedFlavorIdx: -1}},
			},
		},
		"leader in group where peer resolves to non-TAS flavor is rejected": {
			// Exercises the topology-group TAS fallback path in resolvePodSetFlavors:
			// tasFlavorsOnly returns empty because the group's resolved flavor is non-TAS.
			// With no TAS flavors available in cq.TASFlavors, the leader ends up with empty
			// Flavors and podSetTopologyRequest returns ErrNoTASCacheInformation (checked
			// before onlyTASFlavor).
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("worker", 1).
					Request("example.com/gpu-a", "1").
					PodSetGroup("group1").
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("non-tas").
						Resource("example.com/gpu-a", "4").
						Obj(),
				).Obj(),
			wantPodSetFlavors: map[kueue.PodSetReference]ResourceAssignment{
				"leader": {},
				"worker": {"example.com/gpu-a": {Name: "non-tas", Mode: Fit, TriedFlavorIdx: -1}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)

			wlInfo := workload.NewInfo(log, &kueue.Workload{Spec: kueue.WorkloadSpec{PodSets: tc.wlPods}})

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &tc.clusterQueue); err != nil {
				t.Fatalf("Failed to add CQ to cache: %v", err)
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, topology := range topologies {
				cache.AddOrUpdateTopology(log, topology)
			}
			nodes := []corev1.Node{
				*testingnode.MakeNode("tas-node-a").
					Label(corev1.LabelHostname, "tas-node-a").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourcePods: resource.MustParse("32"),
						"example.com/gpu-a": resource.MustParse("4"),
						"example.com/gpu-b": resource.MustParse("4"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("tas-node-b").
					Label(corev1.LabelHostname, "tas-node-b").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourcePods: resource.MustParse("32"),
						"example.com/gpu-a": resource.MustParse("4"),
						"example.com/gpu-b": resource.MustParse("4"),
					}).
					Ready().
					Obj(),
			}
			for i := range nodes {
				cache.TASCache().SyncNode(&nodes[i])
			}
			if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort(tc.clusterQueue.Spec.CohortName).Obj()); err != nil {
				t.Fatalf("Failed to create a cohort: %v", err)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(tc.clusterQueue.Name))
			if cq == nil {
				t.Fatalf("Failed to create CQ snapshot")
			}

			assigner := New(wlInfo, cq, resourceFlavors, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			assignment := assigner.AssignFlavors(ctx, log, nil)

			gotFlavors := map[kueue.PodSetReference]ResourceAssignment{}
			for _, ps := range assignment.PodSets {
				gotFlavors[ps.Name] = ps.Flavors
			}
			if diff := cmp.Diff(tc.wantPodSetFlavors, gotFlavors, cmpopts.IgnoreUnexported(FlavorAssignment{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("podSet flavors mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

// TestAssignFlavors_TopologySpreadingRequiresLevel verifies that a flavor whose
// topology is missing a level named by a Required topology-spreading rule is
// rejected during flavor assignment, rather than silently admitted with
// spreading left unenforced.
func TestAssignFlavors_TopologySpreadingRequiresLevel(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	features.SetFeatureGateDuringTest(t, features.TASTopologySpreading, true)

	const spreadingAnnotation = `{"workloadLabelSelectors":[{"key":"app","operator":"In","values":["main"]}],"rules":[{"topologyKey":"rack","maxShareAllowingPlacement":"0.5","enforcementMode":"Required"}]}`

	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"tas-with-rack":    utiltestingapi.MakeResourceFlavor("tas-with-rack").TopologyName("topo-with-rack").Obj(),
		"tas-without-rack": utiltestingapi.MakeResourceFlavor("tas-without-rack").TopologyName("topo-without-rack").Obj(),
	}
	topologies := []*kueue.Topology{
		utiltestingapi.MakeTopology("topo-with-rack").Levels("rack", corev1.LabelHostname).Obj(),
		utiltestingapi.MakeTopology("topo-without-rack").Levels(corev1.LabelHostname).Obj(),
	}

	cases := map[string]struct {
		flavor           kueue.ResourceFlavorReference
		explicitTopology bool
		wantFit          bool
		wantErrIn        string
	}{
		"explicit TAS: flavor's topology lacks the rule's level: rejected": {
			flavor:           "tas-without-rack",
			explicitTopology: true,
			wantFit:          false,
			wantErrIn:        "topology spreading",
		},
		"explicit TAS: flavor's topology has the rule's level: admitted": {
			flavor:           "tas-with-rack",
			explicitTopology: true,
			wantFit:          true,
		},
		"implied TAS: flavor's topology lacks the rule's level: rejected": {
			flavor:           "tas-without-rack",
			explicitTopology: false,
			wantFit:          false,
			wantErrIn:        "topology spreading",
		},
		"implied TAS: flavor's topology has the rule's level: admitted": {
			flavor:           "tas-with-rack",
			explicitTopology: false,
			wantFit:          true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)

			psBuilder := utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, "1").
				Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: spreadingAnnotation})
			if tc.explicitTopology {
				psBuilder = psBuilder.RequiredTopologyRequest(corev1.LabelHostname)
			}
			wlPods := []kueue.PodSet{*psBuilder.Obj()}
			wlInfo := workload.NewInfo(log, &kueue.Workload{Spec: kueue.WorkloadSpec{PodSets: wlPods}})

			clusterQueue := utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(string(tc.flavor)).
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).Obj()

			cache := schdcache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, clusterQueue); err != nil {
				t.Fatalf("Failed to add CQ to cache: %v", err)
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, topology := range topologies {
				cache.AddOrUpdateTopology(log, topology)
			}
			node := testingnode.MakeNode("tas-node").
				Label("rack", "rack-a").
				Label(corev1.LabelHostname, "tas-node").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourcePods: resource.MustParse("32"),
					corev1.ResourceCPU:  resource.MustParse("4"),
				}).
				Ready().
				Obj()
			cache.TASCache().SyncNode(node)
			if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort(clusterQueue.Spec.CohortName).Obj()); err != nil {
				t.Fatalf("Failed to create a cohort: %v", err)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(clusterQueue.Name))
			if cq == nil {
				t.Fatalf("Failed to create CQ snapshot")
			}

			flvAssigner := New(wlInfo, cq, resourceFlavors, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 0)
			assignment := flvAssigner.AssignFlavors(ctx, log, nil)

			status := assignment.PodSets[0].Status
			if got := status.IsFit(); got != tc.wantFit {
				t.Errorf("PodSet fit = %v, want %v (message: %q)", got, tc.wantFit, status.Message())
			}
			if tc.wantErrIn != "" && !strings.Contains(status.Message(), tc.wantErrIn) {
				t.Errorf("PodSet status message = %q, want it to contain %q", status.Message(), tc.wantErrIn)
			}
		})
	}
}

func TestWorkloadsTopologyRequests_RequiredTopologyRejectedForElasticWorkloadSlices(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
	features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, true)

	assignment := Assignment{
		PodSets: []PodSetAssignment{
			{Name: "leader", Flavors: ResourceAssignment{}, Count: 1, Status: *NewStatus()},
			{Name: "worker", Flavors: ResourceAssignment{"example.com/gpu": {Name: "tas", Mode: Fit, TriedFlavorIdx: -1}}, Count: 1, Status: *NewStatus()},
		},
	}
	wl := workload.NewInfo(log, &kueue.Workload{
		Annotations: map[string]string{
			"kueue.x-k8s.io/elastic-job": "true",
		},
		Spec: kueue.WorkloadSpec{
			PodSets: []kueue.PodSet{
				*utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
				*utiltestingapi.MakePodSet("worker", 1).
					Request("example.com/gpu", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					PodSetGroup("group1").
					Obj(),
			},
		},
	})
	cq := schdcache.ClusterQueueSnapshot{
		TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{
			"tas": {},
		},
		ResourceGroups: []resourcegroups.ResourceGroup{
			{
				CoveredResources: sets.New(corev1.ResourceName("example.com/gpu")),
				Flavors:          []kueue.ResourceFlavorReference{"tas"},
			},
		},
	}

	_ = assignment.WorkloadsTopologyRequests(testr.New(t), wl, &cq)

	if !errors.Is(assignment.PodSets[0].Status.err, ErrElasticRequiredTopologyNotSupported) {
		t.Fatalf("expected leader podSet error to be %v, got %v", ErrElasticRequiredTopologyNotSupported, assignment.PodSets[0].Status.err)
	}
}

func TestTasFlavorsOnly(t *testing.T) {
	tasFlavors := map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": {}}
	in := ResourceAssignment{
		"example.com/gpu":  {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
		corev1.ResourceCPU: {Name: "quota", Mode: Fit, TriedFlavorIdx: -1},
	}
	want := ResourceAssignment{
		"example.com/gpu": {Name: "tas", Mode: Fit, TriedFlavorIdx: -1},
	}
	got := tasFlavorsOnly(in, tasFlavors)
	if diff := cmp.Diff(want, got, cmpopts.IgnoreUnexported(FlavorAssignment{})); diff != "" {
		t.Errorf("tasFlavorsOnly() mismatch (-want,+got):\n%s", diff)
	}
}

// Two TAS flavors with one node each, addressed by hostname. Each node holds 4 CPU, so a
// request above that cannot be placed however much quota the ClusterQueue has.
func bookmarkTestFlavors() map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor {
	return map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"flavor-1": utiltestingapi.MakeResourceFlavor("flavor-1").
			NodeLabel("flavor", "one").TopologyName("topology-1").Obj(),
		"flavor-2": utiltestingapi.MakeResourceFlavor("flavor-2").
			NodeLabel("flavor", "two").TopologyName("topology-2").Obj(),
	}
}

func bookmarkTestNodes() []corev1.Node {
	return []corev1.Node{
		*testingnode.MakeNode("node-1").
			Label("flavor", "one").
			Label(corev1.LabelHostname, "node-1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("32"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("node-2").
			Label("flavor", "two").
			Label(corev1.LabelHostname, "node-2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("32"),
			}).
			Ready().
			Obj(),
	}
}

// newBookmarkSnapshot builds a snapshot with the two TAS flavors above, a ClusterQueue
// holding nominalPerFlavor on each of them, and a sibling ClusterQueue in the same cohort
// lending cohortSpare, so that a request beyond nominal is reachable by borrowing.
func newBookmarkSnapshot(
	ctx context.Context,
	t *testing.T,
	log logr.Logger,
	nominalPerFlavor, cohortSpare string,
	fungibility kueue.FlavorFungibility,
) *schdcache.ClusterQueueSnapshot {
	t.Helper()

	clusterQueue := utiltestingapi.MakeClusterQueue("cq").
		Cohort("cohort").
		FlavorFungibility(fungibility).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-1").Resource(corev1.ResourceCPU, nominalPerFlavor).Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-2").Resource(corev1.ResourceCPU, nominalPerFlavor).Obj(),
		).
		Obj()
	sibling := utiltestingapi.MakeClusterQueue("sibling").
		Cohort("cohort").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("flavor-1").Resource(corev1.ResourceCPU, cohortSpare).Obj(),
			*utiltestingapi.MakeFlavorQuotas("flavor-2").Resource(corev1.ResourceCPU, cohortSpare).Obj(),
		).
		Obj()

	cache := schdcache.New(utiltesting.NewFakeClient())
	for _, rf := range bookmarkTestFlavors() {
		cache.AddOrUpdateResourceFlavor(log, rf)
	}
	for _, topology := range []*kueue.Topology{
		utiltestingapi.MakeTopology("topology-1").Levels(corev1.LabelHostname).Obj(),
		utiltestingapi.MakeTopology("topology-2").Levels(corev1.LabelHostname).Obj(),
	} {
		cache.AddOrUpdateTopology(log, topology)
	}
	nodes := bookmarkTestNodes()
	for i := range nodes {
		cache.TASCache().SyncNode(&nodes[i])
	}
	if err := cache.AddClusterQueue(ctx, clusterQueue); err != nil {
		t.Fatalf("adding ClusterQueue: %v", err)
	}
	if err := cache.AddClusterQueue(ctx, sibling); err != nil {
		t.Fatalf("adding sibling ClusterQueue: %v", err)
	}

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("building snapshot: %v", err)
	}
	cqSnapshot := snapshot.ClusterQueue(kueue.ClusterQueueReference(clusterQueue.Name))
	if cqSnapshot == nil {
		t.Fatalf("ClusterQueue missing from snapshot")
	}
	return cqSnapshot
}

// bookmarkTestWorkload is a single pod requesting cpu on a required hostname topology.
func bookmarkTestWorkload(log logr.Logger, request string) *workload.Info {
	return workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "default").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, request).
			Obj()).
		Obj())
}

// nodeUsageOnFlavorOne consumes cpu on node-1 without charging the ClusterQueue's quota,
// as happens when another ClusterQueue shares the nodes through the same flavor.
func nodeUsageOnFlavorOne(cpu string) workload.TASUsage {
	return workload.TASUsage{
		"flavor-1": []workload.TopologyDomainRequests{{
			Values: []string{"node-1"},
			SinglePodRequests: resources.NewRequestsFromResourceList(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse(cpu),
			}),
			Count: 1,
		}},
	}
}

// bookmarkTestCycle is the scheduling cycle these tests assign in. Nomination and any
// in-cycle recomputation share it, matching the scheduler.
const bookmarkTestCycle int64 = 7

// lastTriedFlavorIdx reads the bookmark the assignment recorded for the first PodSet.
func lastTriedFlavorIdx(a Assignment, res corev1.ResourceName) (int, bool) {
	if len(a.FlavorScanState.LastTriedFlavorIndexes) == 0 {
		return 0, false
	}
	idx, ok := a.FlavorScanState.LastTriedFlavorIndexes[0][res]
	return idx, ok
}

// TestAssignFlavors_RecordsLastTriedFlavorIdx pins down what the cross-cycle flavor
// bookmark holds for each shape the flavor scan can take.
//
// LastTriedFlavorIdx is consumed as the starting index of the next scan
// (NextFlavorToTryForPodSetResource returns idx+1), so it only carries progress when it
// names a specific flavor. When the scan reaches the last flavor it is set to -1, which
// means "start over from the first flavor".
//
// The bookmark is written inside findFlavorForPodSets, so it is settled by the time
// AssignFlavors returns and nothing the topology pass does afterwards can change it. That
// is the point of the two "quota fits but ..." cases: quota accepts flavor-1 and records
// it, and the assignment is still requeued later carrying that usable bookmark because TAS
// went on to reject the flavor. What TAS then decides is asserted in TestAssignTopology.
func TestAssignFlavors_RecordsLastTriedFlavorIdx(t *testing.T) {
	cases := map[string]struct {
		// nominalPerFlavor is the ClusterQueue's nominal quota on each flavor.
		nominalPerFlavor string
		// cohortSpare is the sibling ClusterQueue's nominal quota, lendable to ours.
		cohortSpare string
		// request is what the Workload's single pod asks for.
		request string
		// clusterQueueUsage pre-consumes quota so the request cannot fit outright.
		// CPU amounts are milli-units, so 10 CPU is 10_000.
		clusterQueueUsage resources.FlavorResourceQuantities
		// nodeUsage pre-consumes capacity on node-1 without charging quota.
		nodeUsage workload.TASUsage
		// simulationResult lets a flavor report whether preemption could help.
		simulationResult map[resources.FlavorResource]simulationResultForFlavor
		fungibility      kueue.FlavorFungibility

		// wantMode is the mode as the quota scan leaves it, which is not always the mode
		// the workload ends the cycle in.
		wantMode           FlavorAssignmentMode
		wantTriedFlavorIdx int
	}{
		"quota and topology both fit on the first flavor: bookmark names it": {
			nominalPerFlavor: "10",
			cohortSpare:      "0",
			request:          "2",
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			wantMode:           Fit,
			wantTriedFlavorIdx: 0,
		},
		"quota fits but the pod is larger than any node: bookmark still names the first flavor": {
			nominalPerFlavor: "10",
			cohortSpare:      "0",
			request:          "6",
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			// Quota alone is happy: 6 CPU is well within the nominal 10, so the scan stops
			// on flavor-1 at Fit and records it. No node has 6 CPU, so TAS will later
			// demote this all the way to NoFit - see TestAssignTopology - but by then the
			// bookmark has been written and still points at flavor-1.
			wantMode:           Fit,
			wantTriedFlavorIdx: 0,
		},
		"quota fits but the topology is fragmented: bookmark still names the first flavor": {
			nominalPerFlavor: "10",
			cohortSpare:      "0",
			// The pod would fit node-1 on its own, so this is fragmentation rather than a
			// pod that is structurally too large: 2 of the node's 4 CPU are already taken,
			// leaving too little for a required-topology request of 3.
			request:   "3",
			nodeUsage: nodeUsageOnFlavorOne("2"),
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			// nodeUsage consumes node capacity without charging quota, so the quota scan
			// is unaware of the fragmentation and stops on flavor-1 at Fit. TAS later
			// demotes it to Preempt, the shape reported in #13658, and the bookmark
			// written here still points at flavor-1.
			wantMode:           Fit,
			wantTriedFlavorIdx: 0,
		},
		"fits only by borrowing: bookmark says start over": {
			nominalPerFlavor: "1",
			cohortSpare:      "20",
			request:          "2",
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.TryNextFlavor,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			// Borrowing is not optimal and WhenCanBorrow is TryNextFlavor, so the scan
			// runs past flavor-1 and reaches the last flavor.
			wantMode:           Fit,
			wantTriedFlavorIdx: -1,
		},
		"fits only by borrowing, but WhenCanBorrow is MayStopSearch: bookmark names the first flavor": {
			nominalPerFlavor: "1",
			cohortSpare:      "20",
			request:          "2",
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.MayStopSearch,
				WhenCanPreempt: kueue.TryNextFlavor,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			wantMode:           Fit,
			wantTriedFlavorIdx: 0,
		},
		"needs preemption and candidates exist: bookmark names the first flavor": {
			nominalPerFlavor: "2",
			cohortSpare:      "0",
			request:          "2",
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "flavor-1", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				{Flavor: "flavor-2", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "flavor-1", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 0},
				{Flavor: "flavor-2", Resource: corev1.ResourceCPU}: {preemptioncommon.Preempt, 0},
			},
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.MayStopSearch,
				WhenCanPreempt: kueue.MayStopSearch,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			wantMode:           Preempt,
			wantTriedFlavorIdx: 0,
		},
		"needs preemption but no candidates: bookmark says start over whatever the policy": {
			nominalPerFlavor: "2",
			cohortSpare:      "0",
			request:          "2",
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "flavor-1", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				{Flavor: "flavor-2", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "flavor-1", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
				{Flavor: "flavor-2", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
			},
			// Both knobs are the conservative setting, yet shouldTryNextFlavor returns
			// true for noPreemptionCandidates before either policy is consulted, so no
			// fungibility configuration can stop the scan here.
			fungibility: kueue.FlavorFungibility{
				WhenCanBorrow:  kueue.MayStopSearch,
				WhenCanPreempt: kueue.MayStopSearch,
				Preference:     new(kueue.PreemptionOverBorrowing),
			},
			wantMode:           Preempt,
			wantTriedFlavorIdx: -1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.FlavorFungibility, true)
			ctx, log := utiltesting.ContextWithLog(t)

			cqSnapshot := newBookmarkSnapshot(ctx, t, log, tc.nominalPerFlavor, tc.cohortSpare, tc.fungibility)
			if tc.clusterQueueUsage != nil {
				cqSnapshot.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.clusterQueueUsage}})
			}
			if tc.nodeUsage != nil {
				cqSnapshot.AddUsage(workload.Usage{TAS: tc.nodeUsage})
			}

			wlInfo := bookmarkTestWorkload(log, tc.request)
			assigner := New(wlInfo, cqSnapshot, bookmarkTestFlavors(), false,
				&testOracle{simulationResult: tc.simulationResult}, nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), bookmarkTestCycle)
			assignment := assigner.AssignFlavors(ctx, log, nil)

			if gotMode := assignment.RepresentativeMode(); gotMode != tc.wantMode {
				t.Errorf("RepresentativeMode() = %s, want %s", gotMode, tc.wantMode)
			}
			got, ok := lastTriedFlavorIdx(assignment, corev1.ResourceCPU)
			if !ok {
				t.Fatalf("no bookmark recorded for cpu; mode was %s", assignment.RepresentativeMode())
			}
			if got != tc.wantTriedFlavorIdx {
				t.Errorf("LastTriedFlavorIdx for cpu = %d, want %d", got, tc.wantTriedFlavorIdx)
			}
		})
	}
}

// TestRecomputeRecordsLastTriedFlavorIdx covers the case where quota and topology both fit
// at nomination and the placement is invalidated later in the same cycle, which is what
// triggers the in-cycle recomputation.
//
// This suite deliberately chains both flavor and topology assignment stages
// instead of performing them in isolation.
//
// The recomputation replays flavor assignment with NominationMapping populated so that the
// nominated flavor is kept, and it is the recomputed assignment that the scheduler stores.
// Whatever bookmark that second pass records is therefore the one the next cycle inherits.
//
// Which bookmark that is depends on where the replayed scan stops. If quota still accepts
// the nominated flavor the scan breaks on it and the bookmark names it. If quota has since
// tightened, the scan carries on to the remaining flavors, which the nomination mapping
// skips one by one, and reaches the last flavor - recording "start over" instead. Both
// report the same newMode: Preempt, so a log line alone cannot tell them apart.
func TestRecomputeRecordsLastTriedFlavorIdx(t *testing.T) {
	fungibility := kueue.FlavorFungibility{
		WhenCanBorrow:  kueue.TryNextFlavor,
		WhenCanPreempt: kueue.TryNextFlavor,
		Preference:     new(kueue.PreemptionOverBorrowing),
	}

	cases := map[string]struct {
		// quotaUsageAtRecompute tightens quota before the assignment is replayed.
		quotaUsageAtRecompute resources.FlavorResourceQuantities
		// simulationResult applies to the replayed assignment.
		simulationResult map[resources.FlavorResource]simulationResultForFlavor

		wantRecomputedIdx int
	}{
		"quota still accepts the nominated flavor: bookmark keeps naming it": {
			wantRecomputedIdx: 0,
		},
		"quota tightened as well: bookmark says start over": {
			quotaUsageAtRecompute: resources.FlavorResourceQuantities{
				{Flavor: "flavor-1", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
				{Flavor: "flavor-2", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "flavor-1", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
				{Flavor: "flavor-2", Resource: corev1.ResourceCPU}: {preemptioncommon.NoCandidates, 0},
			},
			// The replayed scan no longer breaks on flavor-1, so it walks on to flavor-2,
			// which the nomination mapping skips, and ends on the last flavor.
			wantRecomputedIdx: -1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.TASRecomputeAssignmentWithinSchedulingCycle, true)
			features.SetFeatureGateDuringTest(t, features.RecomputeAssignmentUponPreemptionTargetsOverlap, false)
			features.SetFeatureGateDuringTest(t, features.FlavorFungibility, true)
			ctx, log := utiltesting.ContextWithLog(t)

			// Quota is generous at nomination, so both flavors are accepted on quota
			// grounds and the pod fits an empty node.
			cqSnapshot := newBookmarkSnapshot(ctx, t, log, "10", "0", fungibility)
			wlInfo := bookmarkTestWorkload(log, "3")
			flavors := bookmarkTestFlavors()

			// Nomination: quota fits, topology fits, and a placement is produced.
			assigner := New(wlInfo, cqSnapshot, flavors, false, &testOracle{}, nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), bookmarkTestCycle)
			// The placement invalidated further down has to
			// be one a real nomination produced, because the whole point is what the
			// second, replayed pass records after the first pass's result goes stale.
			nominated := assigner.AssignFlavors(ctx, log, nil)
			assigner.AssignTopology(ctx, log, &nominated)
			if got := nominated.RepresentativeMode(); got != Fit {
				t.Fatalf("nomination RepresentativeMode() = %s, want %s", got, Fit)
			}
			if nominated.PodSets[0].TopologyAssignment == nil {
				t.Fatal("nomination produced no TopologyAssignment, so there is no placement to invalidate")
			}
			nominatedIdx, ok := lastTriedFlavorIdx(nominated, corev1.ResourceCPU)
			if !ok {
				t.Fatal("nomination recorded no bookmark for cpu")
			}
			if nominatedIdx != 0 {
				t.Errorf("nomination LastTriedFlavorIdx = %d, want 0", nominatedIdx)
			}

			// Another Workload admitted later in the same cycle takes most of node-1, so
			// the placement chosen at nomination no longer fits.
			cqSnapshot.AddUsage(workload.Usage{TAS: nodeUsageOnFlavorOne("2")})
			if tc.quotaUsageAtRecompute != nil {
				cqSnapshot.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: tc.quotaUsageAtRecompute}})
			}

			// The scheduler clears FlavorScanState and pins the nominated flavors before
			// replaying the assignment, so that the recomputation stays on the flavor
			// quota was computed for.
			wlInfo.FlavorScanState = nil
			mapping := workload.PodSetResourcesToFlavors{}
			for _, psa := range nominated.PodSets {
				perResource := workload.ResourceToFlavor{}
				for res, fa := range psa.Flavors {
					perResource[res] = fa.Name
				}
				mapping[psa.Name] = perResource
			}
			wlInfo.NominationMapping = mapping

			assigner = New(wlInfo, cqSnapshot, flavors, false,
				&testOracle{simulationResult: tc.simulationResult}, nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), bookmarkTestCycle)
			recomputed := assigner.AssignFlavors(ctx, log, nil)
			if recomputed.RepresentativeMode() != NoFit {
				assigner.AssignTopology(ctx, log, &recomputed)
			}

			recomputedIdx, ok := lastTriedFlavorIdx(recomputed, corev1.ResourceCPU)
			if !ok {
				t.Fatalf("recomputation recorded no bookmark for cpu; mode was %s", recomputed.RepresentativeMode())
			}
			if recomputedIdx != tc.wantRecomputedIdx {
				t.Errorf("recomputation LastTriedFlavorIdx = %d, want %d", recomputedIdx, tc.wantRecomputedIdx)
			}
			// Both shapes report the same mode, which is why a mode log line alone cannot
			// distinguish them.
			if got := recomputed.RepresentativeMode(); got != Preempt {
				t.Errorf("recomputation RepresentativeMode() = %s, want %s", got, Preempt)
			}
		})
	}
}

// TestAssignTopology exercises FlavorAssigner.AssignTopology on its own. Each case builds
// the Assignment that a previous AssignFlavors pass would have produced, by hand, so the
// entry state is pinned exactly and no quota scan runs.
//
// Note that the inputs of the test cases drop details that do not affect the logic
// of AssignTopology. This means the proposed test case inputs can look similar
// yet expect the prior AssignFLavor to return a different RepresentativeMode
// (e.g. Fit vs Preempt). This would be possible via the existence of other
// nodes with pods reserving quota, affecting the free space available
// on the CQ and thus the initial assignment outcome.
func TestAssignTopology(t *testing.T) {
	// fixture is the entry state a case hands to the runner. cq is kept so the runner can
	// check that the pass left the shared snapshot's capacity as it found it.
	type fixture struct {
		assigner   *FlavorAssigner
		assignment *Assignment
		cq         *schdcache.ClusterQueueSnapshot
	}

	// newFixture builds the entry state for a single bookmarkTestWorkload pod placed on
	// flavor-1 in the given mode. otherUsage is cpu already consumed on node-1 from outside
	// this ClusterQueue's quota, which is what makes "fits on an empty cluster but not on
	// this one" reachable.
	newFixture := func(ctx context.Context, t *testing.T, log logr.Logger, mode FlavorAssignmentMode, request, otherUsage string) fixture {
		t.Helper()
		cq := newBookmarkSnapshot(ctx, t, log, "10", "0", kueue.FlavorFungibility{})
		if otherUsage != "" {
			cq.AddUsage(workload.Usage{TAS: nodeUsageOnFlavorOne(otherUsage)})
		}
		wlInfo := bookmarkTestWorkload(log, request)
		ps := PodSetAssignment{
			Name:     kueue.DefaultPodSetName,
			Count:    1,
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(request)},
			Flavors: ResourceAssignment{
				corev1.ResourceCPU: &FlavorAssignment{Name: "flavor-1", Mode: mode},
			},
			// Pre-seeded because markFlavorAttempt only updates an attempt that already
			// exists; it never appends one.
			FlavorAssignmentAttempts: []FlavorAssignmentAttempt{{Flavor: "flavor-1", Mode: mode}},
		}
		if mode != Fit {
			// PodSetAssignment.RepresentativeMode reports Fit whenever the Status carries
			// no reasons, whatever the flavor modes say, so a non-Fit entry needs one.
			ps.Status = *NewStatus("insufficient unused quota")
		}
		return fixture{
			assigner: New(wlInfo, cq, bookmarkTestFlavors(), false, &testOracle{}, nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), bookmarkTestCycle),
			assignment: &Assignment{PodSets: []PodSetAssignment{ps}},
			cq:         cq,
		}
	}

	// newElasticFixture builds the entry state for an elastic workload slice replacing an
	// admitted 2-pod slice with a 4-pod one. The old slice already occupies node-1, so the
	// replacement only fits because AssignTopology simulates the old slice's usage away
	// first. otherUsage is cpu consumed on node-1 by an unrelated workload, which is not
	// simulated away and therefore decides whether the replacement still fits.
	newElasticFixture := func(ctx context.Context, t *testing.T, log logr.Logger, otherUsage string) fixture {
		t.Helper()
		features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, true)

		cq := newBookmarkSnapshot(ctx, t, log, "10", "0", kueue.FlavorFungibility{})
		topologyAssignment := utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
			Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 2).Obj()).
			Obj()
		psAssignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Count(2).
			Assignment(corev1.ResourceCPU, "flavor-1", "2").
			TopologyAssignment(topologyAssignment).
			Obj()
		admission := utiltestingapi.MakeAdmission("cq").PodSets(psAssignment).Obj()
		old := utiltestingapi.MakeWorkload("old", "default").
			Annotation(constants.ElasticJobAnnotation, "true").
			PodSets(
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			).
			ReserveQuotaAt(admission, time.Now()).
			AdmittedAt(true, time.Now()).
			Obj()
		oldInfo := workload.NewInfo(log, old)
		cq.AddUsage(oldInfo.Usage())
		cq.AddUsage(workload.Usage{TAS: nodeUsageOnFlavorOne(otherUsage)})

		next := workload.NewInfo(
			log,
			utiltestingapi.MakeWorkload("new", "default").
				Annotation(constants.ElasticJobAnnotation, "true").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).
				Obj(),
		)
		ps := PodSetAssignment{
			Name:     kueue.DefaultPodSetName,
			Count:    4,
			Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			Flavors: ResourceAssignment{
				corev1.ResourceCPU: &FlavorAssignment{Name: "flavor-1", Mode: Fit},
			},
			FlavorAssignmentAttempts: []FlavorAssignmentAttempt{{Flavor: "flavor-1", Mode: Fit}},
		}
		return fixture{
			assigner: New(next, cq, bookmarkTestFlavors(), false, &testOracle{}, oldInfo,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), bookmarkTestCycle),
			assignment: &Assignment{PodSets: []PodSetAssignment{ps}},
			cq:         cq,
		}
	}

	cases := map[string]struct {
		setup    func(ctx context.Context, t *testing.T, log logr.Logger) fixture
		wantMode FlavorAssignmentMode
		// wantPlan is whether nomination produced a TopologyAssignment. Only an
		// assignment that carries a plan can later be invalidated mid-cycle, which is
		// what the in-cycle recomputation is triggered by.
		wantPlan bool
		// wantAttemptReason, when non-empty, is the NoFitReason expected on the flavor-1 attempt.
		wantAttemptReason string
		// wantStatusErrMsg, when non-empty, is the expected pod set Status message, which is
		// how an error raised while building the topology requests surfaces.
		wantStatusErrMsg string
	}{
		"a fitting assignment receives a topology assignment": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newFixture(ctx, t, log, Fit, "1", "")
			},
			wantMode: Fit,
			wantPlan: true,
		},
		// node-1 has 4 cpu with 3 already taken, so a 2 cpu pod cannot be placed now but
		// would fit on an empty node. The Fit branch demotes to Preempt and the Preempt
		// branch then succeeds, leaving it there.
		"a fitting assignment that no node can host right now is demoted to Preempt": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newFixture(ctx, t, log, Fit, "2", "3")
			},
			wantMode: Preempt,
			wantPlan: false,
		},
		"an assignment needing preemption stays Preempt when an empty cluster could host it": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newFixture(ctx, t, log, Preempt, "2", "3")
			},
			wantMode: Preempt,
			wantPlan: false,
		},
		// 5 cpu exceeds node-1's 4 cpu outright, so no amount of preemption helps.
		"an assignment needing preemption becomes NoFit when even an empty cluster is too small": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newFixture(ctx, t, log, Preempt, "5", "")
			},
			wantMode:          NoFit,
			wantPlan:          false,
			wantAttemptReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
		// The two branches are sequential, not exclusive: the demotion the first one
		// performs is what makes the second one run. A pod set that quota says fits but
		// that no node can ever host therefore has to fall all the way through to NoFit
		// in a single pass.
		"a fitting assignment no cluster could ever host falls through to NoFit": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newFixture(ctx, t, log, Fit, "5", "")
			},
			wantMode:          NoFit,
			wantPlan:          false,
			wantAttemptReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
		// Verifies that the !HasUnhealthyNodes check prevents demoting an
		// unhealthy node replacement from Preempt to NoFit.
		// - The workload must be admitted because TAS node replacement reads existing
		//   placement from wl.Status.Admission.
		// - We request 5 CPU against node-1's 4 CPU to ensure placement fails.
		//   In this test fixture, node-1 is still marked Ready in the TAS snapshot
		//   (only wl.Status.UnhealthyNodes was set), so a smaller request would
		//   just be re-placed on node-1 (leading to Fit), which would obscure
		//   a missing !HasUnhealthyNodes check.
		"a replacement for an unhealthy node is not demoted": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				cq := newBookmarkSnapshot(ctx, t, log, "10", "0", kueue.FlavorFungibility{})
				plan := utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 1).Obj()).
					Obj()
				wl := utiltestingapi.MakeWorkload("wl", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, "5").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(
						utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Count(1).
							Assignment(corev1.ResourceCPU, "flavor-1", "5").
							TopologyAssignment(plan).
							Obj()).Obj(), time.Now()).
					AdmittedAt(true, time.Now()).
					Obj()
				wl.Status.UnhealthyNodes = []kueue.UnhealthyNode{{Name: "node-1"}}
				wlInfo := workload.NewInfo(log, wl)
				ps := PodSetAssignment{
					Name:     kueue.DefaultPodSetName,
					Count:    1,
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5")},
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: &FlavorAssignment{Name: "flavor-1", Mode: Preempt},
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{{Flavor: "flavor-1", Mode: Preempt}},
					Status:                   *NewStatus("insufficient unused quota"),
					// Non-nil so WorkloadsTopologyRequests re-queues this pod set instead
					// of treating it as already placed.
					TopologyAssignment: tas.InternalFrom(plan),
				}
				return fixture{
					assigner: New(wlInfo, cq, bookmarkTestFlavors(), false, &testOracle{}, nil,
						configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), bookmarkTestCycle),
					assignment: &Assignment{PodSets: []PodSetAssignment{ps}},
				}
			},
			wantMode: Preempt,
			wantPlan: true,
		},
		// An elastic slice replaces its predecessor, so the predecessor's usage has to be
		// simulated away before the replacement is placed. Without that, the old slice's
		// 2 cpu on node-1 would be counted against the replacement that is superseding it,
		// and a 4-pod replacement could never fit a 4 cpu node.
		"an elastic replacement is placed as if its predecessor had already released the node": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newElasticFixture(ctx, t, log, "0")
			},
			wantMode: Fit,
			wantPlan: true,
		},
		// Only the predecessor's own usage is simulated away. 1 cpu held by an unrelated
		// workload stays, leaving 3 of node-1's 4 cpu for a replacement that needs 4.
		"an elastic replacement is demoted when an unrelated workload holds the difference": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newElasticFixture(ctx, t, log, "1")
			},
			wantMode: Preempt,
			wantPlan: false,
		},
		// Topology is a property of a flavor, so a pod set whose resources landed on two
		// different TAS flavors has no single topology to be placed in.
		"a pod set split across two TAS flavors is rejected": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				f := newFixture(ctx, t, log, Fit, "1", "")
				f.assignment.PodSets[0].Flavors[corev1.ResourceMemory] =
					&FlavorAssignment{Name: "flavor-2", Mode: Fit}
				return f
			},
			wantMode: NoFit,
			wantPlan: false,
			wantStatusErrMsg: (&MultipleTASFlavorsAssignedError{
				Flavors: []kueue.ResourceFlavorReference{"flavor-1", "flavor-2"},
			}).Error(),
		},
		"a pod set left on no TAS flavor is rejected": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				f := newFixture(ctx, t, log, Fit, "1", "")
				f.assignment.PodSets[0].Flavors = ResourceAssignment{}
				return f
			},
			wantMode:         NoFit,
			wantPlan:         false,
			wantStatusErrMsg: ErrNoTASFlavorAssigned.Error(),
		},
		"a pod set is rejected when the ClusterQueue has no TAS flavors": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				ps := PodSetAssignment{
					Name:     kueue.DefaultPodSetName,
					Count:    1,
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: &FlavorAssignment{Name: "non-tas", Mode: Fit},
					},
				}
				return fixture{
					assigner: New(bookmarkTestWorkload(log, "1"), &schdcache.ClusterQueueSnapshot{},
						bookmarkTestFlavors(), false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared,
						resources.NewResourceFormatter(), bookmarkTestCycle),
					assignment: &Assignment{PodSets: []PodSetAssignment{ps}},
				}
			},
			wantMode:         NoFit,
			wantPlan:         false,
			wantStatusErrMsg: ErrNoTASCacheInformation.Error(),
		},
		// Neither branch matches NoFit. The scheduler skips the call entirely in this
		// situation, so the method only has to leave the assignment alone.
		"an assignment that is already NoFit is left untouched": {
			setup: func(ctx context.Context, t *testing.T, log logr.Logger) fixture {
				return newFixture(ctx, t, log, NoFit, "1", "")
			},
			wantMode: NoFit,
			wantPlan: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)
			f := tc.setup(ctx, t, log)

			// The snapshot is shared with every other Workload in the cycle, so whatever
			// AssignTopology does to it while searching has to be undone.
			var before string
			if f.cq != nil && f.cq.TASFlavors["flavor-1"] != nil {
				var err error
				if before, err = f.cq.TASFlavors["flavor-1"].SerializeFreeCapacityPerDomain(); err != nil {
					t.Fatalf("reading free capacity before the pass: %v", err)
				}
			}

			f.assigner.AssignTopology(ctx, log, f.assignment)

			if before != "" {
				after, err := f.cq.TASFlavors["flavor-1"].SerializeFreeCapacityPerDomain()
				if err != nil {
					t.Fatalf("reading free capacity after the pass: %v", err)
				}
				if before != after {
					t.Errorf("shared snapshot capacity changed: before=%s after=%s", before, after)
				}
			}

			if got := f.assignment.RepresentativeMode(); got != tc.wantMode {
				t.Errorf("RepresentativeMode() = %s, want %s", got, tc.wantMode)
			}
			if got := f.assignment.PodSets[0].TopologyAssignment != nil; got != tc.wantPlan {
				t.Errorf("has TopologyAssignment = %t, want %t", got, tc.wantPlan)
			}
			if tc.wantAttemptReason != "" {
				attemptRecorded := false
				for _, att := range f.assignment.PodSets[0].FlavorAssignmentAttempts {
					if att.Flavor == "flavor-1" {
						attemptRecorded = true
						if att.NoFitReason != tc.wantAttemptReason {
							t.Errorf("flavor-1 attempt NoFitReason = %q, want %q", att.NoFitReason, tc.wantAttemptReason)
						}
						break
					}
				}
				if !attemptRecorded {
					t.Error("Expeccted failed attempt for flavor-1, but none was recorded.")
				}
			}
			if tc.wantStatusErrMsg != "" {
				if got := f.assignment.PodSets[0].Status.Message(); got != tc.wantStatusErrMsg {
					t.Errorf("pod set Status = %q, want %q", got, tc.wantStatusErrMsg)
				}
			}
		})
	}
}

// TestResolveNoFitReason exercises Assignment.ResolveNoFitReason on its own. Every case
// starts from a hand-built Assignment, so no quota scan and no topology pass runs and the
// only behaviour under test is the reason aggregation.
//
// The aggregation combines reasons in two opposite directions, which is the part that is
// easy to get backwards:
//
//   - Flavors within one resource group are alternatives, so the *least* severe blocker
//     wins. If one flavor is merely waiting for quota, the group is not permanently blocked.
//   - Resource groups are co-requisites, so across groups the *most* severe blocker wins.
//     Every group has to be satisfiable for the pod set to fit.
//     The same "most severe" rule then applies across pod sets.
func TestResolveNoFitReason(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cqWithGroups := func(groups ...[]kueue.ResourceFlavorReference) *schdcache.ClusterQueueSnapshot {
		cache := schdcache.New(utiltesting.NewFakeClient())
		cqBuilder := utiltestingapi.MakeClusterQueue("cq")
		for i, flavors := range groups {
			res := corev1.ResourceName(fmt.Sprintf("res-%d", i))
			flavorQuotas := make([]kueue.FlavorQuotas, 0, len(flavors))
			for _, f := range flavors {
				cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(string(f)).Obj())
				flavorQuotas = append(flavorQuotas, *utiltestingapi.MakeFlavorQuotas(string(f)).Resource(res, "1").Obj())
			}
			cqBuilder.ResourceGroup(flavorQuotas...)
		}
		if err := cache.AddClusterQueue(ctx, cqBuilder.Obj()); err != nil {
			t.Fatalf("adding ClusterQueue: %v", err)
		}
		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("building snapshot: %v", err)
		}
		return snapshot.ClusterQueue("cq")
	}
	noFitAttempt := func(flavor kueue.ResourceFlavorReference, reason string) FlavorAssignmentAttempt {
		return FlavorAssignmentAttempt{Flavor: flavor, Mode: NoFit, NoFitReason: reason}
	}
	// noFitPodSet reports NoFit because its Status carries a reason and it has no assigned
	// flavors. See PodSetAssignment.RepresentativeMode: a Status without reasons would make
	// the pod set report Fit no matter what the attempts say.
	noFitPodSet := func(name kueue.PodSetReference, attempts ...FlavorAssignmentAttempt) PodSetAssignment {
		return PodSetAssignment{
			Name:                     name,
			Status:                   *NewStatus("no fit"),
			FlavorAssignmentAttempts: attempts,
		}
	}
	withStatusReason := func(ps PodSetAssignment, reason string) PodSetAssignment {
		ps.Status.noFitReason = reason
		return ps
	}

	cases := map[string]struct {
		assignment Assignment
		cq         *schdcache.ClusterQueueSnapshot
		want       string
	}{
		"an assignment that is not NoFit is left untouched": {
			assignment: Assignment{
				PodSets:     []PodSetAssignment{{Name: "main"}},
				NoFitReason: "untouched",
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want: "untouched",
		},
		"an assignment with no pod sets resolves to no reason": {
			assignment: Assignment{NoFitReason: "stale"},
			cq:         cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want:       "",
		},
		"pod sets that are not NoFit are skipped": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name:                     "fits",
						FlavorAssignmentAttempts: []FlavorAssignmentAttempt{noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonNoMatchingFlavor)},
					},
					noFitPodSet("blocked", noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonWaitingForQuota)),
				},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		},
		"a pod set with no recorded attempts falls back to NoMatchingFlavor": {
			assignment: Assignment{PodSets: []PodSetAssignment{noFitPodSet("main")}},
			cq:         cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want:       kueue.WorkloadQuotaReservedReasonNoMatchingFlavor,
		},
		"within a resource group the least severe blocker wins": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{noFitPodSet("main",
					noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonExceedsMaxQuota),
					noFitAttempt("flavor-b", kueue.WorkloadQuotaReservedReasonWaitingForQuota),
				)},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a", "flavor-b"}),
			want: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		},
		// The same two reasons in separate groups invert the outcome, because now both
		// groups have to be satisfied.
		"across resource groups the most severe blocker wins": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{noFitPodSet("main",
					noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonWaitingForQuota),
					noFitAttempt("flavor-b", kueue.WorkloadQuotaReservedReasonExceedsMaxQuota),
				)},
			},
			cq: cqWithGroups(
				[]kueue.ResourceFlavorReference{"flavor-a"},
				[]kueue.ResourceFlavorReference{"flavor-b"},
			),
			want: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
		"a flavor in no resource group forms its own group": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{noFitPodSet("main",
					noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonWaitingForQuota),
					noFitAttempt("deleted-flavor", kueue.WorkloadQuotaReservedReasonExceedsMaxQuota),
				)},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
		"attempts that are not NoFit are ignored": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{noFitPodSet("main",
					FlavorAssignmentAttempt{Flavor: "flavor-a", Mode: Preempt, NoFitReason: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed},
					noFitAttempt("flavor-b", kueue.WorkloadQuotaReservedReasonWaitingForQuota),
				)},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a", "flavor-b"}),
			want: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
		},
		"the pod set status reason seeds the aggregation": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{withStatusReason(
					noFitPodSet("main", noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonWaitingForQuota)),
					kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
				)},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
		"the most severe reason across pod sets wins": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{
					noFitPodSet("blocked", noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonExceedsMaxQuota)),
					noFitPodSet("waiting", noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonWaitingForQuota)),
				},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
		},
		"a topology demotion resolves to TopologyPlacementFailed": {
			assignment: Assignment{
				PodSets: []PodSetAssignment{noFitPodSet("main",
					noFitAttempt("flavor-a", kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed),
				)},
			},
			cq:   cqWithGroups([]kueue.ResourceFlavorReference{"flavor-a"}),
			want: kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			assignment := tc.assignment
			assignment.ResolveNoFitReason(tc.cq)
			if diff := cmp.Diff(tc.want, assignment.NoFitReason); diff != "" {
				t.Errorf("unexpected NoFitReason (-want,+got):\n%s", diff)
			}
		})
	}
}
