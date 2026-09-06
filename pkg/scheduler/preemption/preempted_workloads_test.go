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
