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

package preemption

import (
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	clocktesting "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
)

// configurableSnapCmpOpts extends snapCmpOpts for the TAS-enabled cases of this
// table. TASFlavorSnapshot holds an unexported logr.Logger (and other unexported
// helper types) which cmp cannot traverse, so the whole field is skipped; the TAS
// state is compared separately, through tasFreeCapacityPerDomain.
var configurableSnapCmpOpts = append(
	append(cmp.Options{}, snapCmpOpts...),
	cmpopts.IgnoreFields(schdcache.ClusterQueueSnapshot{}, "TASFlavors"),
)

// tasFreeCapacityPerDomain returns the serialized free capacity per domain of the
// TAS flavors of each ClusterQueue in the snapshot, so that the TAS state skipped by
// configurableSnapCmpOpts can still be compared.
func tasFreeCapacityPerDomain(t *testing.T, snapshot *schdcache.Snapshot) map[kueue.ClusterQueueReference]map[kueue.ResourceFlavorReference]string {
	t.Helper()
	state := make(map[kueue.ClusterQueueReference]map[kueue.ResourceFlavorReference]string)
	for cqName, cq := range snapshot.ClusterQueues() {
		for flavor, tasFlavor := range cq.TASFlavors {
			serialized, err := tasFlavor.SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("Failed to serialize the TAS flavor %q of ClusterQueue %q: %v", flavor, cqName, err)
			}
			if state[cqName] == nil {
				state[cqName] = make(map[kueue.ResourceFlavorReference]string)
			}
			state[cqName][flavor] = serialized
		}
	}
	return state
}

