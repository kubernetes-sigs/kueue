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
	"maps"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPartialAdmissionWithResidualTASPods(t *testing.T) {
	cases := map[string]struct {
		victimCount   int32
		deleting      bool
		partial       bool
		wantMode      flavorassigner.FlavorAssignmentMode
		wantCount     int32
		wantWaiting   bool
		wantPreempted bool
	}{
		"wait rather than preempt another victim": {victimCount: 2, deleting: true, partial: true, wantMode: flavorassigner.DeferredFit, wantCount: 3, wantWaiting: true},
		"allow partial fit without preemption":    {victimCount: 1, deleting: true, partial: true, wantMode: flavorassigner.Fit, wantCount: 1},
		"allow ordinary partial preemption":       {victimCount: 2, partial: true, wantMode: flavorassigner.Preempt, wantCount: 2, wantPreempted: true},
		"partial admission disabled":              {victimCount: 2, deleting: true, wantMode: flavorassigner.DeferredFit, wantCount: 3, wantWaiting: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.PartialAdmission, tc.partial)
			features.SetFeatureGateDuringTest(t, features.FlavorFungibility, true)
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewFakeClient()
			cache := schdcache.New(cl)
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			for _, flavor := range []string{"a", "b"} {
				cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor(flavor).
					NodeLabel("group", flavor).TopologyName("topology").Obj())
				cpu := "2"
				if flavor == "b" {
					cpu = "3"
				}
				cache.TASCache().SyncNode(testingnode.MakeNode(flavor).
					Label("group", flavor).Label(corev1.LabelHostname, flavor).
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourcePods: resource.MustParse("32")}).Ready().Obj())
			}
			cq := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("a").Resource(corev1.ResourceCPU, "2").Obj(),
					*utiltestingapi.MakeFlavorQuotas("b").Resource(corev1.ResourceCPU, "10").Obj()).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanPreempt: kueue.MayStopSearch}).Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			victim := utiltestingapi.MakeWorkload("victim", "ns").Priority(0).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, int(tc.victimCount)).
					Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Count(tc.victimCount).Assignment(corev1.ResourceCPU, "a", resource.NewQuantity(int64(tc.victimCount), resource.DecimalSI).String()).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"a"}, tc.victimCount).Obj()).Obj()).Obj()).Obj(), time.Now()).Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, victim) {
				t.Fatal("adding victim reservation")
			}
			pod := testingpod.MakePod("old", "ns").NodeName("b").Request(corev1.ResourceCPU, "3").Obj()
			pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "released"}
			if tc.deleting {
				now := metav1.Now()
				pod.DeletionTimestamp = &now
			}
			cache.TASCache().UpdateTASPodUsage(pod, log)
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "ns").Priority(10).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).SetMinimumCount(1).
					Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj()).Obj())
			incoming.ClusterQueue = "cq"
			beforeUsage := maps.Clone(snap.ClusterQueue("cq").ResourceNode.Usage)
			capacity := func() map[kueue.ResourceFlavorReference]string {
				t.Helper()
				result := make(map[kueue.ResourceFlavorReference]string)
				for flavor, tasFlavor := range snap.ClusterQueue("cq").TASFlavors {
					value, err := tasFlavor.SerializeFreeCapacityPerDomain()
					if err != nil {
						t.Fatalf("serializing TAS capacity: %v", err)
					}
					result[flavor] = value
				}
				return result
			}
			beforeCapacity := capacity()
			s := New(nil, cache, cl, &utiltesting.EventRecorder{})
			asgn, targets := s.getAssignments(ctx, incoming, snap)
			if got := asgn.RepresentativeMode(); got != tc.wantMode {
				t.Fatalf("mode=%v, want %v", got, tc.wantMode)
			}
			if asgn.PodSets[0].Count != tc.wantCount || asgn.WaitingForResidualTASPods != tc.wantWaiting {
				t.Fatalf("count=%d, waiting=%t; want count=%d, waiting=%t", asgn.PodSets[0].Count, asgn.WaitingForResidualTASPods, tc.wantCount, tc.wantWaiting)
			}
			if tc.wantPreempted {
				if len(targets) != 1 || targets[0].WorkloadInfo.Obj.Name != victim.Name {
					t.Fatalf("expected victim preemption, got %v", targets)
				}
			} else if len(targets) != 0 {
				t.Fatalf("unexpected preemption targets: %v", targets)
			}
			if len(snap.ClusterQueue("cq").Workloads) != 1 {
				t.Fatal("assignment probe removed victim reservation from snapshot")
			}
			if diff := cmp.Diff(beforeUsage, snap.ClusterQueue("cq").ResourceNode.Usage); diff != "" {
				t.Fatalf("assignment probe changed quota usage (-before,+after):\n%s", diff)
			}
			if diff := cmp.Diff(beforeCapacity, capacity()); diff != "" {
				t.Fatalf("assignment probe changed TAS capacity (-before,+after):\n%s", diff)
			}
		})
	}
}

