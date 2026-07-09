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

func TestSetIfAbsent(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("default", "lq")
	seedTime := time.Now()
	settleTime := seedTime.Add(-time.Millisecond)

	seeded := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}
	settled := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}

	cases := map[string]struct {
		existing    *ConsumedResourcesEntry
		wantEntry   ConsumedResourcesEntry
		wantExisted bool
	}{
		"stores the seeded value when no entry exists": {
			wantEntry:   ConsumedResourcesEntry{Resources: seeded, LastUpdate: seedTime},
			wantExisted: false,
		},
		"does not overwrite an entry created concurrently": {
			existing:    &ConsumedResourcesEntry{Resources: settled, LastUpdate: settleTime},
			wantEntry:   ConsumedResourcesEntry{Resources: settled, LastUpdate: settleTime},
			wantExisted: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			consumed := NewAfsConsumedResources()
			if tc.existing != nil {
				consumed.Set(lqKey, tc.existing.Resources, tc.existing.LastUpdate)
			}

			gotEntry, gotExisted := consumed.SetIfAbsent(lqKey, seeded, seedTime)

			if gotExisted != tc.wantExisted {
				t.Errorf("SetIfAbsent() existed = %t, want %t", gotExisted, tc.wantExisted)
			}
			if diff := cmp.Diff(tc.wantEntry, gotEntry); diff != "" {
				t.Errorf("SetIfAbsent() returned entry (-want,+got):\n%s", diff)
			}
			stored, found := consumed.Get(lqKey)
			if !found {
				t.Fatal("Get() found no entry after SetIfAbsent()")
			}
			if diff := cmp.Diff(tc.wantEntry, stored); diff != "" {
				t.Errorf("stored entry (-want,+got):\n%s", diff)
			}
		})
	}
}
