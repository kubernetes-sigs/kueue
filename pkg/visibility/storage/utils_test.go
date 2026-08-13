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

package storage

import (
	"testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	visibility "sigs.k8s.io/kueue/apis/visibility/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type req struct {
	nsName      string
	queueName   string
	queryParams *visibility.PendingWorkloadOptions
}

type resp struct {
	wantErr              error
	wantPendingWorkloads []visibility.PendingWorkload
}

func TestNewPendingWorkloadPriority(t *testing.T) {
	// A Workload created when there was no global default priority class and
	// the pod had no priority class keeps a nil Spec.Priority. The endpoint
	// must resolve it to the default rather than dereferencing a nil pointer.
	noPriority := utiltestingapi.MakeWorkload("a", "ns").Obj()
	noPriority.Spec.Priority = nil

	cases := map[string]struct {
		wl           *kueue.Workload
		wantPriority int32
	}{
		"a Workload with no priority resolves to the default": {
			wl:           noPriority,
			wantPriority: constants.DefaultPriority,
		},
		"an explicit priority is preserved": {
			wl:           utiltestingapi.MakeWorkload("a", "ns").Priority(100).Obj(),
			wantPriority: 100,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := newPendingWorkload(&workload.Info{Obj: tc.wl}, 0, 0)
			if got.Priority != tc.wantPriority {
				t.Errorf("Priority = %d, want %d", got.Priority, tc.wantPriority)
			}
		})
	}
}
