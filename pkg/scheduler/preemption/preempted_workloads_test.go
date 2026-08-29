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
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestWorkloadsToRemove(t *testing.T) {
	original := workloadInfoForTest("ns", "victim-a")
	replacement := workloadInfoForTest("ns", "victim-a")
	additional := workloadInfoForTest("ns", "victim-b")
	originalKey := workload.Key(original.Obj)
	additionalKey := workload.Key(additional.Obj)

	preempted := PreemptedWorkloads{originalKey: original}
	got := preempted.MergeWithTargets([]*Target{
		{WorkloadInfo: replacement},
		{WorkloadInfo: additional},
	}).Workloads()

	if len(got) != 2 {
		t.Fatalf("WorkloadsToRemove() returned %d workloads, want 2", len(got))
	}
	gotByKey := make(PreemptedWorkloads, len(got))
	for _, info := range got {
		gotByKey[workload.Key(info.Obj)] = info
	}
	if gotByKey[originalKey] != replacement {
		t.Errorf("duplicate target was not deduplicated with the new target value")
	}
	if gotByKey[additionalKey] != additional {
		t.Errorf("new target was not included")
	}
	if len(preempted) != 1 || preempted[originalKey] != original {
		t.Errorf("WorkloadsToRemove() mutated receiver: %#v", preempted)
	}
}

func TestWorkloadsToRemoveNilReceiver(t *testing.T) {
	target := workloadInfoForTest("ns", "victim")
	var preempted PreemptedWorkloads

	got := preempted.MergeWithTargets([]*Target{{WorkloadInfo: target}}).Workloads()
	if len(got) != 1 || workload.Key(got[0].Obj) != workload.Key(target.Obj) {
		t.Fatalf("WorkloadsToRemove() = %#v, want the target workload", got)
	}
}

func workloadInfoForTest(namespace, name string) *workload.Info {
	return &workload.Info{Obj: &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}}
}

func TestPreemptedWorkloadsDelete(t *testing.T) {
	victimA := workloadInfoForTest("ns", "victim-a")
	victimB := workloadInfoForTest("ns", "victim-b")
	keyA, keyB := workload.Key(victimA.Obj), workload.Key(victimB.Obj)

	tests := map[string]struct {
		preempted PreemptedWorkloads
		remove    []*Target
		want      []workload.Reference
	}{
		"removes the only target": {
			preempted: PreemptedWorkloads{keyA: victimA},
			remove:    []*Target{{WorkloadInfo: victimA}},
			want:      nil,
		},
		"removes one target and keeps the other": {
			preempted: PreemptedWorkloads{keyA: victimA, keyB: victimB},
			remove:    []*Target{{WorkloadInfo: victimA}},
			want:      []workload.Reference{keyB},
		},
		"target that was never inserted is a no-op": {
			preempted: PreemptedWorkloads{keyB: victimB},
			remove:    []*Target{{WorkloadInfo: victimA}},
			want:      []workload.Reference{keyB},
		},
		"no targets is a no-op": {
			preempted: PreemptedWorkloads{keyB: victimB},
			remove:    nil,
			want:      []workload.Reference{keyB},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			tc.preempted.Delete(tc.remove)
			if len(tc.preempted) != len(tc.want) {
				t.Fatalf("after Delete: got %d workloads, want %d (%#v)", len(tc.preempted), len(tc.want), tc.preempted)
			}
			for _, key := range tc.want {
				if _, found := tc.preempted[key]; !found {
					t.Errorf("after Delete: %q was removed but should have been kept", key)
				}
			}
		})
	}
}

// TestPreemptedWorkloadsInsertDeleteRoundTrip asserts Delete undoes Insert, which is
// what lets processEntry release the targets it reserved when a migration is denied.
func TestPreemptedWorkloadsInsertDeleteRoundTrip(t *testing.T) {
	existing := workloadInfoForTest("ns", "already-preempted")
	reserved := workloadInfoForTest("ns", "reserved-then-released")
	preempted := PreemptedWorkloads{workload.Key(existing.Obj): existing}

	targets := []*Target{{WorkloadInfo: reserved}}
	preempted.Insert(targets)
	if !preempted.HasAny(targets) {
		t.Fatalf("Insert did not record the target")
	}
	preempted.Delete(targets)
	if preempted.HasAny(targets) {
		t.Errorf("Delete did not release the target, so it would still block later entries in the cycle")
	}
	if _, found := preempted[workload.Key(existing.Obj)]; !found {
		t.Errorf("Delete removed an unrelated workload preempted by an earlier entry")
	}
}
