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

	"k8s.io/apimachinery/pkg/labels"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestStrictFIFOPerFlavorBlocking tests the core blocking logic
func TestStrictFIFOPerFlavorBlocking(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
	cq, _ := newClusterQueue(ctx, cl,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFOPerFlavor,
			},
		},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
		nil, nil, nil)
	cq.namespaceSelector = labels.Everything()

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

	// Test 1: wl2 with same flavor should be blocked and stay in inadmissible
	wl2 := utiltestingapi.MakeWorkload("wl2", defaultNamespace).Obj()
	info2 := workload.NewInfo(wl2)
	info2.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorA: {}}
	cq.RequeueIfNotPresent(ctx, info2, RequeueReasonFailedAfterNomination)

	// wl2 should be in inadmissible (all workloads go to inadmissible initially)
	if _, found := cq.inadmissibleWorkloads[workload.Key(wl2)]; !found {
		t.Error("wl2 should be in inadmissible")
	}

	// Test 2: wl3 with different flavor goes to inadmissible but should move to heap on QueueInadmissible
	wl3 := utiltestingapi.MakeWorkload("wl3", defaultNamespace).Obj()
	info3 := workload.NewInfo(wl3)
	info3.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorB: {}}
	cq.RequeueIfNotPresent(ctx, info3, RequeueReasonFailedAfterNomination)

	// wl3 should be in inadmissible initially
	if _, found := cq.inadmissibleWorkloads[workload.Key(wl3)]; !found {
		t.Error("wl3 should be in inadmissible initially")
	}

	// Trigger QueueInadmissibleWorkloads - wl3 should move to heap (not blocked), wl2 stays (blocked)
	cq.QueueInadmissibleWorkloads(ctx, cl)

	if cq.heap.GetByKey(workload.Key(wl3)) == nil {
		t.Error("wl3 with different flavor should move to heap after QueueInadmissible")
	}
	if _, found := cq.inadmissibleWorkloads[workload.Key(wl2)]; !found {
		t.Error("wl2 should stay inadmissible (blocked by wl1)")
	}

	// Test 3: Delete wl1, then wl2 should move to heap on next QueueInadmissible
	cq.Delete(log, wl1)
	cq.QueueInadmissibleWorkloads(ctx, cl)

	if cq.heap.GetByKey(workload.Key(wl2)) == nil {
		t.Error("wl2 should move to heap after wl1 deleted")
	}
}

// TestStrictFIFOPerFlavorOverlappingFlavors tests blocking with multiple flavors
func TestStrictFIFOPerFlavorOverlappingFlavors(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewFakeClient(utiltesting.MakeNamespace(defaultNamespace))
	cq, _ := newClusterQueue(ctx, cl,
		&kueue.ClusterQueue{
			Spec: kueue.ClusterQueueSpec{
				QueueingStrategy: kueue.StrictFIFOPerFlavor,
			},
		},
		workload.Ordering{PodsReadyRequeuingTimestamp: config.EvictionTimestamp},
		nil, nil, nil)
	cq.namespaceSelector = labels.Everything()

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
		t.Error("wl2 should be in inadmissible")
	}

	// wl3 with flavor C should not be blocked but goes to inadmissible initially
	wl3 := utiltestingapi.MakeWorkload("wl3", defaultNamespace).Obj()
	info3 := workload.NewInfo(wl3)
	info3.AttemptedFlavors = map[kueue.ResourceFlavorReference]struct{}{flavorC: {}}
	cq.RequeueIfNotPresent(ctx, info3, RequeueReasonFailedAfterNomination)

	// Trigger QueueInadmissibleWorkloads - wl3 should move to heap, wl2 should stay
	cq.QueueInadmissibleWorkloads(ctx, cl)

	if cq.heap.GetByKey(workload.Key(wl3)) == nil {
		t.Error("wl3 should move to heap (no flavor overlap with wl1)")
	}
	if _, found := cq.inadmissibleWorkloads[workload.Key(wl2)]; !found {
		t.Error("wl2 should stay inadmissible (blocked by wl1 on flavor-b)")
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
