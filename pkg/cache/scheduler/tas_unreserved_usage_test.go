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
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	coreindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
)

func TestObserveTASWorkloadIntent(t *testing.T) {
	cases := map[string]struct {
		tasEnabled, concurrentEnabled, parent, variant, wantIntent bool
	}{
		"TAS disabled":                                    {},
		"evicted pending Workload":                        {tasEnabled: true, wantIntent: true},
		"pending concurrent parent":                       {tasEnabled: true, concurrentEnabled: true, parent: true},
		"concurrent variant":                              {tasEnabled: true, concurrentEnabled: true, variant: true, wantIntent: true},
		"parent label with concurrent admission disabled": {tasEnabled: true, parent: true, wantIntent: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.tasEnabled)
			features.SetFeatureGateDuringTest(t, features.ConcurrentAdmission, tc.concurrentEnabled)
			cache := New(utiltesting.NewFakeClient())
			wl := utiltestingapi.MakeWorkload("evicted", "ns").Obj()
			wl.UID = "uid"
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
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
		})
	}
}

func TestDeleteClusterQueueReleasesTASReservation(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
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

func TestReleasedTASWorkloadIntent(t *testing.T) {
	cases := map[string]struct{ podFirst bool }{
		"Workload observed before Pod": {},
		"Pod observed before Workload": {podFirst: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := &nonTasUsageCache{}
			log := logr.Discard()
			wl := utiltestingapi.MakeWorkload("victim", "ns").Obj()
			wl.UID = "old-workload"
			wl.OwnerReferences = []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", UID: "old-owner"}}
			wl.Status.Conditions = []metav1.Condition{{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue}}
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
			// Clearing the eviction while pending removes the intent.
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
