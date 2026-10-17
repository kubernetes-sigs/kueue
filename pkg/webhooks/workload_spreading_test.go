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

func TestValidateWorkloadSpreadingCreate(t *testing.T) {
	podSetsPath := field.NewPath("spec", "podSets")
	firstSpreadingPath := podSetsPath.Index(0).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	secondSpreadingPath := podSetsPath.Index(1).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	baseWorkload := utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace)
	mainPodSet := utiltestingapi.MakePodSet("main", 1).RequiredTopologyRequest(requiredTopologyLevel)
	leaderPodSet := utiltestingapi.MakePodSet("leader", 1).RequiredTopologyRequest(requiredTopologyLevel).PodSetGroup("g")
	workersPodSet := utiltestingapi.MakePodSet("workers", 1).RequiredTopologyRequest(requiredTopologyLevel).PodSetGroup("g")

	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		workload     *kueue.Workload
		wantErr      error
	}{
		"valid: absent spreading annotation is unaffected": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).Obj()).
				Obj(),
		},
		"valid: structured required topology, omitted selectors, no job UID or companion annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*mainPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
		},
		"valid: empty selectors remain accepted": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*mainPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: emptySelectorsJSON}).
					Obj()).
				Obj(),
		},
		"valid: group members have matching spreading annotations": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
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
			workload: baseWorkload.Clone().
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
			workload: baseWorkload.Clone().
				PodSets(
					*utiltestingapi.MakePodSet("g", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
						Obj(),
					*leaderPodSet.Clone().
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*workersPodSet.Clone().
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
				).
				Obj(),
		},
		"valid: unrelated named groups do not participate in one another's comparison": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(
					*utiltestingapi.MakePodSet("a1", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g1").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*utiltestingapi.MakePodSet("a2", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g1").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*utiltestingapi.MakePodSet("b1", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g2").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
						Obj(),
					*utiltestingapi.MakePodSet("b2", 1).
						RequiredTopologyRequest(requiredTopologyLevel).
						PodSetGroup("g2").
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
						Obj(),
				).
				Obj(),
		},
		"valid: neither group member carries the spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(
					*utiltestingapi.MakePodSet("a", 1).RequiredTopologyRequest(requiredTopologyLevel).PodSetGroup("g").Obj(),
					*utiltestingapi.MakePodSet("b", 1).RequiredTopologyRequest(requiredTopologyLevel).PodSetGroup("g").Obj(),
				).
				Obj(),
		},
		"valid: gate off, malformed annotation left unvalidated": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: false},
			workload: baseWorkload.Clone().
				PodSets(*mainPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj()).
				Obj(),
		},
		"invalid: empty spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*mainPodSet.Clone().Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: ""}).Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"invalid: share of 1 is rejected at the Workload field path": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*mainPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: `{"rules":[{"topologyKey":"cloud.com/block","maxShareAllowingPlacement":"1"}]}`}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath.Child("rules").Index(0).Child("maxShareAllowingPlacement"), nil, ""),
			}.ToAggregate(),
		},
		"invalid: nil topology request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(firstSpreadingPath, ""),
			}.ToAggregate(),
		},
		"invalid: topology request without required": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(func() kueue.PodSet {
					ps := utiltestingapi.MakePodSet("main", 1).
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON})
					ps.TopologyRequest = &kueue.PodSetTopologyRequest{}
					return *ps.Obj()
				}()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(firstSpreadingPath, ""),
			}.ToAggregate(),
		},
		"invalid: preferred-only topology request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					PreferredTopologyRequest(requiredTopologyLevel).
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(firstSpreadingPath, ""),
			}.ToAggregate(),
		},
		"invalid: unconstrained-only topology request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					UnconstrainedTopologyRequest().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(firstSpreadingPath, ""),
			}.ToAggregate(),
		},
		"invalid: companion required-topology annotation without structured required request": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Annotations(map[string]string{
						kueue.PodSetRequiredTopologyAnnotation:  requiredTopologyLevel,
						kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON,
					}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(firstSpreadingPath, ""),
			}.ToAggregate(),
		},
		"invalid: group members have different spreading annotations": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(
					*leaderPodSet.Clone().
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*workersPodSet.Clone().
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(secondSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"invalid: first group member is missing the spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(
					*leaderPodSet.Clone().Obj(),
					*workersPodSet.Clone().
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"invalid: later group member is missing the spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			workload: baseWorkload.Clone().
				PodSets(
					*leaderPodSet.Clone().
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
						Obj(),
					*workersPodSet.Clone().Obj(),
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(secondSpreadingPath, nil, ""),
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
	podSetsPath := field.NewPath("spec", "podSets")
	firstSpreadingPath := podSetsPath.Index(0).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	secondSpreadingPath := podSetsPath.Index(1).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	thirdSpreadingPath := podSetsPath.Index(2).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	fourthSpreadingPath := podSetsPath.Index(3).Child("template", "metadata", "annotations").Key(kueue.PodSetTopologySpreadingAnnotation)
	baseWorkload := utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNamespace).Queue("q1")
	mainPodSet := utiltestingapi.MakePodSet("main", 1).RequiredTopologyRequest(requiredTopologyLevel)
	a1PodSet := utiltestingapi.MakePodSet("a1", 1).RequiredTopologyRequest(requiredTopologyLevel)
	a2PodSet := utiltestingapi.MakePodSet("a2", 2).RequiredTopologyRequest(requiredTopologyLevel)
	b1PodSet := utiltestingapi.MakePodSet("b1", 1).RequiredTopologyRequest(requiredTopologyLevel)
	b2PodSet := utiltestingapi.MakePodSet("b2", 2).RequiredTopologyRequest(requiredTopologyLevel)
	leaderPodSet := utiltestingapi.MakePodSet("leader", 1).RequiredTopologyRequest(requiredTopologyLevel).PodSetGroup("g")
	workersPodSet := utiltestingapi.MakePodSet("workers", 2).RequiredTopologyRequest(requiredTopologyLevel).PodSetGroup("g")
	pendingInvalid := baseWorkload.Clone().PodSets(*mainPodSet.Clone().
		Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
		Obj())
	pendingValid := baseWorkload.Clone().PodSets(*mainPodSet.Clone().
		Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
		Obj())

	testCases := map[string]struct {
		featureGates  map[featuregate.Feature]bool
		before, after *kueue.Workload
		wantErr       error
	}{
		"reject changing a valid spreading annotation to invalid": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid.Clone().Obj(),
			after:        pendingInvalid.Clone().Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"exempt an unchanged invalid spreading annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid.Clone().Obj(),
			after: baseWorkload.Clone().
				Queue("q2").
				PodSets(*mainPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj()).
				Obj(),
		},
		"reject a different invalid annotation including equivalent invalid quantities": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(*mainPodSet.Clone().
				Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: shareOneJSON}).
				Obj()).
				Obj(),
			after: baseWorkload.Clone().PodSets(*mainPodSet.Clone().
				Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: shareOneMilliJSON}).
				Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath.Child("rules").Index(0).Child("maxShareAllowingPlacement"), nil, ""),
			}.ToAggregate(),
		},
		"reject absent spreading becoming a present empty annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(*utiltestingapi.MakePodSet("main", 1).
				RequiredTopologyRequest(requiredTopologyLevel).
				Obj()).Obj(),
			after: baseWorkload.Clone().PodSets(*mainPodSet.Clone().
				Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: ""}).
				Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"reject removing required topology while spreading remains": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid.Clone().Obj(),
			after: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(firstSpreadingPath, ""),
			}.ToAggregate(),
		},
		"reject a required topology change while the annotation stays invalid": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid.Clone().Obj(),
			after: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest("cloud.com/rack").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"accept a required topology change when spreading stays valid": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid.Clone().Obj(),
			after: baseWorkload.Clone().
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					RequiredTopologyRequest("cloud.com/rack").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj()).
				Obj(),
		},
		"accept an equivalent rewrite of a valid annotation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingValid.Clone().Obj(),
			after: baseWorkload.Clone().PodSets(*mainPodSet.Clone().
				Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: equivalentSpreadingJSON}).
				Obj()).
				Obj(),
		},
		"reject a membership change that introduces group disagreement": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(
				*a1PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*a2PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*b1PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
				*b2PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
			).Obj(),
			after: baseWorkload.Clone().PodSets(
				*a1PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*a2PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*b1PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
				*b2PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(fourthSpreadingPath, nil, ""),
				field.Invalid(thirdSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"repair one group while an unchanged invalid group remains": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(
				*a1PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*a2PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
				*b1PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*b2PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
			).Obj(),
			after: baseWorkload.Clone().PodSets(
				*a1PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*a2PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*b1PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*b2PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: otherSpreadingJSON}).
					Obj(),
			).Obj(),
		},
		"membership change with identical malformed annotations does not force annotation validity": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(
				*a1PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
				*a2PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
				*b1PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
				*b2PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
			).Obj(),
			after: baseWorkload.Clone().PodSets(
				*a1PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
				*a2PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
				*b1PodSet.Clone().
					PodSetGroup("g2").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
				*b2PodSet.Clone().
					PodSetGroup("g1").
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
					Obj(),
			).Obj(),
		},
		"reorder named PodSets without changing spreading inputs": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(
				*utiltestingapi.MakePodSet("extra", 1).Obj(),
				*mainPodSet.Clone().Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).Obj(),
			).Obj(),
			after: baseWorkload.Clone().PodSets(
				*mainPodSet.Clone().Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).Obj(),
				*utiltestingapi.MakePodSet("extra", 1).Obj(),
			).Obj(),
		},
		"rename a PodSet carrying invalid spreading": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid.Clone().Obj(),
			after: baseWorkload.Clone().PodSets(*utiltestingapi.MakePodSet("other", 1).
				RequiredTopologyRequest(requiredTopologyLevel).
				Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
				Obj()).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(firstSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"adding an unannotated standalone PodSet does not revalidate old spreading errors": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid.Clone().Obj(),
			after: baseWorkload.Clone().PodSets(
				*mainPodSet.Clone().Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).Obj(),
				*utiltestingapi.MakePodSet("extra", 1).Obj(),
			).Obj(),
		},
		"reject removing spreading from only one member of a pair": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(
				*leaderPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*workersPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
			).Obj(),
			after: baseWorkload.Clone().PodSets(
				*leaderPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*workersPodSet.Clone().Obj(),
			).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(secondSpreadingPath, nil, ""),
			}.ToAggregate(),
		},
		"accept removing spreading from all members of a pair together": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before: baseWorkload.Clone().PodSets(
				*leaderPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
				*workersPodSet.Clone().
					Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: validSpreadingJSON}).
					Obj(),
			).Obj(),
			after: baseWorkload.Clone().PodSets(
				*leaderPodSet.Clone().Obj(),
				*workersPodSet.Clone().Obj(),
			).Obj(),
		},
		"exemption does not skip other Workload validation": {
			featureGates: map[featuregate.Feature]bool{features.TASTopologySpreading: true},
			before:       pendingInvalid.Clone().Obj(),
			after: baseWorkload.Clone().
				PodSets(
					*utiltestingapi.MakePodSet("main", 2).
						RequiredTopologyRequest(requiredTopologyLevel).
						Annotations(map[string]string{kueue.PodSetTopologySpreadingAnnotation: invalidSpreadingJSON}).
						SetMinimumCount(1).
						Obj(),
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
			after: pendingInvalid.Clone().Obj(),
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
