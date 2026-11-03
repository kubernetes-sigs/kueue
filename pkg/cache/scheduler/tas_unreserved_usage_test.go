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
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

func TestObserveTASWorkloadIntent(t *testing.T) {
	cases := map[string]struct {
		tasEnabled, concurrentEnabled, parent, variant, wantIntent bool
		disableResidual                                            bool
		workload                                                   *kueue.Workload
	}{
		"TAS disabled":                                    {},
		"residual accounting disabled":                    {tasEnabled: true, disableResidual: true},
		"evicted pending Workload":                        {tasEnabled: true, wantIntent: true},
		"pending concurrent parent":                       {tasEnabled: true, concurrentEnabled: true, parent: true},
		"concurrent variant":                              {tasEnabled: true, concurrentEnabled: true, variant: true, wantIntent: true},
		"parent label with concurrent admission disabled": {tasEnabled: true, parent: true, wantIntent: true},
		"inactive with reservation before eviction status": {
			tasEnabled: true, wantIntent: true,
			workload: utiltestingapi.MakeWorkload("inactive", "ns").Active(false).
				SimpleReserveQuota("cq", "flavor", time.Now()).Obj(),
		},
		"inactive with eviction explicitly false": {
			tasEnabled: true, wantIntent: true,
			workload: utiltestingapi.MakeWorkload("inactive", "ns").Active(false).
				Condition(metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionFalse}).Obj(),
		},
		"inactive pending on informer startup": {
			tasEnabled: true, wantIntent: true,
			workload: utiltestingapi.MakeWorkload("inactive", "ns").Active(false).Obj(),
		},
		"inactive with TAS disabled": {
			workload: utiltestingapi.MakeWorkload("inactive", "ns").Active(false).Obj(),
		},
		"active pending without eviction": {
			tasEnabled: true,
			workload:   utiltestingapi.MakeWorkload("pending", "ns").Obj(),
		},
		"active reservation with stale eviction": {
			tasEnabled: true,
			workload: utiltestingapi.MakeWorkload("reserved", "ns").
				SimpleReserveQuota("cq", "flavor", time.Now()).EvictedAt(time.Now()).Obj(),
		},
		"inactive concurrent parent": {
			tasEnabled: true, concurrentEnabled: true, parent: true,
			workload: utiltestingapi.MakeWorkload("inactive", "ns").Active(false).Obj(),
		},
		"inactive concurrent variant": {
			tasEnabled: true, concurrentEnabled: true, variant: true, wantIntent: true,
			workload: utiltestingapi.MakeWorkload("inactive", "ns").Active(false).Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.tasEnabled)
			if tc.disableResidual {
				features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, false)
			}
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, tc.concurrentEnabled)
			cache := New(utiltesting.NewFakeClient())
			wl := utiltestingapi.MakeWorkload("evicted", "ns").Obj()
			wl.UID = "uid"
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			if tc.workload != nil {
				wl = tc.workload.DeepCopy()
				wl.UID = "uid"
			}
			if tc.parent {
				concurrentadmission.SetParentVariantLabel(wl)
			}
			if tc.variant {
				wl.OwnerReferences = []metav1.OwnerReference{{APIVersion: kueue.SchemeGroupVersion.String(), Kind: "Workload", Name: "parent", UID: "parent-uid"}}
			}
			cache.ObserveTASWorkloadIntent(wl)
			key := releasedTASWorkloadKey{ref: workload.Key(wl), uid: wl.UID}
			if _, got := cache.tasCache.nonTasUsageCache.releasingTASWorkloads[key]; got != tc.wantIntent {
				t.Fatalf("intent recorded=%t, want %t", got, tc.wantIntent)
			}
			// Reservation-release observation must use the same parent/gate rule.
			cache = New(utiltesting.NewFakeClient())
			if err := cache.DeleteWorkload(logr.Discard(), workload.Key(wl), wl); err != nil {
				t.Fatalf("deleting reservation: %v", err)
			}
			if _, got := cache.tasCache.nonTasUsageCache.releasingTASWorkloads[key]; got != tc.wantIntent {
				t.Fatalf("release intent recorded=%t, want %t", got, tc.wantIntent)
			}
			wl.Spec.Active = new(true)
			wl.Status.Conditions = nil
			cache.ObserveTASWorkloadIntent(wl)
			if _, got := cache.tasCache.nonTasUsageCache.releasingTASWorkloads[key]; got {
				t.Fatal("reactivated pending Workload retained release intent")
			}
		})
	}
}

