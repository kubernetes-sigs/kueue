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

package queue

import (
	"testing"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestStrictFIFOPerFlavorBlocking tests the core blocking logic
func TestStrictFIFOPerFlavorBlocking(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq, _ := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFOPerFlavor,
			},
		},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
		nil, nil, nil)

	flavorA := kueue.ResourceFlavorReference("flavor-a")
	flavorB := kueue.ResourceFlavorReference("flavor-b")

	// Manually add wl1 to inadmissible list with flavor-a
	wl1 := utiltestingapi.MakeWorkload("wl1", defaultNamespace).Obj()
	info1 := workload.NewInfo(wl1)
	info1.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorA: {}}
	cq.rwm.Lock()
	cq.inadmissibleWorkloads.insert(workload.Key(wl1), info1)
	cq.inadmissibleBlockedFlavors[workload.Key(wl1)] = info1.AttemptedFlavors
	cq.rwm.Unlock()

	// Test 1: wl2 with same flavor should be blocked
	wl2 := utiltestingapi.MakeWorkload("wl2", defaultNamespace).Obj()
	info2 := workload.NewInfo(wl2)
	info2.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorA: {}}
	cq.RequeueIfNotPresent(ctx, info2, RequeueReasonFailedAfterNomination)

	// wl2 should be in inadmissible (blocked by wl1)
	if _, found := cq.inadmissibleWorkloads[workload.Key(wl2)]; !found {
		t.Error("wl2 with same flavor should be blocked and in inadmissible")
	}

	// Test 2: wl3 with different flavor should not be blocked
	wl3 := utiltestingapi.MakeWorkload("wl3", defaultNamespace).Obj()
	info3 := workload.NewInfo(wl3)
	info3.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorB: {}}
	cq.RequeueIfNotPresent(ctx, info3, RequeueReasonFailedAfterNomination)

	// wl3 should be in heap (not blocked)
	if cq.heap.GetByKey(workload.Key(wl3)) == nil {
		t.Error("wl3 with different flavor should not be blocked and should be in heap")
	}

	// Test 3: Delete wl1 and wl2, then wl4 with flavor-a should not be blocked
	cq.Delete(log, wl1)
	cq.Delete(log, wl2)

	wl4 := utiltestingapi.MakeWorkload("wl4", defaultNamespace).Obj()
	info4 := workload.NewInfo(wl4)
	info4.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorA: {}}
	cq.RequeueIfNotPresent(ctx, info4, RequeueReasonFailedAfterNomination)

	// wl4 should be in heap (wl1 and wl2 were deleted, no blocker)
	if cq.heap.GetByKey(workload.Key(wl4)) == nil {
		t.Error("wl4 should not be blocked after blockers were deleted")
	}
}

// TestStrictFIFOPerFlavorOverlappingFlavors tests blocking with multiple flavors
func TestStrictFIFOPerFlavorOverlappingFlavors(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cq, _ := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFOPerFlavor,
			},
		},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
		nil, nil, nil)

	flavorA := kueue.ResourceFlavorReference("flavor-a")
	flavorB := kueue.ResourceFlavorReference("flavor-b")
	flavorC := kueue.ResourceFlavorReference("flavor-c")

	// Manually add wl1 to inadmissible with flavors A and B
	wl1 := utiltestingapi.MakeWorkload("wl1", defaultNamespace).Obj()
	info1 := workload.NewInfo(wl1)
	info1.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{
		flavorA: {},
		flavorB: {},
	}
	cq.rwm.Lock()
	cq.inadmissibleWorkloads.insert(workload.Key(wl1), info1)
	cq.inadmissibleBlockedFlavors[workload.Key(wl1)] = info1.AttemptedFlavors
	cq.rwm.Unlock()

	// wl2 with flavor B should be blocked (overlaps with wl1)
	wl2 := utiltestingapi.MakeWorkload("wl2", defaultNamespace).Obj()
	info2 := workload.NewInfo(wl2)
	info2.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorB: {}}
	cq.RequeueIfNotPresent(ctx, info2, RequeueReasonFailedAfterNomination)

	if _, found := cq.inadmissibleWorkloads[workload.Key(wl2)]; !found {
		t.Error("wl2 should be blocked by wl1 (overlaps on flavor-b)")
	}

	// wl3 with flavor C should not be blocked
	wl3 := utiltestingapi.MakeWorkload("wl3", defaultNamespace).Obj()
	info3 := workload.NewInfo(wl3)
	info3.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorC: {}}
	cq.RequeueIfNotPresent(ctx, info3, RequeueReasonFailedAfterNomination)

	if cq.heap.GetByKey(workload.Key(wl3)) == nil {
		t.Error("wl3 should not be blocked (no flavor overlap)")
	}
}

// TestStrictFIFOPerFlavorCleanup tests cleanup of blocked flavors tracking
func TestStrictFIFOPerFlavorCleanup(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cq, _ := newClusterQueue(ctx, nil,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFOPerFlavor,
			},
		},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
		nil, nil, nil)

	flavorA := kueue.ResourceFlavorReference("flavor-a")

	// Manually add wl1 to inadmissible
	wl1 := utiltestingapi.MakeWorkload("wl1", defaultNamespace).Obj()
	info1 := workload.NewInfo(wl1)
	info1.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorA: {}}
	cq.rwm.Lock()
	cq.inadmissibleWorkloads.insert(workload.Key(wl1), info1)
	cq.inadmissibleBlockedFlavors[workload.Key(wl1)] = info1.AttemptedFlavors
	cq.rwm.Unlock()

	key := workload.Key(wl1)
	if _, found := cq.inadmissibleBlockedFlavors[key]; !found {
		t.Fatal("expected blocked flavors to be tracked")
	}

	// Delete workload
	cq.Delete(log, wl1)

	// Verify cleanup
	if _, found := cq.inadmissibleBlockedFlavors[key]; found {
		t.Error("expected blocked flavors to be cleaned up after deletion")
	}
}
