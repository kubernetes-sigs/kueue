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

package tas_test

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestAssignedDomainSetIsTopologyGatedOrInAssignedDomain(t *testing.T) {
	assignment := utiltestingapi.MakeTopologyAssignment([]string{"block", "rack"}).
		Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).Obj()
	truncatedAssignment := &kueue.TopologyAssignment{Levels: []string{"block", "rack"}}
	basePod := testingpod.MakePod("pod", "ns")

	testCases := map[string]struct {
		assignment *kueue.TopologyAssignment
		pod        *corev1.Pod
		want       bool
	}{
		"nil assignment accepts topology-ungated pod": {
			pod:  basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			want: true,
		},
		"truncated empty assignment rejects topology-ungated pod": {
			assignment: truncatedAssignment,
			pod:        basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			want:       false,
		},
		"topology gated pod without domain": {
			assignment: assignment,
			pod:        basePod.Clone().TopologySchedulingGate().Obj(),
			want:       true,
		},
		"topology gated pod outside assignment": {
			assignment: assignment,
			pod:        basePod.Clone().TopologySchedulingGate().NodeSelector("block", "b1").NodeSelector("rack", "r0").Obj(),
			want:       true,
		},
		"topology-ungated pod in assigned domain": {
			assignment: assignment,
			pod:        basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			want:       true,
		},
		"topology-ungated pod outside assignment": {
			assignment: assignment,
			pod:        basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r0").Obj(),
			want:       false,
		},
		"elastic gate alone does not retain pod outside assignment": {
			assignment: assignment,
			pod:        basePod.Clone().Gate(kueue.ElasticJobSchedulingGate).NodeSelector("block", "b1").NodeSelector("rack", "r0").Obj(),
			want:       false,
		},
		"topology-ungated pod missing level selector": {
			assignment: assignment,
			pod:        basePod.Clone().Obj(),
			want:       false,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			domains := utiltas.NewAssignedDomainSet(tc.assignment)
			if got := domains.IsTopologyGatedOrInAssignedDomain(log, tc.pod); got != tc.want {
				t.Errorf("IsTopologyGatedOrInAssignedDomain() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAssignedDomainSetTryReservePodDomain(t *testing.T) {
	assignment := utiltestingapi.MakeTopologyAssignment([]string{"block", "rack"}).
		Domains(utiltestingapi.MakeTopologyDomainAssignment([]string{"b1", "r1"}, 1).Obj()).Obj()
	basePod := testingpod.MakePod("pod", "ns")

	testCases := map[string]struct {
		assignment *kueue.TopologyAssignment
		consumed   []*corev1.Pod
		candidates []*corev1.Pod
		want       []bool
	}{
		"nil assignment always allows": {
			candidates: []*corev1.Pod{
				basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r0").Obj(),
			},
			want: []bool{true},
		},
		"topology-gated candidate always allowed": {
			assignment: assignment,
			consumed: []*corev1.Pod{
				basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			},
			candidates: []*corev1.Pod{
				basePod.Clone().Gate(kueue.ElasticJobSchedulingGate, kueue.TopologySchedulingGate).Obj(),
			},
			want: []bool{true},
		},
		"candidate without topology level labels allowed despite non-nil assignment": {
			assignment: assignment,
			consumed: []*corev1.Pod{
				basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			},
			candidates: []*corev1.Pod{
				basePod.Clone().Gate(kueue.ElasticJobSchedulingGate).Obj(),
			},
			want: []bool{true},
		},
		"candidate in removed domain rejected": {
			assignment: assignment,
			candidates: []*corev1.Pod{
				basePod.Clone().Gate(kueue.ElasticJobSchedulingGate).NodeSelector("block", "b1").NodeSelector("rack", "r0").Obj(),
			},
			want: []bool{false},
		},
		"candidate in already full domain rejected": {
			assignment: assignment,
			consumed: []*corev1.Pod{
				basePod.Clone().NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			},
			candidates: []*corev1.Pod{
				basePod.Clone().Gate(kueue.ElasticJobSchedulingGate).NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			},
			want: []bool{false},
		},
		"only first stale candidate in 1-slot domain accepted": {
			assignment: assignment,
			candidates: []*corev1.Pod{
				basePod.Clone().Name("c1").Gate(kueue.ElasticJobSchedulingGate).NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
				basePod.Clone().Name("c2").Gate(kueue.ElasticJobSchedulingGate).NodeSelector("block", "b1").NodeSelector("rack", "r1").Obj(),
			},
			want: []bool{true, false},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			domains := utiltas.NewAssignedDomainSet(tc.assignment)
			for _, pod := range tc.consumed {
				domains.ConsumePodDomain(pod)
			}
			for i, candidate := range tc.candidates {
				if got := domains.TryReservePodDomain(log, candidate); got != tc.want[i] {
					t.Errorf("candidate[%d] TryReservePodDomain() = %v, want %v", i, got, tc.want[i])
				}
			}
		})
	}
}