func TestDeleteClusterQueueReleasesTASReservation(t *testing.T) {
	for name, enabled := range map[string]bool{"residual accounting enabled": true, "residual accounting disabled": false} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, enabled)
			ctx, log := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder()
			if err := utiltesting.AsIndexer(clientBuilder).IndexField(ctx, &corev1.LimitRange{},
				coreindexer.LimitRangeHasContainerOrPodType, coreindexer.IndexLimitRangeHasContainerOrPodType); err != nil {
				t.Fatalf("setting up client indexes: %v", err)
			}
			cache := New(clientBuilder.Build())
			topology := utiltestingapi.MakeDefaultOneLevelTopology("topology")
			cache.AddOrUpdateTopology(log, topology)
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas-flavor").TopologyName(topology.Name).Obj())
			cq := utiltestingapi.MakeClusterQueue("old-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "4").Obj()).
				NamespaceSelector(nil).
				Obj()
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}

			assignment := utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "tas-flavor", "1").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"node-a"}}).
					Obj()).
				Obj()
			wl := utiltestingapi.MakeWorkload("old", "ns").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").RequiredTopologyRequest(corev1.LabelHostname).Obj()).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("old-cq").PodSets(assignment).Obj(), time.Now()).
				Obj()
			if !cache.AddOrUpdateWorkload(ctx, log, wl) {
				t.Fatal("adding TAS Workload to cache")
			}

			ref := workload.Key(wl)
			flavorCache := cache.tasCache.Get("tas-flavor")
			if _, found := flavorCache.wlUsage[ref]; !found {
				t.Fatal("active TAS reservation missing before ClusterQueue deletion")
			}
			pod := makePod("old-pod", "ns", "node-a", "1")
			pod.Annotations = map[string]string{kueue.WorkloadAnnotation: wl.Name}
			cache.tasCache.UpdateTASPodUsage(pod, log)
			reserved := map[workload.Reference]*workload.Info{ref: cache.hm.ClusterQueue("old-cq").Workloads[ref]}
			if got := cache.tasCache.nonTasUsageCache.unreservedTASUsage(reserved); len(got) != 0 {
				t.Fatalf("bound Pod was double-counted before ClusterQueue deletion: %v", got)
			}

			cache.DeleteClusterQueue(cq)
			if !enabled {
				if _, found := flavorCache.wlUsage[ref]; !found {
					t.Fatal("disabled residual accounting changed ClusterQueue deletion reservation cleanup")
				}
				if _, found := cache.workloadAssignedQueues[ref]; !found {
					t.Fatal("disabled residual accounting changed ClusterQueue deletion assignment cleanup")
				}
				return
			}
			if _, found := flavorCache.wlUsage[ref]; found {
				t.Fatal("deleted ClusterQueue left the TAS reservation in the flavor cache")
			}
			if got := flavorCache.usage["node-a"].ResourceValue(corev1.ResourceCPU); got.Sign() != 0 {
				t.Fatalf("TAS reservation after ClusterQueue deletion=%v, want 0", got)
			}
			if _, found := cache.workloadAssignedQueues[ref]; found {
				t.Fatal("deleted ClusterQueue left the Workload assignment in the cache")
			}
			usage := cache.tasCache.nonTasUsageCache.unreservedTASUsage(nil)["node-a"]
			if usage.ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 || usage.ResourceValue(corev1.ResourcePods).CmpInt64(1) != 0 {
				t.Fatalf("residual bound Pod usage=%v, want one CPU and one Pod slot", usage)
			}
			cache.tasCache.DeleteTASPodUsageByKey(client.ObjectKeyFromObject(pod), log)
			if got := cache.tasCache.nonTasUsageCache.unreservedTASUsage(nil); len(got) != 0 {
				t.Fatalf("residual usage after Pod deletion=%v, want none", got)
			}
		})
	}
}

func TestTASResidualPodAccountingSnapshot(t *testing.T) {
	cases := map[string]struct {
		enabled  bool
		wantCPU  string
		wantPods string
	}{
		"enabled counts residual physical usage": {enabled: true, wantCPU: "1", wantPods: "8"},
		"disabled keeps non-TAS usage only":      {wantCPU: "3", wantPods: "9"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("topology"))
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("flavor").TopologyName("topology").Obj())
			cache.TASCache().SyncNode(testingnode.MakeNode("node-a").Label(corev1.LabelHostname, "node-a").
				StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).Ready().Obj())
			if err := cache.AddClusterQueue(ctx, utiltestingapi.MakeClusterQueue("cq").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("flavor").Resource(corev1.ResourceCPU, "4").Obj()).Obj()); err != nil {
				t.Fatalf("adding ClusterQueue: %v", err)
			}
			pod := testingpod.MakePod("bound", "ns").NodeName("node-a").Request(corev1.ResourceCPU, "2").
				Annotation(kueue.WorkloadAnnotation, "released").Obj()
			pod.DeletionTimestamp = new(metav1.Now())
			// Seed data before changing the gate so the disabled path must also
			// ignore stale physical usage rather than merely stop observations.
			cache.TASCache().UpdateTASPodUsage(pod, log)
			cache.TASCache().UpdateNonTASUsage(makePod("background", "ns", "node-a", "1"), log)
			features.SetFeatureGateDuringTest(t, features.TASResidualPodAccounting, tc.enabled)
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			flavor := snapshot.ClusterQueue("cq").TASFlavors["flavor"]
			before, err := flavor.SerializeFreeCapacityPerDomain()
			if err != nil {
				t.Fatalf("serializing capacity: %v", err)
			}
			var capacity map[string]struct {
				FreeCapacity map[string]string `json:"freeCapacity"`
			}
			if err := json.Unmarshal([]byte(before), &capacity); err != nil {
				t.Fatalf("decoding capacity: %v", err)
			}
			if got := capacity["node-a"].FreeCapacity; got["cpu"] != tc.wantCPU || got["pods"] != tc.wantPods {
				t.Fatalf("free capacity=%v, want cpu=%s pods=%s", got, tc.wantCPU, tc.wantPods)
			}
			if !tc.enabled {
				if len(snapshot.residualTASPods) != 0 {
					t.Fatal("disabled accounting populated residual snapshot data")
				}
				// Even an external caller with stale residual data must not probe.
				snapshot.residualTASPods = []tasPodUsageValue{cache.tasCache.nonTasUsageCache.tasPodUsage[client.ObjectKeyFromObject(pod)]}
			}
			restore, found := snapshot.SimulateResidualTASPodRelease(ctx, log,
				workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "ns").Obj()))
			if found != tc.enabled {
				t.Fatalf("release probe found=%t, want %t", found, tc.enabled)
			}
			restore()
			after, err := flavor.SerializeFreeCapacityPerDomain()
			if err != nil || before != after {
				t.Fatalf("release probe did not preserve capacity: before=%s after=%s err=%v", before, after, err)
			}
			newPod := pod.DeepCopy()
			newPod.Name = "second"
			cache.TASCache().UpdateTASPodUsage(newPod, log)
			_, tracked := cache.tasCache.nonTasUsageCache.tasPodUsage[client.ObjectKeyFromObject(newPod)]
			if tracked != tc.enabled {
				t.Fatalf("new TAS Pod tracked=%t, want %t", tracked, tc.enabled)
			}
			cache.TASCache().DeleteTASPodUsageByKey(client.ObjectKeyFromObject(pod), log)
			_, retained := cache.tasCache.nonTasUsageCache.tasPodUsage[client.ObjectKeyFromObject(pod)]
			if retained == tc.enabled {
				t.Fatalf("TAS Pod retained after deletion=%t with gate=%t", retained, tc.enabled)
			}
		})
	}
}

