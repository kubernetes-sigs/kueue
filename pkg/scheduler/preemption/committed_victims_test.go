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
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

// noopPreemptSimulator lets the snapshot record committed victims.
type noopPreemptSimulator struct {
	simulator.SchedulerSimulator
}

func (noopPreemptSimulator) PreemptWorkload(context.Context, workload.Reference) (func() error, error) {
	return func() error { return nil }, nil
}

func TestCommittedVictimsFilter(t *testing.T) {
	vLowKey := workload.NewReference("default", "v-low")
	vMidKey := workload.NewReference("default", "v-mid")

	type step struct {
		op    string // "preempt", "unpreempt" or "reset"
		wlKey workload.Reference
	}
	cases := map[string]struct {
		incomingCPU    string
		steps          []step
		wantMayFit     bool
		wantInSnapshot []workload.Reference
	}{
		"committed victim starts removed, freeing its quota": {
			incomingCPU:    "2",
			wantMayFit:     true,
			wantInSnapshot: []workload.Reference{vLowKey},
		},
		"unpreempting the committed victim puts its quota back": {
			incomingCPU:    "2",
			steps:          []step{{"unpreempt", vMidKey}},
			wantMayFit:     false,
			wantInSnapshot: []workload.Reference{vLowKey, vMidKey},
		},
		"unpreempting the committed victim twice puts it back once": {
			incomingCPU:    "2",
			steps:          []step{{"unpreempt", vMidKey}, {"unpreempt", vMidKey}, {"preempt", vMidKey}},
			wantMayFit:     true,
			wantInSnapshot: []workload.Reference{vLowKey},
		},
		"reset removes the committed victim left unpreempted": {
			incomingCPU:    "2",
			steps:          []step{{"unpreempt", vMidKey}, {"reset", ""}},
			wantMayFit:     true,
			wantInSnapshot: []workload.Reference{vLowKey},
		},
		"other workloads are left to the wrapped strategy": {
			incomingCPU:    "4",
			steps:          []step{{"preempt", vLowKey}},
			wantMayFit:     true,
			wantInSnapshot: nil,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
			ctx, _ := utiltesting.ContextWithLog(t)
			var usageBefore resources.FlavorResourceQuantities
			var revert func()
			// As in the search for preemption targets, the strategy is built
			// with the committed victim removed from the snapshot.
			strategy, snapshot := setupPreemptionStrategy(t, tc.incomingCPU, func(snapshot *schdcache.Snapshot) {
				snapshot.SchedulerSimulator = noopPreemptSimulator{}
				cq := snapshot.ClusterQueue("cq")
				usageBefore = cq.ResourceNode.Usage.Clone()
				snapshot.AddCommittedVictims(ctx, cq.Workloads[vMidKey])
				revert = snapshot.SimulateCommittedVictimsRemoval()
			})
			cq := snapshot.ClusterQueue("cq")
			// The strategy only preempts the candidates it yielded.
			for range strategy.Candidates() {
			}
			strategy.Reset()
			if _, found := strategy.targetsByKey[vMidKey]; found {
				t.Errorf("The committed victim %s is a preemption candidate", vMidKey)
			}
			filter := NewCommittedVictimsFilter(snapshot, &strategy)

			for _, s := range tc.steps {
				switch s.op {
				case "preempt":
					filter.PreemptWorkload(s.wlKey)
				case "unpreempt":
					filter.UnpreemptWorkload(s.wlKey)
				case "reset":
					filter.Reset()
				}
			}
			if got := filter.MayFit(); got != tc.wantMayFit {
				t.Errorf("MayFit() = %v, want %v", got, tc.wantMayFit)
			}
			var gotInSnapshot []workload.Reference
			for _, key := range []workload.Reference{vLowKey, vMidKey} {
				if workloadInSnapshot(snapshot, key) {
					gotInSnapshot = append(gotInSnapshot, key)
				}
			}
			if diff := cmp.Diff(tc.wantInSnapshot, gotInSnapshot); diff != "" {
				t.Errorf("Unexpected workloads in the snapshot (-want,+got):\n%s", diff)
			}

			filter.Reset()
			strategy.Reset()
			revert()
			if diff := cmp.Diff(usageBefore, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("Unexpected usage after reverting (-want,+got):\n%s", diff)
			}
			for _, key := range []workload.Reference{vLowKey, vMidKey} {
				if !workloadInSnapshot(snapshot, key) {
					t.Errorf("Workload %s is missing from the snapshot after reverting", key)
				}
			}
		})
	}
}
