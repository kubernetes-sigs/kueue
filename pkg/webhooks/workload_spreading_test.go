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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	validSpreadingJSON      = `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"0.45"}]}`
	equivalentSpreadingJSON = `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"0.45","enforcementMode":"Required"}]}`
	emptySelectorsJSON      = `{"workloadLabelSelectors":[],"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"0.45"}]}`
	otherSpreadingJSON      = `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"0.5"}]}`
	invalidSpreadingJSON    = `not-json`
	shareOneJSON            = `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"1"}]}`
	shareOneMilliJSON       = `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"1000m"}]}`
	requiredTopologyLevel   = "cloud.com/block"
)

func spreadingAnnPath(i int) *field.Path {
	return field.NewPath("spec", "podSets").Index(i).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
}

func spreadingPodSet(name kueue.PodSetReference, count int, spreading string) kueue.PodSet {
	return *utiltestingapi.MakePodSet(name, count).
		RequiredTopologyRequest(requiredTopologyLevel).
		Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: spreading}).
		Obj()
}

func groupedPodSet(name kueue.PodSetReference, group, spreading string) kueue.PodSet {
	ps := utiltestingapi.MakePodSet(name, 1).
		RequiredTopologyRequest(requiredTopologyLevel).
		PodSetGroup(group)
	if spreading != "" {
		ps = ps.Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: spreading})
	}
	return *ps.Obj()
}