func TestMultiVictimPreemptionWithResidualTASPods(t *testing.T) {
	cases := map[string]struct {
		released    []string
		midEvicted  bool
		count       int
		quota       string
		wantWaiting bool
		wantNoFit   bool
	}{
		"first victim releases quota":                        {released: []string{"low"}, midEvicted: true, count: 2, wantWaiting: true},
		"second victim releases quota first":                 {released: []string{"mid"}, midEvicted: true, count: 2, wantWaiting: true},
		"both victims release quota":                         {released: []string{"low", "mid"}, midEvicted: true, count: 2, wantWaiting: true},
		"quota fits but another victim still holds topology": {released: []string{"low"}, midEvicted: true, count: 2, quota: "20", wantWaiting: true},
		"a new victim is still needed for quota":             {released: []string{"low"}, count: 2},
		"existing victims cannot free enough capacity":       {released: []string{"low"}, midEvicted: true, count: 3, wantNoFit: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewFakeClient()
			cache := schdcache.New(cl)
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas").
				NodeLabel("group", "tas").TopologyName("topology").Obj())
			quota := tc.quota
			if quota == "" {
				quota = "15"
			}
			cq := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas").Resource(corev1.ResourceCPU, quota).Obj()).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			victims := make(map[string]*kueue.Workload)
			for i, name := range []string{"low", "mid", "other"} {
				cache.TASCache().SyncNode(testingnode.MakeNode(name).
					Label("group", "tas").Label(corev1.LabelHostname, name).
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5"), corev1.ResourcePods: resource.MustParse("32")}).Ready().Obj())
				wl := utiltestingapi.MakeWorkload(name, "ns").UID(types.UID(name)).Priority(int32(i+1)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "5").UnconstrainedTopologyRequest().Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "tas", "5").
						TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
							Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{name}, 1).Obj()).Obj()).Obj()).Obj(), time.Now())
				if name == "low" || (name == "mid" && tc.midEvicted) {
					wl.EvictedAt(time.Now())
				}
				victims[name] = wl.Obj()
				if !cache.AddOrUpdateWorkload(ctx, log, wl.Obj()) {
					t.Fatalf("adding %s reservation", name)
				}
				pod := testingpod.MakePod(name, "ns").UID(name+"-pod").
					NodeName(name).Request(corev1.ResourceCPU, "5").
					Annotation(kueue.WorkloadAnnotation, name).Obj()
				cache.TASCache().UpdateTASPodUsage(pod, log)
			}
			for _, name := range tc.released {
				wl := victims[name].DeepCopy()
				workload.UnsetQuotaReservationWithCondition(wl, "Pending", "Eviction complete", time.Now())
				cache.AddOrUpdateWorkload(ctx, log, wl)
			}
			snap, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("high", "ns").Priority(4).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, tc.count).
					Request(corev1.ResourceCPU, "5").UnconstrainedTopologyRequest().Obj()).Obj())
			incoming.ClusterQueue = "cq"
			cqSnapshot := snap.ClusterQueue("cq")
			beforeUsage := maps.Clone(cqSnapshot.ResourceNode.Usage)
			beforeWorkloads := maps.Clone(cqSnapshot.Workloads)
			capacity := func() string {
				t.Helper()
				value, err := cqSnapshot.TASFlavors["tas"].SerializeFreeCapacityPerDomain()
				if err != nil {
					t.Fatalf("serializing TAS capacity: %v", err)
				}
				return value
			}
			beforeCapacity := capacity()
			s := New(nil, cache, cl, &utiltesting.EventRecorder{})
			asgn, targets := s.getAssignments(ctx, incoming, snap)
			if asgn.WaitingForResidualTASPods != tc.wantWaiting {
				t.Errorf("waiting for residual Pods=%t, want %t", asgn.WaitingForResidualTASPods, tc.wantWaiting)
			}
			switch {
			case tc.wantWaiting:
				if asgn.RepresentativeMode() != flavorassigner.DeferredFit || len(targets) != 0 {
					t.Errorf("expected deferred admission without new victims, got mode=%v targets=%v", asgn.RepresentativeMode(), targets)
				}
			case tc.wantNoFit:
				if asgn.RepresentativeMode() != flavorassigner.NoFit {
					t.Errorf("expected topology rejection, got mode=%v", asgn.RepresentativeMode())
				}
			default:
				if asgn.RepresentativeMode() != flavorassigner.Preempt || len(targets) == 0 {
					t.Errorf("expected legitimate preemption, got mode=%v targets=%v", asgn.RepresentativeMode(), targets)
				}
			}
			if diff := cmp.Diff(beforeUsage, cqSnapshot.ResourceNode.Usage); diff != "" {
				t.Errorf("probe changed quota usage (-before,+after):\n%s", diff)
			}
			if !maps.Equal(beforeWorkloads, cqSnapshot.Workloads) {
				t.Error("probe changed reservations")
			}
			if after := capacity(); beforeCapacity != after {
				t.Errorf("probe changed TAS capacity: before %s, after %s", beforeCapacity, after)
			}
		})
	}
}