func TestConfigurablePreemptions(t *testing.T) {
	now := time.Now()
	defaultConfigName := "default-config"
	baseCQs := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "2").Obj()).
			Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
			Obj(),
	}

	baseConfig := *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
		Rule("test-rule-one", kueuealpha.Always,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).Obj()

	// configWithTrigger returns baseConfig with a single rule with the given trigger
	// and selector.
	configWithTrigger := func(trigger kueuealpha.PreemptionConfigActivationTrigger, selector kueuealpha.PreemptionConfigPreemptionCandidateSelector) kueuealpha.PreemptionConfig {
		return *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
			Rule("test-rule-one", trigger, selector).Obj()
	}

	// configWithSelector returns baseConfig with a single rule of the Always trigger
	// using the given selector.
	configWithSelector := func(selector kueuealpha.PreemptionConfigPreemptionCandidateSelector) kueuealpha.PreemptionConfig {
		return configWithTrigger(kueuealpha.Always, selector)
	}

	lowerTierConstraint := []kueuealpha.PreemptionConfigNumericLabelConstraint{
		{
			Key:        "preemption-tier",
			Comparison: ptr.To(kueuealpha.LessThan),
		},
	}
	withinParentCohortConfig := configWithSelector(
		utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinParentCohort).Obj(),
	)
	withinParentCohortTierConfig := configWithSelector(
		utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinParentCohort).
			NumericLabels(lowerTierConstraint...).Obj(),
	)
	anyClusterQueueConfig := configWithSelector(
		utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
	)
	withinClusterQueueTierConfig := configWithSelector(
		utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
			NumericLabels(lowerTierConstraint...).Obj(),
	)
	insufficientQuotaTriggerConfig := configWithTrigger(
		kueuealpha.InsufficientQuota,
		utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
	)
	// multiTriggerConfig allows preempting the workloads of a lower tier unconditionally,
	// and the remaining workloads of the ClusterQueue only if those are not enough to
	// free the quota needed by the preemptor.
	multiTriggerConfig := *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
		Rules(
			withinClusterQueueTierConfig.Spec.Rules[0],
			insufficientQuotaTriggerConfig.Spec.Rules[0],
		).Obj()

	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	// defaultAssignment requests cpu from the default flavor, in preemption mode.
	defaultAssignment := singlePodSetAssignment(flavorassigner.ResourceAssignment{
		corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
			Name: "default", Mode: flavorassigner.Preempt,
		},
	})

	// TopologyAwareScheduling fixtures, used by the QuotaFeasibleAndInsufficientTopology
	// trigger: a hostname topology over two nodes of 2 CPUs each.
	tasTopology := utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
	tasFlavor := utiltestingapi.MakeResourceFlavor("tas-default").
		NodeLabel("tas-node", "true").
		TopologyName("tas-single-level").
		Obj()
	tasNode := func(name string) corev1.Node {
		return *testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	tasNodes := []corev1.Node{tasNode("x1"), tasNode("x2")}
	tasCQs := func(nominalCPU string) []*kueue.ClusterQueue {
		return []*kueue.ClusterQueue{
			utiltestingapi.MakeClusterQueue("a").
				Cohort("all").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-default").
					Resource(corev1.ResourceCPU, nominalCPU).Obj()).
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
				Obj(),
		}
	}
	tasAdmittedWl := func(name, node string) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "").
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(corev1.ResourceCPU, "1").
				PreferredTopologyRequest(corev1.LabelHostname).
				Obj()).
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("a").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "tas-default", "1").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltas.TopologyDomainAssignment{Count: 1, Values: []string{node}}).
							Obj()).
						Obj()).
					Obj(),
				now,
			).
			Obj()
	}
	tasIncomingWl := utiltestingapi.MakeWorkload("a_incoming", "").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
			Request(corev1.ResourceCPU, "1").
			RequiredTopologyRequest(corev1.LabelHostname).
			Obj()).
		Obj()
	tasAssignment := flavorassigner.Assignment{
		PodSets: []flavorassigner.PodSetAssignment{{
			Name: kueue.DefaultPodSetName,
			Flavors: flavorassigner.ResourceAssignment{
				corev1.ResourceCPU: &flavorassigner.FlavorAssignment{
					Name: "tas-default", Mode: flavorassigner.Preempt,
				},
			},
			Count: 2,
		}},
	}
	topologyTriggerConfig := configWithTrigger(
		kueuealpha.QuotaFeasibleAndInsufficientTopology,
		utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
	)

	cases := map[string]struct {
		cohorts                        []*kueue.Cohort
		clusterQueues                  []*kueue.ClusterQueue
		resourceFlavors                []*kueue.ResourceFlavor
		topologies                     []*kueue.Topology
		nodes                          []corev1.Node
		config                         kueuealpha.PreemptionConfig
		admitted                       []kueue.Workload
		incoming                       *kueue.Workload
		assignment                     flavorassigner.Assignment
		targetCQ                       kueue.ClusterQueueReference
		fairSharing                    *config.FairSharing
		configurablePreemptionDisabled bool
		wantPreempted                  sets.Set[workload.Reference]
		wantReasons                    map[string]string
		wantConfigurableReasonsData    map[string]*policy.ConfigurablePreemptionReasonData
	}{
		"no candidates for CQ without config": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Obj(),
			},
			config: baseConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference](),
		},
		"no candidates when the ConfigurablePreemptions feature is disabled": {
			clusterQueues:                  baseCQs,
			config:                         baseConfig,
			configurablePreemptionDisabled: true,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference](),
		},
		"one workload should be preempted to fit incoming workload": {
			clusterQueues: baseCQs,
			config:        baseConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
		},
		"multiple workloads should be preempted to fit incoming workload": {
			clusterQueues: baseCQs,
			config:        baseConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Request(corev1.ResourceCPU, "2").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1", "/a2"),
		},
		"incoming workload cannot fit because it doesn't match any rule": {
			clusterQueues: baseCQs,
			config: *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
				RuleWithPreemptorSelector(
					"test-rule-one",
					kueuealpha.Always,
					&metav1.LabelSelector{
						MatchLabels: map[string]string{"team": "research"},
					},
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Label("team", "batch").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference](),
		},
		"returns no candidates when requested config not found by name": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "unknown-name").
					Obj(),
			},
			config: baseConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference](),
		},
		"returns no candidates when requested config has incorrect parameters": {
			clusterQueues: baseCQs,
			config: *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
				RuleWithPreemptorSelector(
					"test-rule-one",
					kueuealpha.Always,
					&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "test",
								Operator: "invalid",
							},
						},
					},
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference](),
		},
		"candidates from configurable rules are not added when classical ones are enough": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
			},
			config: withinClusterQueueTierConfig,
			admitted: []kueue.Workload{
				// a1 has no tier label, so it is a candidate for the classical algorithm only.
				*unitWl.Clone().Name("a1").
					Priority(10).
					SimpleReserveQuota("a", "default", now).Obj(),
				// a2 is a candidate for both algorithms.
				*unitWl.Clone().Name("a2").
					Priority(20).
					Label("preemption-tier", "1").
					SimpleReserveQuota("a", "default", now).Obj(),
				// a3 has a higher priority than the incoming workload, so it is a
				// candidate for the configurable algorithm only.
				*unitWl.Clone().Name("a3").
					Priority(200).
					Label("preemption-tier", "2").
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Priority(100).
				Label("preemption-tier", "5").
				Request(corev1.ResourceCPU, "2").
				Obj(),
			targetCQ: "a",
			// Classical candidates are considered before configurable ones, so a1 and
			// a2 admit the incoming workload on their own, sparing a3.
			wantPreempted: sets.New[workload.Reference]("/a1", "/a2"),
			wantReasons: map[string]string{
				"/a1": kueue.InClusterQueueReason,
				"/a2": kueue.InClusterQueueReason,
			},
		},
		"candidates selected by the configurable rules are added when classical ones are not enough": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
			},
			config: withinClusterQueueTierConfig,
			admitted: []kueue.Workload{
				// a1 has no tier label, so it is a candidate for the classical
				// algorithm only.
				*unitWl.Clone().Name("a1").
					Priority(10).
					SimpleReserveQuota("a", "default", now).Obj(),
				// a2 is a candidate for both algorithms.
				*unitWl.Clone().Name("a2").
					Priority(20).
					Label("preemption-tier", "1").
					SimpleReserveQuota("a", "default", now).Obj(),
				// a3 has a higher priority than the incoming workload, so it is a
				// candidate for the configurable algorithm only.
				*unitWl.Clone().Name("a3").
					Priority(200).
					Label("preemption-tier", "2").
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Priority(100).
				Label("preemption-tier", "5").
				Request(corev1.ResourceCPU, "3").
				Obj(),
			targetCQ: "a",
			// Classical preemption selects a1 and a2 first, and configurable
			// preemption is used as a fallback to select a3 once the classical
			// candidates are not enough to free the 3 CPU needed.
			wantPreempted: sets.New[workload.Reference]("/a1", "/a2", "/a3"),
			wantReasons: map[string]string{
				"/a1": kueue.InClusterQueueReason,
				"/a2": kueue.InClusterQueueReason,
				"/a3": kueue.ConfigurablePreemptionReason,
			},
		},
		"configurable candidates can select workloads in different ClusterQueue within nominal quota": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			config: withinParentCohortConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				// b is not borrowing, so b1 would be rejected by the classical
				// reclamation rules.
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ: "a",
			// a1 and b1 are both selected by the WithinParentCohort rule, and the
			// candidate ordering considers the workloads of other ClusterQueues
			// first. Preempting b1 frees quota that a can borrow, so a1 is spared.
			wantPreempted: sets.New[workload.Reference]("/b1"),
			wantReasons: map[string]string{
				"/b1": kueue.ConfigurablePreemptionReason,
			},
		},
		"candidate selected by both algorithms is preempted once, with the classical reason": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
			},
			config: baseConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(10).SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Priority(100).Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
			wantReasons: map[string]string{
				// Classical preemption runs before the PreemptionConfig fallback, so
				// a1 is preempted by the classical WithinClusterQueue policy.
				"/a1": kueue.InClusterQueueReason,
			},
		},
		"classical target not needed anymore is given back once configurable target is preempted": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Obj(),
			},
			config: withinParentCohortTierConfig,
			admitted: []kueue.Workload{
				// s1 is selected first by the classical algorithm, but freeing 1 CPU
				// is not enough; once c1 (2 CPU) is selected by the configurable
				// rules, c1 alone frees enough quota so s1 is given back during backfill.
				*unitWl.Clone().Name("s1").
					Priority(50).
					SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "2").
					Priority(10).
					Label("preemption-tier", "1").
					SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").
				Priority(100).
				Label("preemption-tier", "5").
				Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/c1"),
			wantReasons: map[string]string{
				"/c1": kueue.ConfigurablePreemptionReason,
			},
		},
		"Priority: only candidates with lower priority are preempted": {
			clusterQueues: baseCQs,
			config: *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
				Rule("priority-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.Base, kueuealpha.LessThan).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").
					Priority(50).
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Priority(200).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Priority(100).
				Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
			},
		},
		"Priority with priority boost annotation in Boosted mode modifies preemption ordering": {
			clusterQueues: baseCQs,
			config: *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
				Rule("boosted-priority-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.Boosted, kueuealpha.LessThan).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").
					Priority(100).
					Annotation(controllerconstants.PriorityBoostAnnotationKey, "-60").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Priority(60).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Priority(70).
				Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
			},
		},
		"Priority with priority boost annotation in Base mode ignores boost": {
			clusterQueues: baseCQs,
			config: *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
				Rule("base-priority-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.Base, kueuealpha.LessThan).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").
					Priority(100).
					Annotation(controllerconstants.PriorityBoostAnnotationKey, "-60").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Priority(60).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Priority(70).
				Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a2"),
			wantReasons: map[string]string{
				"/a2": kueue.ConfigurablePreemptionReason,
			},
		},
		"LabelSelector filters candidates in preemption configurable pipeline": {
			clusterQueues: baseCQs,
			config: *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
				Rule("label-selector-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						LabelSelector(&metav1.LabelSelector{
							MatchLabels: map[string]string{"env": "preemptible"},
						}).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").
					Label("env", "preemptible").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Label("env", "guaranteed").
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
			},
		},
		"candidate from another Cohort is given back when it doesn't help": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("one").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("two").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			config: anyClusterQueueConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(100).SimpleReserveQuota("a", "default", now).Obj(),
				// b1 is considered first by the candidate ordering, as it belongs to
				// another ClusterQueue and has a lower priority. However, b belongs to
				// another Cohort, so preempting b1 doesn't free any quota for the
				// incoming workload, and b1 is given back once a1 is preempted.
				*unitWl.Clone().Name("b1").Priority(10).SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
			},
		},
		"already evicted configurable candidate is preempted first": {
			clusterQueues: baseCQs,
			config:        baseConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				// Despite sorting after a1 by UID, z1 comes first as it is already evicted.
				*unitWl.Clone().Name("z1").SimpleReserveQuota("a", "default", now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						LastTransitionTime: metav1.NewTime(now),
					}).Obj(),
			},
			incoming:      unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/z1"),
		},
		"fair sharing: configurable candidate in a ClusterQueue within nominal quota is preempted": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			config:      withinParentCohortConfig,
			fairSharing: &config.FairSharing{},
			admitted: []kueue.Workload{
				// a doesn't allow preemption within the ClusterQueue, so a1 is not a
				// Fair Sharing candidate.
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				// when default preemption policies (Never) are set and Fair Sharing is enabled,
				// only the configurable preemption candidates are returned
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").Obj(),
			targetCQ: "a",
			// a1 and b1 are both selected by the WithinParentCohort rule, and the
			// candidate ordering considers the workloads of other ClusterQueues
			// first. Preempting b1 frees quota that a can borrow, so a1 is spared.
			wantPreempted: sets.New[workload.Reference]("/b1"),
			wantReasons: map[string]string{
				"/b1": kueue.ConfigurablePreemptionReason,
			},
		},
		"fair sharing: strategies are preferred over the configurable candidates": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			config:      withinParentCohortTierConfig,
			fairSharing: &config.FairSharing{},
			admitted: []kueue.Workload{
				// b is borrowing, so both b1 and b2 are Fair Sharing candidates, but only
				// b2 is selected by the configurable rules.
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b2").
					Label("preemption-tier", "1").
					SimpleReserveQuota("b", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Label("preemption-tier", "5").Obj(),
			targetCQ: "a",
			// The Fair Sharing strategy admits the workload on its own, so the
			// candidates of the Always trigger are never considered.
			wantPreempted: sets.New[workload.Reference]("/b1"),
			wantReasons: map[string]string{
				"/b1": kueue.InCohortReclamationReason,
			},
		},
		"fair sharing: the second strategy is preferred over the configurable candidates": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "3").Obj()).
					Obj(),
			},
			config:      withinParentCohortTierConfig,
			fairSharing: &config.FairSharing{},
			admitted: []kueue.Workload{
				// b borrows 2 CPUs, so b1 is a Fair Sharing candidate, but only rule
				// S2-b can preempt it: preempting b1 would leave b with a share
				// lower than the one a reaches with the incoming workload, while a
				// stays below the initial share of b.
				*utiltestingapi.MakeWorkload("b1", "").Request(corev1.ResourceCPU, "5").
					SimpleReserveQuota("b", "default", now).Obj(),
				// c is not borrowing, so the Fair Sharing ordering prunes c1: it is
				// only reachable through the configurable rules, even though
				// preempting it would admit the incoming workload on its own.
				*utiltestingapi.MakeWorkload("c1", "").Request(corev1.ResourceCPU, "3").
					Label("preemption-tier", "1").
					SimpleReserveQuota("c", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").
				Request(corev1.ResourceCPU, "4").
				Label("preemption-tier", "5").Obj(),
			targetCQ: "a",
			// The configurable candidates are a last resort, reached only once both
			// strategies failed, so b1 is preferred over c1
			wantPreempted: sets.New[workload.Reference]("/b1"),
			wantReasons: map[string]string{
				"/b1": kueue.InCohortFairSharingReason,
			},
		},
		"fair sharing: candidates selected by configurable rules are added when strategies are not enough": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
					Obj(),
			},
			config:      withinClusterQueueTierConfig,
			fairSharing: &config.FairSharing{},
			admitted: []kueue.Workload{
				// x1 has a higher priority than the incoming workload, so it is only
				// preemptible through the configurable rules.
				*unitWl.Clone().Name("x1").
					Priority(500).
					Label("preemption-tier", "1").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("x2").
					Priority(10).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: utiltestingapi.MakeWorkload("a_incoming", "").Request(corev1.ResourceCPU, "2").
				Priority(100).
				Label("preemption-tier", "5").
				Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/x1", "/x2"),
			wantReasons: map[string]string{
				"/x1": kueue.ConfigurablePreemptionReason,
				"/x2": kueue.InClusterQueueReason,
			},
		},
		"InsufficientQuota trigger extends the candidates of the Always trigger": {
			clusterQueues: baseCQs,
			config:        multiTriggerConfig,
			admitted: []kueue.Workload{
				// a1 belongs to a lower tier, so it is selected by the Always rule,
				// while a2 is only selected by the InsufficientQuota rule. The candidate
				// ordering prefers a2, as it has a lower priority, so it would be
				// preempted first if the triggers were considered at once.
				*unitWl.Clone().Name("a1").
					Priority(100).
					Label("preemption-tier", "1").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Priority(10).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Request(corev1.ResourceCPU, "2").
				Label("preemption-tier", "5").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1", "/a2"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
				"/a2": kueue.ConfigurablePreemptionReason,
			},
		},
		"InsufficientQuota trigger is not used when the Always trigger is enough": {
			clusterQueues: baseCQs,
			config:        multiTriggerConfig,
			admitted: []kueue.Workload{
				// a1 belongs to a lower tier, so it is selected by the Always rule,
				// while a2 is only selected by the InsufficientQuota rule. The candidate
				// ordering prefers a2, as it has a lower priority, so it would be
				// preempted instead of a1 if the triggers were considered at once.
				*unitWl.Clone().Name("a1").
					Priority(100).
					Label("preemption-tier", "1").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Priority(10).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Label("preemption-tier", "5").Obj(),
			targetCQ: "a",
			// a1 is selected by the Always trigger and frees enough quota on its
			// own, so the InsufficientQuota trigger is not reached.
			wantPreempted: sets.New[workload.Reference]("/a1"),
		},
		"fair sharing: InsufficientQuota trigger extends the candidates of the Always trigger": {
			clusterQueues: baseCQs,
			config:        multiTriggerConfig,
			fairSharing:   &config.FairSharing{},
			admitted: []kueue.Workload{
				// The ClusterQueue doesn't allow preemption, so the Fair Sharing
				// algorithm has no candidate of its own. a1 belongs to a lower tier,
				// so it is selected by the Always rule, while a2 is only selected by
				// the InsufficientQuota rule. The candidate ordering prefers a2, as
				// it has a lower priority, so it would be preempted first if the
				// triggers were considered at once.
				*unitWl.Clone().Name("a1").
					Priority(100).
					Label("preemption-tier", "1").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Priority(10).
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			incoming: unitWl.Clone().Name("a_incoming").
				Request(corev1.ResourceCPU, "2").
				Label("preemption-tier", "5").Obj(),
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1", "/a2"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
				"/a2": kueue.ConfigurablePreemptionReason,
			},
		},
		"QuotaFeasibleAndInsufficientTopology trigger is used when the quota fits but no topology assignment is found": {
			clusterQueues:   tasCQs("4"),
			resourceFlavors: []*kueue.ResourceFlavor{tasFlavor},
			topologies:      []*kueue.Topology{tasTopology},
			nodes:           tasNodes,
			config:          topologyTriggerConfig,
			admitted: []kueue.Workload{
				tasAdmittedWl("a1", "x1"),
				tasAdmittedWl("a2", "x2"),
			},
			// The incoming workload fits in the quota of the ClusterQueue (2 out of
			// the 4 CPUs are used), but its 2 pods require the same node, and both
			// nodes have only 1 of their 2 CPUs free.
			incoming:      tasIncomingWl,
			assignment:    tasAssignment,
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference]("/a1"),
			wantReasons: map[string]string{
				"/a1": kueue.ConfigurablePreemptionReason,
			},
			wantConfigurableReasonsData: map[string]*policy.ConfigurablePreemptionReasonData{
				"/a1": {
					ConfigName:                policy.PreemptionConfigReference(defaultConfigName),
					RuleNameToSelectorIndexes: map[policy.PreemptionConfigRuleReference][]int{"test-rule-one": {0}},
				},
			},
		},
		"QuotaFeasibleAndInsufficientTopology trigger is not used when the quota is insufficient": {
			// The nominal quota only covers the admitted workloads, so the preemptor
			// is blocked by the quota rather than by the topology, and the rule of the
			// QuotaFeasibleAndInsufficientTopology trigger must not be applied.
			clusterQueues:   tasCQs("2"),
			resourceFlavors: []*kueue.ResourceFlavor{tasFlavor},
			topologies:      []*kueue.Topology{tasTopology},
			nodes:           tasNodes,
			config:          topologyTriggerConfig,
			admitted: []kueue.Workload{
				tasAdmittedWl("a1", "x1"),
				tasAdmittedWl("a2", "x2"),
			},
			incoming:      tasIncomingWl,
			assignment:    tasAssignment,
			targetCQ:      "a",
			wantPreempted: sets.New[workload.Reference](),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, !tc.configurablePreemptionDisabled)
			features.SetFeatureGateDuringTest(t, features.PriorityBoost, true)
			// Only the cases exercising the QuotaFeasibleAndInsufficientTopology trigger need
			// TAS; the others keep running without it, as most deployments do.
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, len(tc.topologies) > 0)
			ctx, log := utiltesting.ContextWithLog(t)
			// Set name as UID so that candidates sorting is predictable.
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				WithLists(&kueuealpha.PreemptionConfigList{Items: []kueuealpha.PreemptionConfig{tc.config}}).
				WithLists(&corev1.NodeList{Items: tc.nodes}).
				Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			for _, flavor := range tc.resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(log, flavor)
			}
			for _, topology := range tc.topologies {
				cqCache.AddOrUpdateTopology(log, topology)
			}
			for i := range tc.nodes {
				cqCache.TASCache().SyncNode(&tc.nodes[i])
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}
			for _, cohort := range tc.cohorts {
				if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
					t.Fatalf("Couldn't add Cohort to cache: %v", err)
				}
			}

			recorder := &utiltesting.EventRecorder{}
			preemptor := New(cl, workload.Ordering{}, recorder, tc.fairSharing, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)

			beforeSnapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			snapshotWorkingCopy, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			wlInfo := workload.NewInfo(log, tc.incoming)
			wlInfo.ClusterQueue = tc.targetCQ
			assignment := tc.assignment
			if len(assignment.PodSets) == 0 {
				assignment = defaultAssignment
			}
			strategies := preemptor.GetPreemptionStrategyIterator(ctx, *wlInfo, snapshotWorkingCopy, assignment)
			targets := preemptor.GetTargetsWithStrategy(ctx, strategies)
			// The targets are compared as a sorted list, rather than as a set, so that
			// duplicated targets are reported as well.
			gotTargets := slices.Sorted(slices.Values(utilslices.Map(targets, func(t **Target) workload.Reference {
				return workload.Key((*t).WorkloadInfo.Obj)
			})))
			if diff := cmp.Diff(sets.List(tc.wantPreempted), gotTargets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Issued preemptions (-want,+got):\n%s", diff)
			}
			if tc.wantReasons != nil {
				gotReasons := make(map[string]string, len(targets))
				for _, target := range targets {
					gotReasons[string(workload.Key(target.WorkloadInfo.Obj))] = target.Reason
				}
				if diff := cmp.Diff(tc.wantReasons, gotReasons); diff != "" {
					t.Errorf("Preemption reasons (-want,+got):\n%s", diff)
				}
			}
			if tc.wantConfigurableReasonsData != nil {
				gotData := make(map[string]*policy.ConfigurablePreemptionReasonData, len(targets))
				for _, target := range targets {
					gotData[string(workload.Key(target.WorkloadInfo.Obj))] = target.ConfigurablePreemptionReasonData
				}
				if diff := cmp.Diff(tc.wantConfigurableReasonsData, gotData); diff != "" {
					t.Errorf("Preemption reason data (-want,+got):\n%s", diff)
				}
			}

			if diff := cmp.Diff(tasFreeCapacityPerDomain(t, beforeSnapshot), tasFreeCapacityPerDomain(t, snapshotWorkingCopy)); diff != "" {
				t.Errorf("TAS snapshot was modified (-initial,+end):\n%s", diff)
			}
			if diff := cmp.Diff(beforeSnapshot, snapshotWorkingCopy, configurableSnapCmpOpts); diff != "" {
				t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
			}
		})
	}
}

