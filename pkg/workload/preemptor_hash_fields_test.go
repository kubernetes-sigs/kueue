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

package workload

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestSchedulingHashPreemptorHashFields(t *testing.T) {
	base := func() *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("wl", "default").Queue("lq").Priority(10).
			Request(corev1.ResourceCPU, "1").
			Label("team", "a").Label("kueue.x-k8s.io/job-uid", "uid-1")
	}
	labels := &PreemptorHashFields{LabelKeys: []string{"team"}}
	cases := map[string]struct {
		fields      *PreemptorHashFields
		otherFields *PreemptorHashFields
		// plainOther hashes b without PreemptorHashFields.
		plainOther bool
		a, b       *kueue.Workload
		wantSame   bool
	}{
		"rules reading nothing keep the scheduling hash": {
			fields:     &PreemptorHashFields{},
			plainOther: true,
			a:          base().Obj(),
			b:          base().Obj(),
			wantSame:   true,
		},
		"a label read by a rule tells workloads apart": {
			fields: labels,
			a:      base().Obj(),
			b:      base().Label("team", "b").Obj(),
		},
		"labels no rule reads do not tell workloads apart": {
			fields:   labels,
			a:        base().Obj(),
			b:        base().Label("kueue.x-k8s.io/job-uid", "uid-2").Obj(),
			wantSame: true,
		},
		"workloads lacking the label read by a rule are equivalent": {
			fields:   labels,
			a:        utiltestingapi.MakeWorkload("a", "default").Request(corev1.ResourceCPU, "1").Obj(),
			b:        utiltestingapi.MakeWorkload("b", "default").Request(corev1.ResourceCPU, "1").Label("other", "x").Obj(),
			wantSame: true,
		},
		"a missing label differs from an empty one": {
			fields: labels,
			a:      utiltestingapi.MakeWorkload("a", "default").Request(corev1.ResourceCPU, "1").Obj(),
			b:      utiltestingapi.MakeWorkload("b", "default").Request(corev1.ResourceCPU, "1").Label("team", "").Obj(),
		},
		"the priority class tells workloads apart when a rule reads it": {
			fields: &PreemptorHashFields{PriorityClass: true},
			a:      base().WorkloadPriorityClassRef("high").Obj(),
			b:      base().Obj(),
		},
		"the base priority tells workloads apart when a rule reads it": {
			fields: &PreemptorHashFields{BasePriority: true},
			a:      base().Obj(),
			b:      base().Priority(20).Obj(),
		},
		"the LocalQueue tells workloads apart when a rule reads it": {
			fields: &PreemptorHashFields{LocalQueue: true},
			a:      base().Obj(),
			b:      utiltestingapi.MakeWorkload("wl", "other").Queue("lq").Priority(10).Request(corev1.ResourceCPU, "1").Label("team", "a").Obj(),
		},
		"fields no rule reads do not tell workloads apart": {
			fields:   labels,
			a:        base().Obj(),
			b:        utiltestingapi.MakeWorkload("wl", "other").Queue("lq-2").Priority(10).Request(corev1.ResourceCPU, "1").Label("team", "a").Obj(),
			wantSame: true,
		},
		"a workload hashed with fields of other rules differs": {
			fields:      labels,
			otherFields: &PreemptorHashFields{LabelKeys: []string{"team"}, PriorityClass: true},
			a:           base().Obj(),
			b:           base().Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
			_, log := utiltesting.ContextWithLog(t)
			otherFields := tc.fields
			if tc.otherFields != nil || tc.plainOther {
				otherFields = tc.otherFields
			}
			a, b := NewInfo(log, tc.a), NewInfo(log, tc.b)
			a.SetPreemptorHashFields(log, tc.fields)
			b.SetPreemptorHashFields(log, otherFields)
			if !validHash(a.SchedulingHash) || !validHash(b.SchedulingHash) {
				t.Fatalf("SchedulingHash = (%q, %q), want known hashes", a.SchedulingHash, b.SchedulingHash)
			}
			if gotSame := a.SchedulingHash == b.SchedulingHash; gotSame != tc.wantSame {
				t.Errorf("SchedulingHash = (%q, %q), want equal: %v", a.SchedulingHash, b.SchedulingHash, tc.wantSame)
			}
		})
	}
}

// The scheduling hash keeps the PreemptorHashFields when the Info is updated,
// and the hash is recomputed when they are set or dropped.
func TestSetPreemptorHashFields(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulingEquivalenceHashing, true)
	_, log := utiltesting.ContextWithLog(t)
	fields := &PreemptorHashFields{LabelKeys: []string{"team"}}
	wl := func(rv, team string) *kueue.Workload {
		return utiltestingapi.MakeWorkload("wl", "default").UID("uid").ResourceVersion(rv).
			Request(corev1.ResourceCPU, "1").Label("team", team).Obj()
	}
	plainA, plainB := NewInfo(log, wl("1", "a")).SchedulingHash, NewInfo(log, wl("1", "b")).SchedulingHash
	if plainA != plainB {
		t.Fatalf("SchedulingHash without PreemptorHashFields differs by an unread label: %q, %q", plainA, plainB)
	}

	info := NewInfo(log, wl("1", "a"))
	info.SetPreemptorHashFields(log, fields)
	withFieldsA := info.SchedulingHash
	if withFieldsA == plainA {
		t.Errorf("SchedulingHash = %q after setting the PreemptorHashFields, want it to change", withFieldsA)
	}
	if got := NewInfo(log, wl("1", "a"), WithPreemptorHashFields(fields)).SchedulingHash; got != withFieldsA {
		t.Errorf("SchedulingHash = %q with WithPreemptorHashFields, want %q", got, withFieldsA)
	}
	info.Update(log, wl("2", "b"))
	if info.SchedulingHash == withFieldsA || info.SchedulingHash == plainB {
		t.Errorf("SchedulingHash = %q after a relabel, want one including the new label value", info.SchedulingHash)
	}
	info.SetPreemptorHashFields(log, nil)
	if info.SchedulingHash != plainB {
		t.Errorf("SchedulingHash = %q after dropping the PreemptorHashFields, want %q", info.SchedulingHash, plainB)
	}

	skipped := NewInfo(log, wl("1", "a"), WithSkipSchedulingHash())
	skipped.SetPreemptorHashFields(log, fields)
	if skipped.SchedulingHash != "" {
		t.Errorf("SchedulingHash = %q, want it to stay skipped", skipped.SchedulingHash)
	}
}

func validHash(hash EquivalenceHash) bool {
	return hash != "" && hash != SchedulingHashUnknown
}