func TestValidateWorkloadSpreadingCreate(t *testing.T) {
	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		workload     *kueue.Workload
		wantErr      error
	}{
		"valid: absent spreading annotation is unaffected": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
		},
		"valid: structured required topology, omitted selectors, no job UID or companion annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(spreadingPodSet("main", 1, validSpreadingJSON)).
				Obj(),
		},
		"valid: empty selectors remain accepted": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(spreadingPodSet("main", 1, emptySelectorsJSON)).
				Obj(),
		},
		"valid: group members have matching spreading annotations": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*utiltestingapi.MakePodSet("b", 2).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
				).
				Obj(),
		},
		"valid: group members have equivalent parsed spreading annotations": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*utiltestingapi.MakePodSet("b", 2).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: equivalentSpreadingJSON}).
						Obj(),
				).
				Obj(),
		},
		"valid: standalone PodSet and a named group with the same name stay distinct": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					spreadingPodSet("g", 1, otherSpreadingJSON),
					groupedPodSet("leader", "g", validSpreadingJSON),
					groupedPodSet("workers", "g", validSpreadingJSON),
				).
				Obj(),
		},
		"valid: unrelated named groups do not participate in one another's comparison": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					groupedPodSet("a1", "g1", validSpreadingJSON),
					groupedPodSet("a2", "g1", validSpreadingJSON),
					groupedPodSet("b1", "g2", otherSpreadingJSON),
					groupedPodSet("b2", "g2", otherSpreadingJSON),
				).
				Obj(),
		},
		"valid: neither group member carries the spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					groupedPodSet("a", "g", ""),
					groupedPodSet("b", "g", ""),
				).
				Obj(),
		},
		"valid: gate off, malformed annotation left unvalidated": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: false},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(spreadingPodSet("main", 1, invalidSpreadingJSON)).
				Obj(),
		},
		"invalid: empty spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(spreadingPodSet("main", 1, "")).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0), nil, ""),
			}.ToAggregate(),
		},
		"invalid: share of 1 is rejected at the Workload field path": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(spreadingPodSet("main", 1, `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"1"}]}`)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0).Child("rules").Index(0).Child("maxShareAllowingPlacement"), nil, ""),
			}.ToAggregate(),
		},
		"invalid: nil topology request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(spreadingAnnPath(0), ""),
			}.ToAggregate(),
		},
		"invalid: topology request without required": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(func() kueue.PodSet {
					ps := utiltestingapi.MakePodSet("main", 1).
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON})
					ps.TopologyRequest = &kueue.PodSetTopologyRequest{}
					return *ps.Obj()
				}()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(spreadingAnnPath(0), ""),
			}.ToAggregate(),
		},
		"invalid: preferred-only topology request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PreferredTopologyRequest(requiredTopologyLevel).
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(spreadingAnnPath(0), ""),
			}.ToAggregate(),
		},
		"invalid: unconstrained-only topology request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					UnconstrainedTopologyRequest().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(spreadingAnnPath(0), ""),
			}.ToAggregate(),
		},
		"invalid: companion required-topology annotation without structured required request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Annotations(map[string]string{
						kueue.PodSetRequiredTopologyAnnotation:  requiredTopologyLevel,
						kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON,
					}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(spreadingAnnPath(0), ""),
			}.ToAggregate(),
		},
		"invalid: group members have different spreading annotations": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					groupedPodSet("leader", "g", validSpreadingJSON),
					groupedPodSet("workers", "g", otherSpreadingJSON),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(1), nil, ""),
			}.ToAggregate(),
		},
		"invalid: first group member is missing the spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					groupedPodSet("leader", "g", ""),
					groupedPodSet("workers", "g", validSpreadingJSON),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0), nil, ""),
			}.ToAggregate(),
		},
		"invalid: later group member is missing the spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(
					groupedPodSet("leader", "g", validSpreadingJSON),
					groupedPodSet("workers", "g", ""),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(1), nil, ""),
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			_, gotErr := (&WorkloadWebhook{}).ValidateCreate(t.Context(), tc.workload)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateCreate() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateWorkloadSpreadingUpdate(t *testing.T) {
	pendingInvalid := func() *kueue.Workload {
		return utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
			Queue("q1").
			PodSets(spreadingPodSet("main", 1, invalidSpreadingJSON)).
			Obj()
	}
	pendingValid := func() *kueue.Workload {
		return utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
			Queue("q1").
			PodSets(spreadingPodSet("main", 1, validSpreadingJSON)).
			Obj()
	}
	withPodSets := func(podSets ...kueue.PodSet) *kueue.Workload {
		return utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
			Queue("q1").
			PodSets(podSets...).
			Obj()
	}
	annotatedMember := func(name kueue.PodSetReference, count int, group, spreading string) kueue.PodSet {
		return *utiltestingapi.MakePodSet(name, count).
			RequiredTopologyRequest(requiredTopologyLevel).
			PodSetGroup(group).
			Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: spreading}).
			Obj()
	}

	testCases := map[string]struct {
		featureGates  map[featuregate.Feature]bool
		before, after *kueue.Workload
		wantErr       error
	}{
		"reject changing a valid spreading annotation to invalid": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid(),
			after:        pendingInvalid(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0), nil, ""),
			}.ToAggregate(),
		},
		"exempt an unchanged invalid spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Queue("q2").
				PodSets(spreadingPodSet("main", 1, invalidSpreadingJSON)).
				Obj(),
		},
		"reject a different invalid annotation including equivalent invalid quantities": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       withPodSets(spreadingPodSet("main", 1, shareOneJSON)),
			after:        withPodSets(spreadingPodSet("main", 1, shareOneMilliJSON)),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0).Child("rules").Index(0).Child("maxShareAllowingPlacement"), nil, ""),
			}.ToAggregate(),
		},
		"reject absent spreading becoming a present empty annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(*utiltestingapi.MakePodSet("main", 1).
				RequiredTopologyRequest(requiredTopologyLevel).
				Obj()),
			after: withPodSets(spreadingPodSet("main", 1, "")),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0), nil, ""),
			}.ToAggregate(),
		},
		"reject removing required topology while spreading remains": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Queue("q1").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(spreadingAnnPath(0), ""),
			}.ToAggregate(),
		},
		"reject a required topology change while the annotation stays invalid": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Queue("q1").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest("cloud.com/rack").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0), nil, ""),
			}.ToAggregate(),
		},
		"accept a required topology change when spreading stays valid": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Queue("q1").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest("cloud.com/rack").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
		},
		"accept an equivalent rewrite of a valid annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid(),
			after:        withPodSets(spreadingPodSet("main", 1, equivalentSpreadingJSON)),
		},
		"reject a membership change that introduces group disagreement": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(
				annotatedMember("a1", 1, "g1", validSpreadingJSON),
				annotatedMember("a2", 2, "g1", validSpreadingJSON),
				annotatedMember("b1", 1, "g2", otherSpreadingJSON),
				annotatedMember("b2", 2, "g2", otherSpreadingJSON),
			),
			after: withPodSets(
				annotatedMember("a1", 1, "g1", validSpreadingJSON),
				annotatedMember("a2", 2, "g2", validSpreadingJSON),
				annotatedMember("b1", 1, "g2", otherSpreadingJSON),
				annotatedMember("b2", 2, "g1", otherSpreadingJSON),
			),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(3), nil, ""),
				field.Invalid(spreadingAnnPath(2), nil, ""),
			}.ToAggregate(),
		},
		"repair one group while an unchanged invalid group remains": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(
				annotatedMember("a1", 1, "g1", validSpreadingJSON),
				annotatedMember("a2", 2, "g1", otherSpreadingJSON),
				annotatedMember("b1", 1, "g2", validSpreadingJSON),
				annotatedMember("b2", 2, "g2", otherSpreadingJSON),
			),
			after: withPodSets(
				annotatedMember("a1", 1, "g1", validSpreadingJSON),
				annotatedMember("a2", 2, "g1", validSpreadingJSON),
				annotatedMember("b1", 1, "g2", validSpreadingJSON),
				annotatedMember("b2", 2, "g2", otherSpreadingJSON),
			),
		},
		"membership change with identical malformed annotations does not force annotation validity": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(
				annotatedMember("a1", 1, "g1", invalidSpreadingJSON),
				annotatedMember("a2", 2, "g1", invalidSpreadingJSON),
				annotatedMember("b1", 1, "g2", invalidSpreadingJSON),
				annotatedMember("b2", 2, "g2", invalidSpreadingJSON),
			),
			after: withPodSets(
				annotatedMember("a1", 1, "g1", invalidSpreadingJSON),
				annotatedMember("a2", 2, "g2", invalidSpreadingJSON),
				annotatedMember("b1", 1, "g2", invalidSpreadingJSON),
				annotatedMember("b2", 2, "g1", invalidSpreadingJSON),
			),
		},
		"reorder named PodSets without changing spreading inputs": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(
				*utiltestingapi.MakePodSet("extra", 1).Obj(),
				spreadingPodSet("main", 1, invalidSpreadingJSON),
			),
			after: withPodSets(
				spreadingPodSet("main", 1, invalidSpreadingJSON),
				*utiltestingapi.MakePodSet("extra", 1).Obj(),
			),
		},
		"rename a PodSet carrying invalid spreading": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid(),
			after:        withPodSets(spreadingPodSet("other", 1, invalidSpreadingJSON)),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(0), nil, ""),
			}.ToAggregate(),
		},
		"adding an unannotated standalone PodSet does not revalidate old spreading errors": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid(),
			after: withPodSets(
				spreadingPodSet("main", 1, invalidSpreadingJSON),
				*utiltestingapi.MakePodSet("extra", 1).Obj(),
			),
		},
		"reject removing spreading from only one member of a pair": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(
				annotatedMember("leader", 1, "g", validSpreadingJSON),
				annotatedMember("workers", 2, "g", validSpreadingJSON),
			),
			after: withPodSets(
				annotatedMember("leader", 1, "g", validSpreadingJSON),
				*utiltestingapi.MakePodSet("workers", 2).
					RequiredTopologyRequest(requiredTopologyLevel).
					PodSetGroup("g").
					Obj(),
			),
			wantErr: field.ErrorList{
				field.Invalid(spreadingAnnPath(1), nil, ""),
			}.ToAggregate(),
		},
		"accept removing spreading from all members of a pair together": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: withPodSets(
				annotatedMember("leader", 1, "g", validSpreadingJSON),
				annotatedMember("workers", 2, "g", validSpreadingJSON),
			),
			after: withPodSets(
				*utiltestingapi.MakePodSet("leader", 1).
					RequiredTopologyRequest(requiredTopologyLevel).
					PodSetGroup("g").
					Obj(),
				*utiltestingapi.MakePodSet("workers", 2).
					RequiredTopologyRequest(requiredTopologyLevel).
					PodSetGroup("g").
					Obj(),
			),
		},
		"exemption does not skip other Workload validation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid(),
			after: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				Queue("q1").
				PodSets(
					func() kueue.PodSet {
						ps := spreadingPodSet("main", 2, invalidSpreadingJSON)
						ps.MinCount = new(int32(1))
						return ps
					}(),
					*utiltestingapi.MakePodSet("other", 2).SetMinimumCount(1).Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "podSets"), nil, ""),
			}.ToAggregate(),
		},
		"gate off: invalid spreading updates remain unvalidated": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: false},
			before: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).
				PodSets(*utiltestingapi.MakePodSet("main", 1).RequiredTopologyRequest(requiredTopologyLevel).Obj()).
				Obj(),
			after: pendingInvalid(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			_, gotErr := (&WorkloadWebhook{}).ValidateUpdate(t.Context(), tc.before, tc.after)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateUpdate() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
