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

package classical

import (
	"sort"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCandidatesOrdering(t *testing.T) {
	now := time.Now()
	candidates := []*workload.Info{
		workload.NewInfo(utiltesting.MakeWorkload("high", "").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now).
			Priority(10).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("low", "").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now).
			Priority(-10).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("other", "").
			ReserveQuotaAt(utiltesting.MakeAdmission("other").Obj(), now).
			Priority(10).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("evicted", "").
			SetOrReplaceCondition(metav1.Condition{
				Type:               kueue.WorkloadEvicted,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(now),
			}).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("old-a", "").
			UID("old-a").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("old-b", "").
			UID("old-b").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now).
			Obj()),
		workload.NewInfo(utiltesting.MakeWorkload("current", "").
			ReserveQuotaAt(utiltesting.MakeAdmission("self").Obj(), now.Add(time.Second)).
			Obj()),
	}
	sort.Slice(candidates, CandidatesOrdering(candidates, "self", now))
	gotNames := make([]string, len(candidates))
	for i, c := range candidates {
		gotNames[i] = workload.Key(c.Obj)
	}
	wantCandidates := []string{"/evicted", "/other", "/low", "/current", "/old-a", "/old-b", "/high"}
	if diff := cmp.Diff(wantCandidates, gotNames); diff != "" {
		t.Errorf("Sorted with wrong order (-want,+got):\n%s", diff)
	}
}