func TestUnreservedTASPodUsage(t *testing.T) {
	cache := &nonTasUsageCache{}
	log := logr.Discard()
	pod := makePod("old-pod", "ns", "node-a", "2")
	pod.Annotations = map[string]string{
		kueue.WorkloadAnnotation:                    "wl",
		kueue.PodSetUnconstrainedTopologyAnnotation: "true",
	}
	cache.updateTAS(pod, log)

	assertUsage := func(reserved map[workload.Reference]*workload.Info, cpu, pods int64) {
		t.Helper()
		usage := cache.unreservedTASUsage(reserved)["node-a"]
		if cpu == 0 && pods == 0 {
			if usage != nil {
				t.Fatalf("unexpected residual usage: %v", usage)
			}
			return
		}
		if usage == nil || usage.ResourceValue(corev1.ResourceCPU).CmpInt64(cpu) != 0 || usage.ResourceValue(corev1.ResourcePods).CmpInt64(pods) != 0 {
			t.Fatalf("residual usage=%v, want CPU=%d Pods=%d", usage, cpu, pods)
		}
	}

	assertUsage(nil, 2000, 1)
	assertUsage(map[workload.Reference]*workload.Info{"other/wl": {Obj: &kueue.Workload{}}}, 2000, 1)
	assertUsage(map[workload.Reference]*workload.Info{"ns/wl": {Obj: &kueue.Workload{}}}, 0, 0)
	// Reservation release is detected on the next snapshot without a Pod event.
	assertUsage(nil, 2000, 1)

	deleting := metav1.Now()
	pod.DeletionTimestamp = &deleting
	cache.updateTAS(pod, log)
	assertUsage(nil, 2000, 1)
	pod.Status.Phase = corev1.PodSucceeded
	cache.updateTAS(pod, log)
	assertUsage(nil, 0, 0)
	cache.deleteTAS(client.ObjectKeyFromObject(pod), log)
	assertUsage(nil, 0, 0)
}

func TestUnreservedTASUsageWithoutPrimaryReference(t *testing.T) {
	cases := map[string]struct {
		annotations map[string]string
		namespace   string
		wantCovered bool
	}{
		"missing annotations remain physical usage": {},
		"empty primary reference remains physical usage": {
			annotations: map[string]string{kueue.WorkloadAnnotation: ""},
		},
		"slice-only Pod is covered by the replacement reservation": {
			annotations: map[string]string{kueue.WorkloadSliceNameAnnotation: "origin"}, wantCovered: true,
		},
		"empty primary reference still uses the slice alias": {
			annotations: map[string]string{kueue.WorkloadAnnotation: "", kueue.WorkloadSliceNameAnnotation: "origin"}, wantCovered: true,
		},
		"slice alias from another namespace does not cover the Pod": {
			annotations: map[string]string{kueue.WorkloadSliceNameAnnotation: "origin"}, namespace: "other",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &nonTasUsageCache{}
			wl := utiltestingapi.MakeWorkload("replacement", "ns").Obj()
			wl.Annotations = map[string]string{kueue.WorkloadSliceNameAnnotation: "origin"}
			pod := makePod("bound", "ns", "node-a", "1")
			pod.Annotations = tc.annotations
			if tc.namespace != "" {
				pod.Namespace = tc.namespace
			}
			cache.updateTAS(pod, logr.Discard())
			reserved := map[workload.Reference]*workload.Info{workload.Key(wl): {Obj: wl}}
			usage, residual := cache.unreservedTASUsageAndPods(reserved)
			if tc.wantCovered {
				if len(usage) != 0 || len(residual) != 0 {
					t.Fatalf("slice reservation double-counted the Pod: usage=%v, residual=%v", usage, residual)
				}
			} else if len(residual) != 1 || usage["node-a"].ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 ||
				usage["node-a"].ResourceValue(corev1.ResourcePods).CmpInt64(1) != 0 {
				t.Fatalf("unannotated Pod was not fully charged: usage=%v, residual=%v", usage, residual)
			}
			// Reservation changes must take effect without another Pod event.
			usage, residual = cache.unreservedTASUsageAndPods(nil)
			if len(residual) != 1 || usage["node-a"].ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 {
				t.Fatalf("released reservation hid physical usage: usage=%v, residual=%v", usage, residual)
			}
			cache.deleteTAS(client.ObjectKeyFromObject(pod), logr.Discard())
			if usage, residual := cache.unreservedTASUsageAndPods(nil); len(usage) != 0 || len(residual) != 0 || len(cache.tasPods.Pods()) != 0 {
				t.Fatalf("deleted Pod remained tracked: usage=%v, residual=%v", usage, residual)
			}
		})
	}
}

