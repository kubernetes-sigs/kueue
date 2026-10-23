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

package flavorassigner

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestFindTopologyAssignmentsReplacesSliceUsage(t *testing.T) {
	cases := map[string]struct {
		gateOff             bool
		topologyGateOff     bool
		withoutPredecessor  bool
		removedPredecessor  bool
		releasedPredecessor bool
		differentUID        bool
		leader              bool
		otherCPU            string
		wantFailure         bool
	}{
		"accounts for predecessor workers exactly once":                                {},
		"accounts for predecessor leader and workers exactly once":                     {leader: true},
		"does not remove unrelated usage":                                              {otherCPU: "1", wantFailure: true},
		"does not subtract an already removed predecessor":                             {removedPredecessor: true, otherCPU: "1", wantFailure: true},
		"does not subtract usage already released while the predecessor stays present": {releasedPredecessor: true, otherCPU: "1", wantFailure: true},
		"does not subtract a different workload with the same name":                    {differentUID: true, wantFailure: true},
		"does not subtract usage without a predecessor":                                {withoutPredecessor: true, wantFailure: true},
		"does not subtract predecessor usage when the gate is off":                     {gateOff: true, wantFailure: true},
		"does not subtract predecessor usage when topology scheduling is off":          {topologyGateOff: true, wantFailure: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlicesWithTAS, !tc.gateOff)
			ctx, log := utiltesting.ContextWithLog(t)
			cq := newBookmarkSnapshot(ctx, t, log, "10", "0", kueue.FlavorFungibility{})
			workerCount := 4
			if tc.leader {
				workerCount = 3
			}
			oldPodSets := []kueue.PodSet{*utiltestingapi.MakePodSet("workers", 2).
				Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().PodSetGroup("group").Obj()}
			newPodSets := []kueue.PodSet{*utiltestingapi.MakePodSet("workers", workerCount).
				Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().PodSetGroup("group").Obj()}
			oldAssignments := []kueue.PodSetAssignment{utiltestingapi.MakePodSetAssignment("workers").Count(2).
				Assignment(corev1.ResourceCPU, "flavor-1", "2").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 2).Obj()).Obj()).Obj()}
			if tc.leader {
				leader := *utiltestingapi.MakePodSet("leader", 1).Request(corev1.ResourceCPU, "1").
					UnconstrainedTopologyRequest().PodSetGroup("group").Obj()
				oldPodSets = append(oldPodSets, leader)
				newPodSets = append(newPodSets, leader)
				oldAssignments = append(oldAssignments, utiltestingapi.MakePodSetAssignment("leader").Count(1).
					Assignment(corev1.ResourceCPU, "flavor-1", "1").
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node-1"}, 1).Obj()).Obj()).Obj())
			}
			old := utiltestingapi.MakeWorkload("old", "default").UID("old-uid").
				Annotation(constants.ElasticJobAnnotation, "true").PodSets(oldPodSets...).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(oldAssignments...).Obj(), time.Now()).
				AdmittedAt(true, time.Now()).Obj()
			oldInfo := workload.NewInfo(log, old)
			cq.Workloads = map[workload.Reference]*workload.Info{workload.Key(old): oldInfo}
			cq.AddUsage(oldInfo.Usage())
			snapshot := &schdcache.Snapshot{Manager: hierarchy.NewManagerForTest(
				map[kueue.CohortReference]*schdcache.CohortSnapshot{},
				map[kueue.ClusterQueueReference]*schdcache.ClusterQueueSnapshot{"cq": cq},
			)}
			if tc.removedPredecessor {
				restore := snapshot.SimulateWorkloadRemoval([]*workload.Info{oldInfo})
				defer restore()
			}
			if tc.releasedPredecessor {
				snapshot.ReleaseWorkloadUsage(oldInfo)
				if cq.Workloads[workload.Key(old)] == nil {
					t.Fatal("released predecessor should remain present")
				}
			}
			if tc.differentUID {
				cached := old.DeepCopy()
				cached.UID = "new-uid"
				cq.Workloads[workload.Key(old)] = workload.NewInfo(log, cached)
			}
			if tc.otherCPU != "" {
				cq.AddUsage(workload.Usage{TAS: nodeUsageOnFlavorOne(tc.otherCPU)})
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("new", "default").
				Annotation(constants.ElasticJobAnnotation, "true").PodSets(newPodSets...).Obj())
			predecessor := oldInfo
			if tc.withoutPredecessor {
				predecessor = nil
			}
			assignment := Assignment{replaceWorkloadSlice: predecessor}
			for _, ps := range newPodSets {
				assignment.PodSets = append(assignment.PodSets, PodSetAssignment{
					Name: ps.Name, Count: ps.Count,
					Requests: corev1.ResourceList{corev1.ResourceCPU: *resource.NewQuantity(int64(ps.Count), resource.DecimalSI)},
					Flavors:  ResourceAssignment{corev1.ResourceCPU: {Name: "flavor-1", Mode: Fit}},
				})
			}
			tasRequests := assignment.WorkloadsTopologyRequests(log, incoming, cq)
			if tc.topologyGateOff {
				features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, false)
			}
			before, err := cq.TASFlavors["flavor-1"].SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("reading TAS capacity: %v", err)
			}
			quotaBefore := cq.ResourceNode.Usage.Clone()
			result := assignment.FindTopologyAssignments(ctx, cq, tasRequests, schdcache.WithWorkloadInfo(incoming))
			if got := result.Failure() != nil; got != tc.wantFailure {
				t.Errorf("topology failure=%v, want %v: %v", got, tc.wantFailure, result.Failure())
			}
			after, err := cq.TASFlavors["flavor-1"].SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("reading restored TAS capacity: %v", err)
			}
			if before != after {
				t.Errorf("placement changed TAS capacity: before=%s after=%s", before, after)
			}
			if diff := cmp.Diff(quotaBefore, cq.ResourceNode.Usage); diff != "" {
				t.Errorf("placement changed quota usage (-want +got):\n%s", diff)
			}
		})
	}
}
