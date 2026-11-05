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
	"slices"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TestReplacedWorkloadSliceUsage checks that a workload slice and the slice it replaces are
// charged once, by the replacement, while both are in the cache.
func TestReplacedWorkloadSliceUsage(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
		Obj()
	slice := func(name, cpu string) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload(name, "ns").
			Request(corev1.ResourceCPU, cpu).
			ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", cpu).Obj()).
				Obj(), now)
	}
	oldSlice := slice("old", "2").Obj()
	oldSliceFinished := slice("old", "2").Finished().Obj()
	newSlice := slice("new", "3").Annotation(constants.WorkloadSliceReplacementForAnnotation, "ns/old").Obj()

	type step struct {
		add, finish, del *kueue.Workload // finish is a Finished copy of a cached workload
		wantCPU          string
		wantWorkloads    []workload.Reference
	}
	cases := map[string]struct {
		gateOff bool
		steps   []step
	}{
		"the old slice finishes first": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{add: oldSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{finish: oldSliceFinished, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{del: newSlice, wantCPU: "0"},
			},
		},
		"the replacement leaves first": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{del: newSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{finish: oldSliceFinished, wantCPU: "0"},
			},
		},
		"gate off charges both": {
			gateOff: true,
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "5", wantWorkloads: []workload.Reference{"ns/new", "ns/old"}},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.gateOff {
				features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, false)
			}
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			for i, s := range tc.steps {
				switch {
				case s.add != nil:
					cache.AddOrUpdateWorkload(ctx, log, s.add.DeepCopy())
				case s.finish != nil:
					cache.AddOrUpdateWorkload(ctx, log, s.finish.DeepCopy())
				case s.del != nil:
					if err := cache.DeleteWorkload(log, workload.Key(s.del)); err != nil {
						t.Fatalf("Step %d: deleting workload: %v", i, err)
					}
				}
				stats, err := cache.Usage(cq)
				if err != nil {
					t.Fatalf("Step %d: getting usage: %v", i, err)
				}
				gotCPU := stats.ReservedResources[0].Resources[0].Total
				if want := resource.MustParse(s.wantCPU); gotCPU.Cmp(want) != 0 {
					t.Errorf("Step %d: reserved CPU = %s, want %s", i, gotCPU.String(), want.String())
				}
				snapshot, err := cache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("Step %d: taking snapshot: %v", i, err)
				}
				got := slices.Sorted(maps.Keys(snapshot.ClusterQueue("cq").Workloads))
				if !slices.Equal(got, s.wantWorkloads) {
					t.Errorf("Step %d: snapshot workloads = %v, want %v", i, got, s.wantWorkloads)
				}
			}
		})
	}
}
