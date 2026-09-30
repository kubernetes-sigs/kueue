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

	corev1 "k8s.io/api/core/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAssignFlavorsZeroCountRetry(t *testing.T) {
	const gpu = corev1.ResourceName("example.com/gpu")
	cases := map[string]struct {
		count              int32
		otherPodSetBlocked bool
		firstQuota         string
		secondQuota        string
		lastTried          int
		restrictToSecond   bool
		wantFlavor         kueue.ResourceFlavorReference
		wantMode           FlavorAssignmentMode
		wantTriedFlavorIdx int
		wantFallback       bool
	}{
		"probe revisits a suitable flavor before falling back": {
			firstQuota: "2", secondQuota: "0", lastTried: 0,
			wantFlavor: "first", wantMode: Fit, wantTriedFlavorIdx: -1,
		},
		"wrapped probe exhausts the scan when another PodSet still waits": {
			firstQuota: "2", secondQuota: "0", lastTried: 0, otherPodSetBlocked: true,
			wantFlavor: "first", wantMode: Preempt, wantTriedFlavorIdx: -1,
		},
		"probe keeps the next suitable flavor": {
			firstQuota: "2", secondQuota: "2", lastTried: 0,
			wantFlavor: "second", wantMode: Fit, wantTriedFlavorIdx: -1,
		},
		"probe still falls back when neither flavor fits": {
			firstQuota: "0", secondQuota: "0", lastTried: 0,
			wantFlavor: "second", wantMode: Fit, wantTriedFlavorIdx: -1, wantFallback: true,
		},
		"probe respects node affinity when revisiting a flavor": {
			firstQuota: "2", secondQuota: "0", lastTried: 0, restrictToSecond: true,
			wantFlavor: "second", wantMode: Fit, wantTriedFlavorIdx: -1, wantFallback: true,
		},
		"fresh probe prefers the first suitable flavor": {
			firstQuota: "2", secondQuota: "2", lastTried: -1,
			wantFlavor: "first", wantMode: Fit,
		},
		"positive count continues without revisiting earlier flavors": {
			count: 1, firstQuota: "2", secondQuota: "0", lastTried: 0,
			wantMode: NoFit,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.FlavorFungibility, true)
			ctx, log := utiltesting.ContextWithLog(t)
			flavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"first":  utiltestingapi.MakeResourceFlavor("first").NodeLabel("instance-type", "first").Obj(),
				"second": utiltestingapi.MakeResourceFlavor("second").NodeLabel("instance-type", "second").Obj(),
			}
			cq := utiltestingapi.MakeClusterQueue("cq").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("first").Resource(gpu, tc.firstQuota).Obj(),
				*utiltestingapi.MakeFlavorQuotas("second").Resource(gpu, tc.secondQuota).Obj(),
			).Obj()
			cache := schdcache.New(utiltesting.NewFakeClient())
			for _, flavor := range flavors {
				cache.AddOrUpdateResourceFlavor(log, flavor)
			}
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			ps := utiltestingapi.MakePodSet("worker", int(tc.count)).Request(gpu, "1").Obj()
			if tc.restrictToSecond {
				ps.Template.Spec.NodeSelector = map[string]string{"instance-type": "second"}
			}
			podSets := []kueue.PodSet{*ps}
			scan := &workload.FlavorScanState{LastTriedFlavorIndexes: []map[corev1.ResourceName]int{{gpu: tc.lastTried}}}
			if tc.otherPodSetBlocked {
				podSets = append(podSets, *utiltestingapi.MakePodSet("active", 1).Request(gpu, "1").Obj())
				scan.LastTriedFlavorIndexes = append(scan.LastTriedFlavorIndexes, map[corev1.ResourceName]int{gpu: -1})
				snapshot.ClusterQueue("cq").AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{
					{Flavor: "first", Resource: gpu}: resources.NewAmount(2),
				}}})
			}
			info := workload.NewInfo(log, utiltestingapi.MakeWorkload("wl", "ns").PodSets(podSets...).Obj())
			info.FlavorScanState = scan
			oracle := &testOracle{simulationResult: map[resources.FlavorResource]simulationResultForFlavor{
				{Flavor: "first", Resource: gpu}: {preemptionPossiblity: preemptioncommon.NoCandidates},
			}}
			assigner := New(info, snapshot.ClusterQueue("cq"), flavors, false, oracle, nil,
				configapi.QuotaCheckBlockUndeclared, resources.NewResourceFormatter(), 2)
			assignment := assigner.AssignFlavors(ctx, log, nil)
			if got := assignment.RepresentativeMode(); got != tc.wantMode {
				t.Fatalf("assignment mode = %v, want %v: %s", got, tc.wantMode, assignment.Message())
			}
			if got := assignment.ZeroCountFlavorFallback != ""; got != tc.wantFallback {
				t.Errorf("fallback = %q, want fallback %t", assignment.ZeroCountFlavorFallback, tc.wantFallback)
			}
			if tc.wantFlavor != "" {
				flavor := assignment.PodSets[0].Flavors[gpu]
				if flavor == nil || flavor.Name != tc.wantFlavor {
					t.Fatalf("flavor = %v, want %s", flavor, tc.wantFlavor)
				}
				if got := flavor.TriedFlavorIdx; got != tc.wantTriedFlavorIdx {
					t.Errorf("last tried index = %d, want %d", got, tc.wantTriedFlavorIdx)
				}
			}
			if tc.otherPodSetBlocked && assignment.FlavorScanState.PendingFlavors() {
				t.Error("exhausted probe must not keep a blocked workload immediately retrying")
			}
			if tc.count == 0 && !tc.otherPodSetBlocked {
				for fr, usage := range assignment.Usage.Quota.Assigned {
					if usage.Cmp(resources.NewAmount(0)) != 0 {
						t.Errorf("zero-count assignment consumed quota for %v: %v", fr, usage)
					}
				}
			}
		})
	}
}
