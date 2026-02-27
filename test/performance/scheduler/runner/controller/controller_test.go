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

package controller

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/performance/scheduler/runner/recorder"
	"sigs.k8s.io/kueue/test/util"
)

func TestCreatePredicate(t *testing.T) {
	cases := []struct {
		name   string
		object client.Object
		want   bool
	}{
		{
			name:   "non-workload (ClusterQueue) passes through",
			object: &kueue.ClusterQueue{},
			want:   true,
		},
		{
			name:   "pending workload does not trigger reconcile",
			object: utiltesting.MakeWorkload("wl-pending", "default").Obj(),
			want:   false,
		},
		{
			// Simulates a workload admitted during the informer watch gap that
			// appears for the first time via a relist-driven Create event.
			name: "admitted workload triggers reconcile (relist scenario)",
			object: utiltesting.MakeWorkload("wl-admitted", "default").
				Admission(&kueue.Admission{ClusterQueue: "test-cq"}).
				AdmittedAt(true, time.Now().Add(-time.Second)).
				Obj(),
			want: true,
		},
		{
			name: "admitted-and-finished workload does not trigger reconcile",
			object: utiltesting.MakeWorkload("wl-finished", "default").
				Admission(&kueue.Admission{ClusterQueue: "test-cq"}).
				AdmittedAt(true, time.Now().Add(-time.Second)).
				Finished().
				Obj(),
			want: false,
		},
	}

	// recorder.New() creates a non-running recorder;
	// RecordWorkloadState() is a no-op when running == false.
	rec := recorder.New(time.Minute)
	r := &reconciler{
		admissionTime: map[types.UID]time.Time{},
		recorder:      rec,
		clock:         util.RealClock,
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := r.Create(event.CreateEvent{Object: tc.object})
			if got != tc.want {
				t.Errorf("Create() = %v, want %v", got, tc.want)
			}
		})
	}
}