func TestUnreservedTASUsageAggregatesPodsAcrossNodesAndWorkloads(t *testing.T) {
	cache := &nonTasUsageCache{}
	log := logr.Discard()
	for _, tc := range []struct {
		name, node, workloadName string
	}{
		{name: "a", node: "node-a", workloadName: "old"},
		{name: "b", node: "node-a", workloadName: "old"},
		{name: "c", node: "node-b", workloadName: "other"},
	} {
		pod := makePod(tc.name, "ns", tc.node, "1")
		pod.Annotations = map[string]string{kueue.WorkloadAnnotation: tc.workloadName}
		cache.updateTAS(pod, log)
	}
	usage := cache.unreservedTASUsage(nil)
	if got := usage["node-a"].ResourceValue(corev1.ResourceCPU); got.CmpInt64(2000) != 0 {
		t.Fatalf("node-a residual CPU=%v, want 2000", got)
	}
	if got := usage["node-a"].ResourceValue(corev1.ResourcePods); got.CmpInt64(2) != 0 {
		t.Fatalf("node-a residual Pods=%v, want 2", got)
	}
	if got := usage["node-b"].ResourceValue(corev1.ResourceCPU); got.CmpInt64(1000) != 0 {
		t.Fatalf("node-b residual CPU=%v, want 1000", got)
	}
	usage = cache.unreservedTASUsage(map[workload.Reference]*workload.Info{"ns/old": {Obj: &kueue.Workload{}}})
	if _, found := usage["node-a"]; found {
		t.Fatalf("active reservation was double-counted on node-a: %v", usage["node-a"])
	}
	if got := usage["node-b"].ResourceValue(corev1.ResourceCPU); got.CmpInt64(1000) != 0 {
		t.Fatalf("unreserved node-b CPU=%v, want 1000", got)
	}
	cache.deleteTAS(client.ObjectKey{Namespace: "ns", Name: "a"}, log)
	usage = cache.unreservedTASUsage(nil)
	if got := usage["node-a"].ResourceValue(corev1.ResourceCPU); got.CmpInt64(1000) != 0 {
		t.Fatalf("node-a residual CPU after deletion=%v, want 1000", got)
	}
}

func TestTASPodUsageChangeFreesNode(t *testing.T) {
	cache := &nonTasUsageCache{}
	log := logr.Discard()
	pod := makePod("pod", "ns", "node-a", "1")
	pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "old"}
	if got := cache.updateTAS(pod, log); got != "" {
		t.Fatalf("initial addition freed node %q", got)
	}
	if got := cache.updateTAS(pod, log); got != "" {
		t.Fatalf("unchanged Pod freed node %q", got)
	}
	pod.Spec.Containers[0].Resources.Requests = nil
	if got := cache.updateTAS(pod, log); got != "node-a" {
		t.Fatalf("removed resource key freed node %q, want node-a", got)
	}
	usage := cache.unreservedTASUsage(nil)["node-a"]
	if usage.ResourceValue(corev1.ResourceCPU).Sign() != 0 || usage.ResourceValue(corev1.ResourcePods).CmpInt64(1) != 0 {
		t.Fatalf("zero-request Pod usage=%v, want one Pod slot", usage)
	}
	pod.Annotations[kueue.WorkloadAnnotation] = "new"
	if got := cache.updateTAS(pod, log); got != "node-a" {
		t.Fatalf("reservation change freed node %q, want node-a", got)
	}
	if usage := cache.unreservedTASUsage(map[workload.Reference]*workload.Info{"ns/new": {Obj: &kueue.Workload{}}}); len(usage) != 0 {
		t.Fatalf("Pod covered by new reservation was double-counted: %v", usage)
	}
	pod.Spec.NodeName = "node-b"
	if got := cache.updateTAS(pod, log); got != "node-a" {
		t.Fatalf("node move freed node %q, want node-a", got)
	}
}

func BenchmarkUnreservedTASUsage(b *testing.B) {
	cache := &nonTasUsageCache{}
	reserved := make(map[workload.Reference]*workload.Info)
	for i := range 1000 {
		name := fmt.Sprintf("wl-%d", i)
		if i%2 == 0 {
			reserved[workload.NewReference("ns", name)] = &workload.Info{Obj: &kueue.Workload{}}
		}
		pod := makePod(fmt.Sprintf("pod-%d", i), "ns", "node-a", "1")
		pod.Annotations = map[string]string{kueue.WorkloadAnnotation: name}
		cache.updateTAS(pod, logr.Discard())
	}
	b.ResetTimer()
	for range b.N {
		cache.unreservedTASUsage(reserved)
	}
}

func BenchmarkUnreservedTASUsageForPodGroup(b *testing.B) {
	for _, podCount := range []int{256, 1024, 4096} {
		b.Run(fmt.Sprintf("pods-%d", podCount), func(b *testing.B) {
			cache := &nonTasUsageCache{}
			owners := make([]metav1.OwnerReference, 0, podCount)
			for i := range podCount {
				pod := makePod(fmt.Sprintf("pod-%d", i), "ns", "node-a", "1")
				pod.UID = types.UID(pod.Name)
				pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "group"}
				cache.updateTAS(pod, logr.Discard())
				owners = append(owners, metav1.OwnerReference{
					APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Pod", UID: pod.UID,
				})
			}
			reserved := map[workload.Reference]*workload.Info{
				"ns/group": {Obj: &kueue.Workload{OwnerReferences: owners}},
			}
			b.ResetTimer()
			for range b.N {
				cache.unreservedTASUsage(reserved)
			}
		})
	}
}

