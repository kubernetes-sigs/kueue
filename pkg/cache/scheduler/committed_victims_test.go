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

package scheduler

import (
	"context"
	"errors"
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// preemptRecordingSimulator records the Workloads removed from the simulator.
type preemptRecordingSimulator struct {
	simulator.SchedulerSimulator
	preempted  []workload.Reference
	preemptErr error
}

func (s *preemptRecordingSimulator) PreemptWorkload(_ context.Context, wlKey workload.Reference) (func() error, error) {
	s.preempted = append(s.preempted, wlKey)
	return func() error { return nil }, s.preemptErr
}

func TestSnapshotCommittedVictims(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	admitted := func(name, cpu string) kueue.Workload {
		return *utiltestingapi.MakeWorkload(name, "ns").
			Request(corev1.ResourceCPU, cpu).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", cpu).
					Obj()).
				Obj(), now).
			Obj()
	}
	cpu := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	cases := map[string]struct {
		deepIntegration bool
		preemptErr      error
		// absent are the victims removed from the snapshot before simulating
		// the removal of the committed victims.
		absent []workload.Reference
		// add are the batches of victims passed to AddCommittedVictims.
		add [][]workload.Reference

		wantVictims   []workload.Reference
		wantPreempted []workload.Reference
		// wantRemovedUsage is the CPU usage removed while the committed victims
		// are removed from the snapshot.
		wantRemovedUsage int64
	}{
		"deep integration disabled: victims are not recorded": {
			add: [][]workload.Reference{{"ns/a"}},
		},
		"victims are recorded once and their Pods are removed from the simulator once": {
			deepIntegration:  true,
			add:              [][]workload.Reference{{"ns/a", "ns/b"}, {"ns/b"}},
			wantVictims:      []workload.Reference{"ns/a", "ns/b"},
			wantPreempted:    []workload.Reference{"ns/a", "ns/b"},
			wantRemovedUsage: 3_000,
		},
		"victims are recorded even if their Pods cannot be removed from the simulator": {
			deepIntegration:  true,
			preemptErr:       errors.New("injected error"),
			add:              [][]workload.Reference{{"ns/a"}},
			wantVictims:      []workload.Reference{"ns/a"},
			wantPreempted:    []workload.Reference{"ns/a"},
			wantRemovedUsage: 1_000,
		},
		"victims absent from the snapshot are not removed again": {
			deepIntegration:  true,
			absent:           []workload.Reference{"ns/b"},
			add:              [][]workload.Reference{{"ns/a", "ns/b"}},
			wantVictims:      []workload.Reference{"ns/a", "ns/b"},
			wantPreempted:    []workload.Reference{"ns/a", "ns/b"},
			wantRemovedUsage: 1_000,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, tc.deepIntegration)
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: []kueue.Workload{admitted("a", "1"), admitted("b", "2"), admitted("c", "3")}}).
				Build()
			cqCache := New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cqCache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()); err != nil {
				t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
			}
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot() returned error: %v", err)
			}
			sim := &preemptRecordingSimulator{preemptErr: tc.preemptErr}
			snap.SchedulerSimulator = sim
			cq := snap.ClusterQueue("cq")
			infos := maps.Clone(cq.Workloads)

			for _, batch := range tc.add {
				victims := make([]*workload.Info, 0, len(batch))
				for _, key := range batch {
					victims = append(victims, infos[key])
				}
				snap.AddCommittedVictims(ctx, victims...)
			}
			if diff := cmp.Diff(tc.wantVictims, slices.Sorted(maps.Keys(snap.CommittedVictims().Workloads))); diff != "" {
				t.Errorf("Unexpected committed victims (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantPreempted, sim.preempted); diff != "" {
				t.Errorf("Unexpected Workloads removed from the simulator (-want,+got):\n%s", diff)
			}

			for _, key := range tc.absent {
				snap.RemoveWorkload(infos[key])
			}
			usageBefore := cq.ResourceNode.Usage[cpu]
			revert := snap.SimulateCommittedVictimsRemoval()
			if got := usageBefore.Sub(cq.ResourceNode.Usage[cpu]); got.CmpInt64(tc.wantRemovedUsage) != 0 {
				t.Errorf("Unexpected usage removed with the committed victims: got %v, want %d", got, tc.wantRemovedUsage)
			}
			for key := range snap.CommittedVictims().Workloads {
				if snap.HasWorkload(infos[key]) {
					t.Errorf("Committed victim %s is still in the snapshot", key)
				}
			}
			revert()
			if got := cq.ResourceNode.Usage[cpu]; got.Cmp(usageBefore) != 0 {
				t.Errorf("Unexpected usage after reverting the removal: got %v, want %v", got, usageBefore)
			}
			for key := range infos {
				if want := !slices.Contains(tc.absent, key); snap.HasWorkload(infos[key]) != want {
					t.Errorf("HasWorkload(%s) = %v after reverting the removal, want %v", key, !want, want)
				}
			}
		})
	}
}
