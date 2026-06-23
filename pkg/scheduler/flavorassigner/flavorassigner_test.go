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
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
	log logr.Logger,
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Mi),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "tainted", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "taint_and_toleration", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor default, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:      resources.NewAmount(3_000),
					{Flavor: "b_one", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(9_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(5_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(5),
					{Flavor: "two", Resource: "example.com/gpu"}:     resources.NewAmount(4),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(3_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
					{Flavor: "b_one", Resource: "example.com/gpu"}:   resources.NewAmount(3),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for memory in flavor two, 5Mi more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(3_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(10 * utiltesting.Mi),
					{Flavor: "b_one", Resource: "example.com/gpu"}:   resources.NewAmount(3),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Mi),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "label-x-a", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(5_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}:    resources.NewAmount(10_000),
					{Flavor: "default", Resource: corev1.ResourceMemory}: resources.NewAmount(5 * utiltesting.Gi),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor two, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
								PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
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
								PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
								Reasons:               []string{"insufficient unused quota for cpu in flavor tainted, 3 more needed"},
							},
						},
						Count: 10,
					},
				},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:     resources.NewAmount(2_000),
					{Flavor: "tainted", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
					{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(0),
				}},
			},
			wantRepMode: Fit,
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(3),
					{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(3_000),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(3),
					{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(3_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: resources.NewAmount(5),
					{Flavor: "default", Resource: corev1.ResourceCPU}:  resources.NewAmount(5_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: "pods"}: resources.NewAmount(1),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: "pods"}: resources.NewAmount(1),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
						{Flavor: "two", Mode: Fit},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "two", Resource: "pods"}: resources.NewAmount(1),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "two", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: "pods"}: resources.NewAmount(1),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "two", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.NoCandidates),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
						},
						{Flavor: "two", Mode: Fit, Borrow: 1},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.NoCandidates),
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
						},
						{Flavor: "two", Mode: Fit, Borrow: 1},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
						},
					},
					Count: 1,
				}},
				Borrowing: 1,
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  resources.NewAmount(9_000),
					{Flavor: "one", Resource: corev1.ResourcePods}: resources.NewAmount(1),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
							PreemptionPossibility: ptr.To(preemptioncommon.Preempt),
							Borrow:                1,
							Reasons:               []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
						},
					},
					Count: 1,
				}},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: resources.NewAmount(12_000),
				}},
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
						Requests: resources.Requests{
							corev1.ResourceCPU:    2000,
							corev1.ResourceMemory: 10 * utiltesting.Mi,
						},
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "two", Resource: corev1.ResourceMemory}: resources.NewAmount(0),
				}},
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
						Requests: resources.Requests{
							corev1.ResourceCPU:    2000,
							corev1.ResourceMemory: 10 * utiltesting.Mi,
						},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
				NoFitReason: "ExceedsMaxQuota",
			},
		},
		"multiple TAS flavors assigned to different resources in the same PodSet leads to NoFit": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
			},
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("tas-topo-a").Levels(corev1.LabelHostname).Obj(),
				utiltestingapi.MakeTopology("tas-topo-b").Levels(corev1.LabelHostname).Obj(),
			},
			wlPods: []kueue.PodSet{
				*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj(),
			},
			clusterQueue: *utiltestingapi.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-a").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-b").
						Resource(corev1.ResourceMemory, "10Mi").
						Obj(),
				).
				Obj(),
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: kueue.DefaultPodSetName,
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "tas-a", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "tas-b", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 1,
						Status: Status{
							err: &MultipleTASFlavorsAssignedError{Flavors: []kueue.ResourceFlavorReference{"tas-a", "tas-b"}},
						},
					},
				},
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "tas-a", Resource: corev1.ResourceCPU}:    resources.NewAmount(1_000),
					{Flavor: "tas-b", Resource: corev1.ResourceMemory}: resources.NewAmount(utiltesting.Mi),
				}},
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
					Quota: resources.FlavorResourceQuantities{
						{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(1000),
					},
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
				wlInfo := workload.NewInfo(&kueue.Workload{
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
					clusterQueue.AddUsage(workload.Usage{Quota: tc.clusterQueueUsage})
				}

				if tc.secondaryClusterQueue != nil {
					secondaryClusterQueue := snapshot.ClusterQueue(kueue.ClusterQueueReference(tc.secondaryClusterQueue.Name))
					if secondaryClusterQueue == nil {
						t.Fatalf("Failed to create secondary CQ snapshot")
					}
					secondaryClusterQueue.AddUsage(workload.Usage{Quota: tc.secondaryClusterQueueUsage})
				}

				flvAssigner := New(
					wlInfo,
					clusterQueue,
					resourceFlavors,
					tc.enableFairSharing,
					&testOracle{simulationResult: tc.simulationResult},
					tc.preemptWorkloadSlice,
					configapi.QuotaCheckBlockUndeclared,
				)
				assignment := flvAssigner.Assign(log, nil)
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
						statusComparer, cmpopts.IgnoreFields(Assignment{}, "LastState"),
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
func TestReclaimBeforePriorityPreemption(t *testing.T) {
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

			wlInfo := workload.NewInfo(&kueue.Workload{
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
			log := testr.NewWithOptions(t, testr.Options{Verbosity: 2})
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			otherClusterQueue := snapshot.ClusterQueue("other-clusterqueue")
			otherClusterQueue.AddUsage(workload.Usage{Quota: tc.otherClusterQueueUsage})

			testClusterQueue := snapshot.ClusterQueue("test-clusterqueue")
			testClusterQueue.AddUsage(workload.Usage{Quota: tc.testClusterQueueUsage})

			flvAssigner := New(wlInfo, testClusterQueue, resourceFlavors, false, &testOracle{tc.simulationResult}, nil, configapi.QuotaCheckBlockUndeclared)
			assignment := flvAssigner.Assign(log, nil)
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
func TestDeletedFlavors(t *testing.T) {
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
				Usage: workload.Usage{Quota: resources.FlavorResourceQuantities{
					{Flavor: "flavor", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				}},
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
				Usage:       workload.Usage{Quota: resources.FlavorResourceQuantities{}},
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
				wlInfo := workload.NewInfo(&kueue.Workload{
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

				flvAssigner := New(wlInfo, clusterQueue, flavorMap, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared)

				assignment := flvAssigner.Assign(log, nil)
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
						cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{}), statusComparer, cmpopts.IgnoreFields(Assignment{}, "LastState"),
					)...,
				); diff != "" {
					t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
				}
			})
		}
	}
}

func TestLastAssignmentOutdated(t *testing.T) {
	type args struct {
		wl *workload.Info
		cq *schdcache.ClusterQueueSnapshot
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Cluster queue allocatableResourceIncreasedGen increased",
			args: args{
				wl: &workload.Info{
					LastAssignment: &workload.AssignmentClusterQueueState{
						ClusterQueueGeneration: 0,
					},
				},
				cq: &schdcache.ClusterQueueSnapshot{
					AllocatableResourceGeneration: 1,
				},
			},
			want: true,
		},
		{
			name: "AllocatableResourceGeneration not increased",
			args: args{
				wl: &workload.Info{
					LastAssignment: &workload.AssignmentClusterQueueState{
						ClusterQueueGeneration: 0,
					},
				},
				cq: &schdcache.ClusterQueueSnapshot{
					AllocatableResourceGeneration: 0,
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastAssignmentOutdated(tt.args.wl, tt.args.cq); got != tt.want {
				t.Errorf("LastAssignmentOutdated() = %v, want %v", got, tt.want)
			}
		})
	}
}

// We have 3 flavors: one, two, three and a 3-level cohort hierarchy where
// each cohort get 4 units of CPU in a different flavor.
func TestHierarchical(t *testing.T) {
	type rfMap = map[corev1.ResourceName]kueue.ResourceFlavorReference
	cases := map[string]struct {
		workloadRequests       *utiltestingapi.PodSetWrapper
		testClusterQueueUsage  resources.FlavorResourceQuantities
		otherClusterQueueUsage resources.FlavorResourceQuantities
		flavorFungibility      *kueue.FlavorFungibility
		wantMode               FlavorAssignmentMode
		wantAssigment          rfMap
	}{
		"Select the top flavor": {
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "4"),
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
			workloadRequests: utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Request(corev1.ResourceCPU, "4"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
			},
			wantMode:      Fit,
			wantAssigment: rfMap{corev1.ResourceCPU: "two"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
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
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas("three").Resource(corev1.ResourceCPU, "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.TryNextFlavor,
				}).Obj()
			otherCq := *utiltestingapi.MakeClusterQueue("other-clusterqueue").
				Cohort("two").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("two").Resource(corev1.ResourceCPU, "0").Obj(),
				*utiltestingapi.MakeFlavorQuotas("three").Resource(corev1.ResourceCPU, "0").Obj(),
			).Obj()

			wlInfo := workload.NewInfo(&kueue.Workload{
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
			log := testr.NewWithOptions(t, testr.Options{Verbosity: 2})
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			otherClusterQueue := snapshot.ClusterQueue("other-clusterqueue")
			otherClusterQueue.AddUsage(workload.Usage{Quota: tc.otherClusterQueueUsage})

			testClusterQueue := snapshot.ClusterQueue("test-clusterqueue")
			testClusterQueue.AddUsage(workload.Usage{Quota: tc.testClusterQueueUsage})

			flvAssigner := New(wlInfo, testClusterQueue, resourceFlavors, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared)
			assignment := flvAssigner.Assign(log, nil)
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
			workload: *workload.NewInfo(&kueue.Workload{
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
			workload: *workload.NewInfo(&kueue.Workload{
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
			workload: *workload.NewInfo(&kueue.Workload{
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
		"required topology is rejected with ElasticJobsViaWorkloadSlices": {
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
				replaceWorkloadSlice: workload.NewInfo(&kueue.Workload{
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
			workload: *workload.NewInfo(&kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/elastic-job": "true",
					},
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
		"preferred topology is rejected with ElasticJobsViaWorkloadSlices": {
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
				replaceWorkloadSlice: workload.NewInfo(&kueue.Workload{
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
			workload: *workload.NewInfo(&kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/elastic-job": "true",
					},
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
			wantErr: ErrElasticPreferredTopologyNotSupported,
		},
		"preferred topology is rejected for new elastic workload": {
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
			workload: *workload.NewInfo(&kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/elastic-job": "true",
					},
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
			wantErr: ErrElasticPreferredTopologyNotSupported,
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
				replaceWorkloadSlice: workload.NewInfo(&kueue.Workload{
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
			workload: *workload.NewInfo(&kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/elastic-job": "true",
					},
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
			workload: *workload.NewInfo(&kueue.Workload{
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
			workload: *workload.NewInfo(&kueue.Workload{
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
				wl: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
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
				wl: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
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
						Count: 71, // Assigned 1 pod.
					},
				},
				replaceWorkloadSlice: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).Obj()).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj()),
			},
			args: args{
				wl: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
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
				wl: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
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
				replaceWorkloadSlice: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
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
				wl: workload.NewInfo(utiltestingapi.MakeWorkload("test", "default").
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
			a := &Assignment{
				PodSets:              tt.fields.PodSets,
				replaceWorkloadSlice: tt.fields.replaceWorkloadSlice,
			}
			got := a.TotalRequestsFor(logr.Discard(), tt.args.wl)
			if diff := cmp.Diff(got, tt.want); diff != "" {
				t.Errorf("TotalRequestsFor() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestAssignment_ComputeTASNetUsage(t *testing.T) {
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
			wl: workload.NewInfo(&kueue.Workload{
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
					SinglePodRequests: resources.NewRequests(corev1.ResourceList{
						corev1.ResourceCPU:           resource.MustParse("1"),
						corev1.ResourceMemory:        resource.MustParse("1Gi"),
						"example.com/gpu":            resource.MustParse("1"),
						"networking.example.com/vpc": resource.MustParse("1"),
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
			wl: workload.NewInfo(&kueue.Workload{
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
					Name:               kueue.DefaultPodSetName,
					TopologyAssignment: &kueue.TopologyAssignment{},
				}},
			},
			want: workload.TASUsage{},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := tt.assignment.ComputeTASNetUsage(testr.New(t), tt.cq, tt.wl, tt.prevAdmission)

			if diff := cmp.Diff(tt.want, got, cmpopts.EquateEmpty()); diff != "" {
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
			cq := schdcache.ClusterQueueSnapshot{
				TASFlavors: map[kueue.ResourceFlavorReference]*schdcache.TASFlavorSnapshot{"tas": tasFlavor},
			}
			assignment := Assignment{PodSets: tc.podSets}
			wl := workload.NewInfo(&kueue.Workload{
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
func TestAssignFlavorsWithAllowedFlavors(t *testing.T) {
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
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, true)
			wlBuilder := utiltestingapi.MakeWorkload("wl", "ns").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "2").Obj())
			if tc.allowedFlavors != nil {
				wlBuilder = wlBuilder.AllowedFlavors(tc.allowedFlavors...)
			}
			wl := wlBuilder.Obj()

			wlInfo := workload.NewInfo(wl)

			ctx, log := utiltesting.ContextWithLog(t)
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

			assigner := New(wlInfo, cqSnapshot, resourceFlavors, false, &testOracle{}, nil, configapi.QuotaCheckBlockUndeclared)
			gotAssignment := assigner.Assign(log, nil)

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

func TestIsNoFitDueToCapacityAndLimits(t *testing.T) {
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
			replaceWl: workload.NewInfo(
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
		"tas placement fails": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
			},
			resourceFlavors: tasFlavors,
			cq:              tasCQ,
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("topology-tas").Levels("rack").Obj(),
			},
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				RequiredTopologyRequest("rack").
				Obj(),
			wantNoFitReason: "TopologyPlacementFailed",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a":   "NoMatchingFlavor",
				"flavor-b":   "NoMatchingFlavor",
				"flavor-tas": "TopologyPlacementFailed",
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
		"tas placement fails, but queue has available capacity (waiting for preemption)": {
			featureGates: map[featuregate.Feature]bool{
				features.TopologyAwareScheduling: true,
				features.ConcurrentAdmission:     true,
			},
			resourceFlavors: tasFlavors,
			cq:              tasCQ,
			topologies: []*kueue.Topology{
				utiltestingapi.MakeTopology("topology-tas").Levels("rack").Obj(),
			},
			podSet: *utiltestingapi.MakePodSet("main", 1).
				Request(corev1.ResourceCPU, "1").
				RequiredTopologyRequest("rack").
				Obj(),
			cqUsage: resources.FlavorResourceQuantities{
				{Flavor: "flavor-tas", Resource: corev1.ResourceCPU}: resources.NewAmount(2_000),
			},
			wantNoFitReason: "TopologyPlacementFailed",
			wantFlavorAttempts: map[kueue.ResourceFlavorReference]string{
				"flavor-a":   "NoMatchingFlavor",
				"flavor-b":   "NoMatchingFlavor",
				"flavor-tas": "TopologyPlacementFailed",
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
			wlInfo := workload.NewInfo(wl)

			ctx, log := utiltesting.ContextWithLog(t)
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
				cqSnapshot.AddUsage(workload.Usage{Quota: tc.cqUsage})
			}
			for siblingName, usage := range tc.siblingCQUsage {
				siblingSnapshot := snapshot.ClusterQueue(siblingName)
				if siblingSnapshot == nil {
					t.Fatalf("Sibling ClusterQueue %s not found in snapshot", siblingName)
				}
				siblingSnapshot.AddUsage(workload.Usage{Quota: usage})
			}

			assigner := New(wlInfo, cqSnapshot, testFlavors, false, &testOracle{simulationResult: tc.simulationResult}, tc.replaceWl, configapi.QuotaCheckBlockUndeclared)
			gotAssignment := assigner.Assign(log, nil)

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