func TestDeletedTASWorkloadIntentMatchesSliceReferences(t *testing.T) {
	cases := map[string]struct {
		workloadName, sliceName, alias, podOwnerUID string
		wantReleasing                               bool
	}{
		"primary Workload reference":                        {workloadName: "victim", podOwnerUID: "job-uid", wantReleasing: true},
		"slice reference matches Workload name":             {workloadName: "old-slice", sliceName: "victim", podOwnerUID: "job-uid", wantReleasing: true},
		"slice reference matches origin alias":              {workloadName: "old-slice", sliceName: "origin", alias: "origin", podOwnerUID: "job-uid", wantReleasing: true},
		"slice reference without primary reference":         {sliceName: "origin", alias: "origin", podOwnerUID: "job-uid", wantReleasing: true},
		"primary and slice reference match duplicate alias": {workloadName: "victim", sliceName: "victim", alias: "victim", podOwnerUID: "job-uid", wantReleasing: true},
		"different comparable owner":                        {workloadName: "old-slice", sliceName: "origin", alias: "origin", podOwnerUID: "other-job"},
		"unrelated references with same owner":              {workloadName: "other", sliceName: "other-origin", alias: "origin", podOwnerUID: "job-uid"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &nonTasUsageCache{}
			wl := utiltestingapi.MakeWorkload("victim", "ns").UID("old-workload").Obj()
			wl.Annotations = map[string]string{kueue.WorkloadSliceNameAnnotation: tc.alias}
			wl.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid"}}
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			pod := makePod("bound", "ns", "node-a", "1")
			pod.UID = "old-pod"
			pod.Annotations = map[string]string{kueue.WorkloadAnnotation: tc.workloadName, kueue.WorkloadSliceNameAnnotation: tc.sliceName}
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: types.UID(tc.podOwnerUID), Controller: new(true)}}
			cache.updateTAS(pod, logr.Discard())
			cache.observeWorkload(wl)
			assertReleasing := func() {
				t.Helper()
				usage, pods := cache.unreservedTASUsageAndPods(nil)
				if len(pods) != 1 || pods[0].releasing != tc.wantReleasing ||
					usage["node-a"].ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 ||
					usage["node-a"].ResourceValue(corev1.ResourcePods).CmpInt64(1) != 0 {
					t.Fatalf("usage=%v, pods=%v, want one charged Pod and releasing=%t", usage, pods, tc.wantReleasing)
				}
			}
			assertReleasing()
			cache.deleteWorkload(workload.Key(wl), wl.UID)
			assertReleasing()
			key := releasedTASWorkloadKey{ref: workload.Key(wl), uid: wl.UID}
			if tc.wantReleasing {
				entry := cache.releasingTASWorkloads[key]
				if !entry.deleted || len(entry.pods) != 1 || entry.pods[client.ObjectKeyFromObject(pod)] != pod.UID {
					t.Fatalf("frozen intent=%v, want the exact Pod UID once", entry)
				}
			} else if len(cache.releasingTASWorkloads) != 0 {
				t.Fatal("unmatched Pod retained a deleted Workload intent")
			}
			cache.deleteTAS(client.ObjectKeyFromObject(pod), logr.Discard())
			if len(cache.releasingTASWorkloads) != 0 || len(cache.deletedWorkloadsByPod) != 0 {
				t.Fatal("last bound Pod deletion left stale intent mappings")
			}
		})
	}
}