func TestPreemptionOracleConfigurablePreemptions(t *testing.T) {
	now := time.Now()
	defaultConfigName := "default-config"
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	// The ClusterQueue doesn't allow any classical or Fair Sharing preemption, so the
	// only candidates are the ones selected by the PreemptionConfig.
	clusterQueue := utiltestingapi.MakeClusterQueue("a").
		Cohort("all").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "2").Obj()).
		Annotation(kueuealpha.PreemptionConfigNameAnnotation, defaultConfigName).
		Obj()
	preemptionConfig := *utiltestingalpha.MakePreemptionConfig(defaultConfigName).
		Rule("within-cluster-queue", kueuealpha.Always, kueuealpha.PreemptionConfigPreemptionCandidateSelector{
			Scope: kueuealpha.WithinClusterQueue,
		}).Obj()
	admitted := []kueue.Workload{
		*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
		*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
	}
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	cases := map[string]struct {
		fairSharing                    *config.FairSharing
		configurablePreemptionDisabled bool
		want                           policy.PreemptionPossibility
	}{
		"classical: candidates selected by the PreemptionConfig are considered": {
			want: policy.Preempt,
		},
		"classical: no candidates when the ConfigurablePreemptions feature is disabled": {
			configurablePreemptionDisabled: true,
			want:                           policy.NoCandidates,
		},
		"fair sharing: candidates selected by the PreemptionConfig are considered": {
			fairSharing: &config.FairSharing{},
			want:        policy.Preempt,
		},
		"fair sharing: no candidates when the ConfigurablePreemptions feature is disabled": {
			fairSharing:                    &config.FairSharing{},
			configurablePreemptionDisabled: true,
			want:                           policy.NoCandidates,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, !tc.configurablePreemptionDisabled)
			ctx, log := utiltesting.ContextWithLog(t)
			workloads := make([]kueue.Workload, len(admitted))
			for i := range admitted {
				workloads[i] = *admitted[i].DeepCopy()
				workloads[i].UID = types.UID(workloads[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: workloads}).
				WithLists(&kueuealpha.PreemptionConfigList{Items: []kueuealpha.PreemptionConfig{preemptionConfig}}).
				Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cqCache.AddClusterQueue(ctx, clusterQueue); err != nil {
				t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
			}
			beforeSnapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, tc.fairSharing, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil)
			wlInfo := workload.NewInfo(log, unitWl.Clone().Name("a_incoming").Obj())
			wlInfo.ClusterQueue = "a"

			// When
			got, _ := NewOracle(preemptor, snapshot).SimulatePreemption(ctx, snapshot.ClusterQueue("a"), *wlInfo, fr, resources.NewAmount(1000))

			// Then
			if got != tc.want {
				t.Errorf("SimulatePreemption() = %v, want %v", got, tc.want)
			}
			if diff := cmp.Diff(beforeSnapshot, snapshot, snapCmpOpts); diff != "" {
				t.Errorf("Snapshot was modified (-initial,+end):\n%s", diff)
			}
		})
	}
}
