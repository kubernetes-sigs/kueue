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

package afs

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

func TestUpdate(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("default", "lq")
	settleTime := time.Now()
	seedTime := settleTime.Add(time.Millisecond)

	seeded := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}
	settled := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}

	seedFn := func(entry ConsumedResourcesEntry, found bool) ConsumedResourcesEntry {
		if !found {
			return ConsumedResourcesEntry{Resources: seeded, LastUpdate: seedTime, StatusAccounted: true}
		}
		return entry
	}
	writerFn := func(entry ConsumedResourcesEntry, found bool) ConsumedResourcesEntry {
		return ConsumedResourcesEntry{Resources: settled, LastUpdate: settleTime, StatusAccounted: entry.StatusAccounted}
	}

	cases := map[string]struct {
		existing  *ConsumedResourcesEntry
		fn        func(ConsumedResourcesEntry, bool) ConsumedResourcesEntry
		wantEntry ConsumedResourcesEntry
	}{
		"passes found=false and a zero entry when absent, stores the result": {
			fn:        seedFn,
			wantEntry: ConsumedResourcesEntry{Resources: seeded, LastUpdate: seedTime, StatusAccounted: true},
		},
		"passes the current entry when present": {
			existing:  &ConsumedResourcesEntry{Resources: settled, LastUpdate: settleTime},
			fn:        seedFn,
			wantEntry: ConsumedResourcesEntry{Resources: settled, LastUpdate: settleTime},
		},
		"a writer-style update preserves StatusAccounted": {
			existing:  &ConsumedResourcesEntry{Resources: seeded, LastUpdate: seedTime, StatusAccounted: true},
			fn:        writerFn,
			wantEntry: ConsumedResourcesEntry{Resources: settled, LastUpdate: settleTime, StatusAccounted: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			consumed := NewAfsConsumedResources()
			if tc.existing != nil {
				consumed.Update(lqKey, func(ConsumedResourcesEntry, bool) ConsumedResourcesEntry { return *tc.existing })
			}

			got := consumed.Update(lqKey, tc.fn)

			if diff := cmp.Diff(tc.wantEntry, got); diff != "" {
				t.Errorf("Update() returned entry (-want,+got):\n%s", diff)
			}
			stored, found := consumed.Get(lqKey)
			if !found {
				t.Fatal("Get() found no entry after Update()")
			}
			if diff := cmp.Diff(tc.wantEntry, stored); diff != "" {
				t.Errorf("stored entry (-want,+got):\n%s", diff)
			}
		})
	}
}