func TestDeletedTASWorkloadSliceIntentPodUpdates(t *testing.T) {
	cases := map[string]struct {
		change        func(*corev1.Pod)
		deletePod     bool
		wantPod       bool
		wantReleasing bool
	}{
		"unchanged identity":         {change: func(*corev1.Pod) {}, wantPod: true, wantReleasing: true},
		"node changes":               {change: func(pod *corev1.Pod) { pod.Spec.NodeName = "node-b" }, wantPod: true, wantReleasing: true},
		"deletion timestamp changes": {change: func(pod *corev1.Pod) { pod.DeletionTimestamp = new(metav1.Now()) }, wantPod: true, wantReleasing: true},
		"primary reference changes":  {change: func(pod *corev1.Pod) { pod.Annotations[kueue.WorkloadAnnotation] = "new-slice" }, wantPod: true},
		"primary reference removed":  {change: func(pod *corev1.Pod) { delete(pod.Annotations, kueue.WorkloadAnnotation) }, wantPod: true},
		"primary reference emptied":  {change: func(pod *corev1.Pod) { pod.Annotations[kueue.WorkloadAnnotation] = "" }, wantPod: true},
		"slice reference changes":    {change: func(pod *corev1.Pod) { pod.Annotations[kueue.WorkloadSliceNameAnnotation] = "new-origin" }, wantPod: true},
		"slice reference removed":    {change: func(pod *corev1.Pod) { delete(pod.Annotations, kueue.WorkloadSliceNameAnnotation) }, wantPod: true},
		"controller owner changes":   {change: func(pod *corev1.Pod) { pod.OwnerReferences[0].UID = "new-job" }, wantPod: true},
		"Pod UID changes":            {change: func(pod *corev1.Pod) { pod.UID = "new-pod" }, wantPod: true},
		"Pod succeeds":               {change: func(pod *corev1.Pod) { pod.Status.Phase = corev1.PodSucceeded }},
		"Pod fails":                  {change: func(pod *corev1.Pod) { pod.Status.Phase = corev1.PodFailed }},
		"Pod becomes unbound":        {change: func(pod *corev1.Pod) { pod.Spec.NodeName = "" }},
		"Pod is deleted":             {deletePod: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &nonTasUsageCache{}
			wl := utiltestingapi.MakeWorkload("replacement-slice", "ns").UID("old-workload").Obj()
			wl.Annotations = map[string]string{kueue.WorkloadSliceNameAnnotation: "origin"}
			wl.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid"}}
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			pod := makePod("bound", "ns", "node-a", "1")
			pod.UID = "old-pod"
			pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "old-slice", kueue.WorkloadSliceNameAnnotation: "origin"}
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid", Controller: new(true)}}
			cache.updateTAS(pod, logr.Discard())
			cache.observeWorkload(wl)
			cache.deleteWorkload(workload.Key(wl), wl.UID)
			if tc.deletePod {
				cache.deleteTAS(client.ObjectKeyFromObject(pod), logr.Discard())
			} else {
				tc.change(pod)
				cache.updateTAS(pod, logr.Discard())
			}
			usage, residual := cache.unreservedTASUsageAndPods(nil)
			if !tc.wantPod {
				if len(usage) != 0 || len(residual) != 0 || len(cache.tasPodUsage) != 0 ||
					len(cache.tasPods.Pods()) != 0 || len(cache.tasPodsBySliceWorkload) != 0 || len(cache.tasNodeUsage) != 0 {
					t.Fatal("removed Pod left physical usage or stale reference indexes")
				}
			} else {
				if len(usage) != 1 || len(residual) != 1 || residual[0].releasing != tc.wantReleasing ||
					usage[pod.Spec.NodeName].ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 ||
					usage[pod.Spec.NodeName].ResourceValue(corev1.ResourcePods).CmpInt64(1) != 0 {
					t.Fatalf("usage=%v, residual=%v, want one charged Pod and releasing=%t", usage, residual, tc.wantReleasing)
				}
				podKey := client.ObjectKeyFromObject(pod)
				if len(cache.tasPodUsage) != 1 || len(cache.tasPods.Pods()) != 1 ||
					cache.tasPods.Pod(podKey) == nil || cache.tasPods.Pod(podKey).UID != pod.UID {
					t.Fatal("primary Pod reference index is stale")
				}
				wlName := pod.Annotations[kueue.WorkloadAnnotation]
				wlPods := cache.tasPods.PodsForWorkload(client.ObjectKey{Namespace: pod.Namespace, Name: wlName})
				if wlName == "" {
					if len(wlPods) != 0 {
						t.Fatal("unannotated Pod was indexed under an empty Workload name")
					}
				} else if len(wlPods) != 1 || wlPods[0].UID != pod.UID {
					t.Fatal("shared Workload index is stale")
				}
				if sliceName := pod.Annotations[kueue.WorkloadSliceNameAnnotation]; sliceName != "" {
					sliceRef := workload.NewReference(pod.Namespace, sliceName)
					if len(cache.tasPodsBySliceWorkload) != 1 || len(cache.tasPodsBySliceWorkload[sliceRef]) != 1 ||
						cache.tasPodsBySliceWorkload[sliceRef][podKey].podUID != pod.UID {
						t.Fatal("slice Pod reference index is stale")
					}
				} else if len(cache.tasPodsBySliceWorkload) != 0 {
					t.Fatal("removed slice annotation left an index bucket")
				}
			}
			if tc.wantReleasing {
				key := releasedTASWorkloadKey{ref: workload.Key(wl), uid: wl.UID}
				if len(cache.releasingTASWorkloads[key].pods) != 1 || len(cache.deletedWorkloadsByPod) != 1 {
					t.Fatal("unchanged Pod identity lost frozen intent mappings")
				}
			} else if len(cache.releasingTASWorkloads) != 0 || len(cache.deletedWorkloadsByPod) != 0 {
				t.Fatal("removed or reattributed Pod left stale intent mappings")
			}
		})
	}
}

func TestDeletedTASWorkloadSliceIntentSharedAlias(t *testing.T) {
	cache := &nonTasUsageCache{}
	wl := utiltestingapi.MakeWorkload("replacement", "ns").UID("workload-uid").Obj()
	wl.Annotations = map[string]string{kueue.WorkloadSliceNameAnnotation: "origin"}
	wl.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid"}}
	var pods []*corev1.Pod
	for _, name := range []string{"old-slice", "other-slice", "origin"} {
		pod := makePod(name, "ns", "node-a", "1")
		pod.UID = types.UID(name)
		pod.Annotations = map[string]string{kueue.WorkloadAnnotation: name, kueue.WorkloadSliceNameAnnotation: "origin"}
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid", Controller: new(true)}}
		cache.updateTAS(pod, logr.Discard())
		pods = append(pods, pod)
	}
	if usage := cache.unreservedTASUsage(map[workload.Reference]*workload.Info{workload.Key(wl): {Obj: wl}}); len(usage) != 0 {
		t.Fatalf("shared alias double-counted reserved Pods: %v", usage)
	}
	wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
	cache.observeWorkload(wl)
	cache.deleteWorkload(workload.Key(wl), wl.UID)
	for remaining := len(pods); remaining > 0; remaining-- {
		usage, residual := cache.unreservedTASUsageAndPods(nil)
		if len(residual) != remaining || usage["node-a"].ResourceValue(corev1.ResourceCPU).CmpInt64(int64(remaining)*1000) != 0 ||
			usage["node-a"].ResourceValue(corev1.ResourcePods).CmpInt64(int64(remaining)) != 0 {
			t.Fatalf("usage=%v, residual=%v, want %d Pods charged once", usage, residual, remaining)
		}
		for _, pod := range residual {
			if !pod.releasing {
				t.Fatal("shared alias Pod lost frozen eviction intent")
			}
		}
		key := releasedTASWorkloadKey{ref: workload.Key(wl), uid: wl.UID}
		if len(cache.releasingTASWorkloads[key].pods) != remaining {
			t.Fatal("shared alias duplicated or lost exact Pod intent mappings")
		}
		cache.deleteTAS(client.ObjectKeyFromObject(pods[remaining-1]), logr.Discard())
	}
	if len(cache.releasingTASWorkloads) != 0 || len(cache.deletedWorkloadsByPod) != 0 ||
		len(cache.tasPods.Pods()) != 0 || len(cache.tasPodsBySliceWorkload) != 0 {
		t.Fatal("shared alias Pod deletion left stale intent or reference mappings")
	}
}

