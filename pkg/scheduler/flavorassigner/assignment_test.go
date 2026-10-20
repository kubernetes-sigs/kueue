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
	cases := map[string]struct {
		observability   bool
		failures        map[kueue.PodSetReference]*Status
		wantMode        FlavorAssignmentMode
		wantAttemptMode FlavorAssignmentMode
		wantStatuses    []Status
		wantReasons     []string
	}{
		"multiple failed PodSets update only assigned flavor attempts": {
			observability: true, failures: failures,
			wantMode: NoFit, wantAttemptMode: NoFit,
			wantStatuses: []Status{*leaderStatus, *workerStatus},
			wantReasons:  []string{kueue.WorkloadQuotaReservedReasonSimulationFailed, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed},
		},
		"observability disabled preserves attempts": {
			failures: failures,
			wantMode: NoFit, wantAttemptMode: Fit,
			wantStatuses: []Status{*leaderStatus, *workerStatus},
			wantReasons:  []string{"", ""},
		},
		"no failures preserves assignments and attempts": {
			observability: true,
			wantMode:      Fit, wantAttemptMode: Fit,
			wantStatuses: []Status{{}, {}}, wantReasons: []string{"", ""},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.UnadmittedWorkloadsObservability, tc.observability)
			assignment := Assignment{representativeMode: new(Fit)}
			want := Assignment{representativeMode: new(tc.wantMode)}
			for i, psName := range []kueue.PodSetReference{"leader", "workers"} {
				assignment.PodSets = append(assignment.PodSets, PodSetAssignment{
					Name: psName,
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
				})
				want.PodSets = append(want.PodSets, PodSetAssignment{
					Name: psName, Status: tc.wantStatuses[i],
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "compute", Mode: tc.wantMode},
						corev1.ResourceMemory: {Name: "compute", Mode: tc.wantMode},
						"example.com/gpu":     {Name: "gpu", Mode: tc.wantMode},
						corev1.ResourcePods:   {Name: "unrecorded", Mode: tc.wantMode},
					},
					FlavorAssignmentAttempts: []FlavorAssignmentAttempt{
						{Flavor: "unused", Mode: NoFit, NoFitReason: kueue.WorkloadQuotaReservedReasonExceedsMaxQuota},
						{Flavor: "gpu", Mode: tc.wantAttemptMode, NoFitReason: tc.wantReasons[i], Borrow: 2, Reasons: []string{"borrowed quota"}},
						{Flavor: "compute", Mode: tc.wantAttemptMode, NoFitReason: tc.wantReasons[i]},
						{Flavor: "compute", Mode: Fit, Reasons: []string{"later attempt"}},
					},
				})
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
			}, PodSetAssignment{Name: "no-attempts", Status: tc.wantStatuses[0]})

			assignment.ResolvePodSetFailures(tc.failures, NoFit)

			if diff := cmp.Diff(want, assignment, cmp.AllowUnexported(Assignment{}, Status{}, FlavorAssignment{}), cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected assignment (-want +got):\n%s", diff)
			}
		})
	}
}
