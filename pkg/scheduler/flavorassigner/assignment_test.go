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
	"maps"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
)

func TestResolvePodSetFailures(t *testing.T) {
	leaderStatus := NewStatus("leader failed").WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed)
	workerStatus := NewStatus("workers failed").WithError(errors.New("placement error")).
		WithNoFitReason(kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
	failures := map[kueue.PodSetReference]*Status{
		"leader":      leaderStatus,
		"workers":     workerStatus,
		"no-attempts": leaderStatus,
	}
	basePodSetAssignment := PodSetAssignment{
		Flavors: ResourceAssignment{
			corev1.ResourceCPU:    {Name: "compute", Mode: Fit},
			corev1.ResourceMemory: {Name: "compute", Mode: Fit},
			"example.com/gpu":     {Name: "gpu", Mode: Fit},
			corev1.ResourcePods:   {Name: "unrecorded", Mode: Fit},
		},
		FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
			{Flavor: "unused", Mode: NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota},
			{Flavor: "gpu", Mode: Fit, Borrow: 2, Reasons: []string{"borrowed quota"}},
			{Flavor: "compute", Mode: Fit},
			{Flavor: "compute", Mode: Fit, Reasons: []string{"later attempt"}},
		},
	}
	makePodSetAssignment := func(name kueue.PodSetReference, status Status, mode, attemptMode FlavorAssignmentMode, reason string) PodSetAssignment {
		psa := basePodSetAssignment
		psa.Name = name
		psa.Status = status
		psa.Flavors = make(ResourceAssignment, len(basePodSetAssignment.Flavors))
		for resource, flavor := range basePodSetAssignment.Flavors {
			assignment := *flavor
			assignment.Mode = mode
			psa.Flavors[resource] = &assignment
		}
		psa.FlavorAssignmentAttempts = slices.Clone(basePodSetAssignment.FlavorAssignmentAttempts)
		for i := range psa.FlavorAssignmentAttempts {
			psa.FlavorAssignmentAttempts[i].Reasons = slices.Clone(psa.FlavorAssignmentAttempts[i].Reasons)
		}
		psa.FlavorAssignmentAttempts[1].Mode = attemptMode
		psa.FlavorAssignmentAttempts[1].NoFitReason = reason
		psa.FlavorAssignmentAttempts[2].Mode = attemptMode
		psa.FlavorAssignmentAttempts[2].NoFitReason = reason
		return psa
	}
	cases := map[string]struct {
		observability        bool
		failures             map[kueue.PodSetReference]*Status
		wantMode             FlavorAssignmentMode
		wantAttemptMode      FlavorAssignmentMode
		wantStatuses         []Status
		wantNoAttemptsStatus Status
		wantReasons          []string
	}{
		"when multiple failed PodSets update only assigned flavor attempts": {
			observability:        true,
			failures:             failures,
			wantMode:             NoFit,
			wantAttemptMode:      NoFit,
			wantStatuses:         []Status{*leaderStatus, *workerStatus},
			wantNoAttemptsStatus: *leaderStatus,
			wantReasons:          []string{kueue.WorkloadQuotaReservedReasonSimulationFailed, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed},
		},
		"observability disabled preserves attempts": {
			failures:             failures,
			wantMode:             NoFit,
			wantAttemptMode:      Fit,
			wantStatuses:         []Status{*leaderStatus, *workerStatus},
			wantNoAttemptsStatus: *leaderStatus,
			wantReasons:          []string{"", ""},
		},
		"failures in the prefix preserve remaining PodSets": {
			observability: true,
			failures: map[kueue.PodSetReference]*Status{
				"leader":  leaderStatus,
				"workers": workerStatus,
			},
			wantMode:        NoFit,
			wantAttemptMode: NoFit,
			wantStatuses:    []Status{*leaderStatus, *workerStatus},
			wantReasons:     []string{kueue.WorkloadQuotaReservedReasonSimulationFailed, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed},
		},
		"unknown failure names do not prevent resolving known failures": {
			observability: true,
			failures: map[kueue.PodSetReference]*Status{
				"leader":      leaderStatus,
				"workers":     workerStatus,
				"no-attempts": leaderStatus,
				"unknown":     leaderStatus,
			},
			wantMode:             NoFit,
			wantAttemptMode:      NoFit,
			wantStatuses:         []Status{*leaderStatus, *workerStatus},
			wantNoAttemptsStatus: *leaderStatus,
			wantReasons:          []string{kueue.WorkloadQuotaReservedReasonSimulationFailed, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed},
		},
		"no failures preserves assignments and attempts": {
			observability:   true,
			wantMode:        Fit,
			wantAttemptMode: Fit,
			wantStatuses:    []Status{{}, {}},
			wantReasons:     []string{"", ""},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, tc.observability)
			assignment := Assignment{representativeMode: new(Fit)}
			want := Assignment{representativeMode: new(tc.wantMode)}
			for i, psName := range []kueue.PodSetReference{"leader", "workers"} {
				assignment.PodSets = append(assignment.PodSets, makePodSetAssignment(psName, Status{}, Fit, Fit, ""))
				want.PodSets = append(want.PodSets, makePodSetAssignment(psName, tc.wantStatuses[i], tc.wantMode, tc.wantAttemptMode, tc.wantReasons[i]))
			}
			assignment.PodSets = append(assignment.PodSets, PodSetAssignment{
				Name:                     "healthy",
				Flavors:                  ResourceAssignment{corev1.ResourceCPU: {Name: "compute", Mode: Fit}},
				FlavorAssignmentAttempts: []FlavorAssignmentAttempt{{Flavor: "compute", Mode: Fit}},
			}, PodSetAssignment{Name: "no-attempts"})
			want.PodSets = append(want.PodSets, PodSetAssignment{
				Name:                     "healthy",
				Flavors:                  ResourceAssignment{corev1.ResourceCPU: {Name: "compute", Mode: Fit}},
				FlavorAssignmentAttempts: []FlavorAssignmentAttempt{{Flavor: "compute", Mode: Fit}},
			}, PodSetAssignment{Name: "no-attempts", Status: tc.wantNoAttemptsStatus})

			wantFailures := maps.Clone(tc.failures)
			assignment.ResolvePodSetFailures(tc.failures, NoFit)

			if diff := cmp.Diff(wantFailures, tc.failures, cmp.AllowUnexported(Status{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected changes to failures (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(want, assignment, cmp.AllowUnexported(Assignment{}, Status{}, FlavorAssignment{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected assignment (-want +got):\n%s", diff)
			}
		})
	}
}