func TestReleasedTASWorkloadIntent(t *testing.T) {
	cases := map[string]struct{ podFirst, inactive bool }{
		"Workload observed before Pod":          {},
		"Pod observed before Workload":          {podFirst: true},
		"inactive Workload observed before Pod": {inactive: true},
		"Pod observed before inactive Workload": {podFirst: true, inactive: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &nonTasUsageCache{}
			log := logr.Discard()
			wl := utiltestingapi.MakeWorkload("victim", "ns").Obj()
			wl.UID = "old-workload"
			wl.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "old-owner"}}
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			if tc.inactive {
				wl.Spec.Active = new(false)
				wl.Status.Conditions = nil
			}
			pod := makePod("bound", "ns", "node-a", "1")
			pod.Annotations = map[string]string{kueue.WorkloadAnnotation: wl.Name}
			pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "old-owner", Controller: new(true)}}
			if tc.podFirst {
				cache.updateTAS(pod, log)
			}
			cache.observeWorkload(wl)
			if !tc.podFirst {
				cache.updateTAS(pod, log)
			}
			assertReleasing := func(want bool) {
				t.Helper()
				usage, pods := cache.unreservedTASUsageAndPods(nil)
				if usage["node-a"].ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 || len(pods) != 1 || pods[0].releasing != want {
					t.Fatalf("usage=%v, pods=%v, want charged CPU and releasing=%t", usage, pods, want)
				}
			}
			assertReleasing(true)
			// A same-name Workload belonging to a different Job must not
			// turn an unrelated Pod into a prospective release candidate.
			wl.OwnerReferences[0].UID = "new-owner"
			cache.observeWorkload(wl)
			assertReleasing(false)
			wl.OwnerReferences[0].UID = "old-owner"
			cache.observeWorkload(wl)
			// Reactivation without eviction removes both forms of release intent.
			wl.Spec.Active = new(true)
			wl.Status.Conditions = nil
			cache.observeWorkload(wl)
			assertReleasing(false)
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			cache.observeWorkload(wl)
			cache.deleteWorkload(workload.Key(wl), wl.UID)
			assertReleasing(true)
			// A new same-name object must neither clear the old intent nor
			// keep it alive through a different Pod generation.
			replacement := wl.DeepCopy()
			replacement.UID = "new-workload"
			replacement.Status.Conditions = nil
			cache.observeWorkload(replacement)
			assertReleasing(true)
			replacement.Status.Conditions = wl.Status.Conditions
			cache.observeWorkload(replacement)
			cache.deleteWorkload(workload.Key(wl), wl.UID)
			if cache.releasingTASWorkloads[releasedTASWorkloadKey{ref: workload.Key(replacement), uid: replacement.UID}].deleted {
				t.Fatal("old Workload deletion froze the new Workload's live intent")
			}
			replacement.Status.Conditions = nil
			cache.observeWorkload(replacement)
			newPod := pod.DeepCopy()
			newPod.Name = "replacement-pod"
			newPod.UID = "new-pod"
			cache.updateTAS(newPod, log)
			_, residual := cache.unreservedTASUsageAndPods(nil)
			for _, value := range residual {
				if value.podKey.Name == newPod.Name && value.releasing {
					t.Fatal("deleted Workload intent incorrectly covers a new Pod")
				}
			}
			cache.deleteTAS(client.ObjectKeyFromObject(pod), log)
			if len(cache.releasingTASWorkloads) != 0 {
				t.Fatal("deleted Workload intent survived deletion of its last bound Pod")
			}
			if len(cache.deletedWorkloadsByPod) != 0 {
				t.Fatal("deleted Workload left reverse Pod intent mappings")
			}
		})
	}
}

func TestDeletedTASIntentDetachesOnPodAttributionChange(t *testing.T) {
	cases := map[string]struct{ change string }{
		"Workload reference changes": {change: "workload"},
		"slice reference changes":    {change: "slice"},
		"controller owner changes":   {change: "owner"},
		"Pod UID changes":            {change: "podUID"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &nonTasUsageCache{}
			wl := utiltestingapi.MakeWorkload("victim", "ns").Obj()
			wl.UID = "old"
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
			pod := makePod("bound", "ns", "node-a", "1")
			pod.UID = "old-pod"
			pod.Annotations = map[string]string{kueue.WorkloadAnnotation: wl.Name}
			cache.updateTAS(pod, logr.Discard())
			cache.observeWorkload(wl)
			cache.deleteWorkload(workload.Key(wl), wl.UID)
			switch tc.change {
			case "workload":
				pod.Annotations[kueue.WorkloadAnnotation] = "replacement"
			case "slice":
				pod.Annotations[kueue.WorkloadSliceNameAnnotation] = "replacement"
			case "owner":
				pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "replacement", Controller: new(true)}}
			case "podUID":
				pod.UID = "replacement"
			}
			cache.updateTAS(pod, logr.Discard())
			usage, residual := cache.unreservedTASUsageAndPods(nil)
			if len(residual) != 1 || residual[0].releasing || usage["node-a"].ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 {
				t.Fatalf("changed Pod attribution retained deleted intent or lost physical usage: pods=%v, usage=%v", residual, usage)
			}
			if len(cache.releasingTASWorkloads) != 0 || len(cache.deletedWorkloadsByPod) != 0 {
				t.Fatal("attribution change left stale intent indexes")
			}
		})
	}
}

func TestUnreservedTASUsageForMixedPodGroupOwners(t *testing.T) {
	cache := &nonTasUsageCache{}
	owners := make([]metav1.OwnerReference, 0, 32)
	for i := range 32 {
		pod := makePod(fmt.Sprintf("pod-%d", i), "ns", "node-a", "1")
		pod.UID = types.UID(pod.Name)
		pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "group"}
		cache.updateTAS(pod, logr.Discard())
		if i < 31 {
			owners = append(owners, metav1.OwnerReference{
				APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Pod", UID: pod.UID,
			})
		}
	}
	active := &kueue.Workload{OwnerReferences: owners}
	reserved := map[workload.Reference]*workload.Info{"ns/group": {Obj: active}}
	usage := cache.unreservedTASUsage(reserved)["node-a"]
	if usage == nil || usage.ResourceValue(corev1.ResourceCPU).CmpInt64(1000) != 0 ||
		usage.ResourceValue(corev1.ResourcePods).CmpInt64(1) != 0 {
		t.Fatalf("uncovered Pod usage=%v, want one CPU and one Pod slot", usage)
	}
	active.OwnerReferences = append(active.OwnerReferences, metav1.OwnerReference{
		APIVersion: corev1.SchemeGroupVersion.String(), Kind: "Pod", UID: "pod-31",
	})
	if got := cache.unreservedTASUsage(reserved); len(got) != 0 {
		t.Fatalf("all Pod owners are covered, residual usage=%v", got)
	}
}

func TestUnreservedTASUsageForReplacementOwner(t *testing.T) {
	cache := &nonTasUsageCache{}
	pod := makePod("old-pod", "ns", "node-a", "1")
	pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "wl"}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "old-job", Controller: new(true)}}
	cache.updateTAS(pod, logr.Discard())
	active := &kueue.Workload{OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "new-job"}}}
	reserved := map[workload.Reference]*workload.Info{"ns/wl": {Obj: active}}
	if got := cache.unreservedTASUsage(reserved)["node-a"].ResourceValue(corev1.ResourceCPU); got.CmpInt64(1000) != 0 {
		t.Fatalf("old owner's residual CPU=%v, want 1000", got)
	}
	active.OwnerReferences[0].UID = "old-job"
	if usage := cache.unreservedTASUsage(reserved); len(usage) != 0 {
		t.Fatalf("Pod covered by its own active reservation was double-counted: %v", usage)
	}
}

func TestUnreservedTASUsageForPodGroupReplacement(t *testing.T) {
	cache := &nonTasUsageCache{}
	pod := makePod("old-pod", "ns", "node-a", "1")
	pod.UID = "old-pod-uid"
	pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "wl"}
	cache.updateTAS(pod, logr.Discard())
	active := &kueue.Workload{OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", UID: "new-pod-uid"}}}
	reserved := map[workload.Reference]*workload.Info{"ns/wl": {Obj: active}}
	if got := cache.unreservedTASUsage(reserved)["node-a"].ResourceValue(corev1.ResourcePods); got.CmpInt64(1) != 0 {
		t.Fatalf("old PodGroup member residual Pod count=%v, want 1", got)
	}
	active.OwnerReferences[0].UID = pod.UID
	if usage := cache.unreservedTASUsage(reserved); len(usage) != 0 {
		t.Fatalf("PodGroup member covered by reservation was double-counted: %v", usage)
	}
}

func TestUnreservedTASUsageForSliceReplacement(t *testing.T) {
	cache := &nonTasUsageCache{}
	pod := makePod("old-pod", "ns", "node-a", "1")
	pod.Annotations = map[string]string{
		kueue.WorkloadAnnotation:          "old-slice",
		kueue.WorkloadSliceNameAnnotation: "original-slice",
	}
	pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid", Controller: new(true)}}
	cache.updateTAS(pod, logr.Discard())
	active := &kueue.Workload{
		Namespace: "ns", Name: "replacement-slice",
		Annotations:     map[string]string{kueue.WorkloadSliceNameAnnotation: "original-slice"},
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "job-uid"}}}
	reserved := map[workload.Reference]*workload.Info{"ns/replacement-slice": {Obj: active}}
	if got := cache.unreservedTASUsage(reserved); len(got) != 0 {
		t.Fatalf("slice replacement double-counted its old bound Pod: %v", got)
	}
	active.OwnerReferences[0].UID = "different-job"
	if got := cache.unreservedTASUsage(reserved)["node-a"].ResourceValue(corev1.ResourceCPU); got.CmpInt64(1000) != 0 {
		t.Fatalf("different owner's residual CPU=%v, want 1000", got)
	}
}

func TestUnreservedTASUsageForConcurrentAdmissionVariant(t *testing.T) {
	cache := &nonTasUsageCache{}
	pod := makePod("pod", "ns", "node-a", "1")
	pod.Annotations = map[string]string{kueue.WorkloadAnnotation: "parent"}
	cache.updateTAS(pod, logr.Discard())
	variant := &kueue.Workload{
		Namespace: "ns", Name: "variant",
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: kueue.SchemeGroupVersion.String(), Kind: "Workload", Name: "parent", UID: "parent-uid",
		}}}
	reserved := map[workload.Reference]*workload.Info{"ns/variant": {Obj: variant}}
	if got := cache.unreservedTASUsage(reserved); len(got) != 0 {
		t.Fatalf("variant double-counted its parent's bound Pod: %v", got)
	}
}
