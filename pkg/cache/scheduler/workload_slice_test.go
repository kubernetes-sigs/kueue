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

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"

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
	jobGVK := batchv1.SchemeGroupVersion.WithKind("Job")
	// Every slice of a chain carries the chain's slice name; slices of a job also have it as controller.
	base := utiltestingapi.MakeWorkload("", "ns").Annotation(kueue.WorkloadSliceNameAnnotation, "old").
		ControllerReference(jobGVK, "job", "job-uid")
	old := base.Clone().Name("old").UID("old").Creation(now.Add(-3*time.Second)).
		Request(corev1.ResourceCPU, "2").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "2").Obj()).Obj(), now)
	replacement := base.Clone().Name("new").UID("new").Creation(now.Add(-2*time.Second)).
		Annotation(constants.WorkloadSliceReplacementForAnnotation, "ns/old").
		Request(corev1.ResourceCPU, "3").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "3").Obj()).Obj(), now)
	small := base.Clone().Request(corev1.ResourceCPU, "1").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "1").Obj()).Obj(), now)

	oldSlice := old.Clone().Obj()
	oldSliceFinished := old.Clone().Finished().Obj()
	newSlice := replacement.Clone().Obj()
	newSliceFinished := replacement.Clone().Finished().Obj()
	newestSlice := base.Clone().Name("newest").UID("newest").Creation(now.Add(-time.Second)).
		Annotation(constants.WorkloadSliceReplacementForAnnotation, "ns/new").
		Request(corev1.ResourceCPU, "4").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "4").Obj()).Obj(), now).
		Obj()
	otherAtOldKey := small.Clone().Name("old").UID("other").Creation(now.Add(-3 * time.Second)).Obj()
	otherAtOldKey.OwnerReferences[0].Name, otherAtOldKey.OwnerReferences[0].UID = "other-job", "other-job-uid"
	otherNamespace := small.Clone().Name("old").UID("other-ns").Creation(now.Add(-3 * time.Second)).Obj()
	otherNamespace.Namespace = "other-ns"
	replacesOtherNamespace := replacement.Clone().Annotation(constants.WorkloadSliceReplacementForAnnotation, "other-ns/old").Obj()
	selfReplacing := small.Clone().Name("self").UID("self").Creation(now).
		Annotation(constants.WorkloadSliceReplacementForAnnotation, "ns/self").Obj()
	// TAS slices keep charging every slice: a newer slice may be placed on other nodes than the
	// running Pods of an older one.
	tasOld := old.Clone().PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
		Request(corev1.ResourceCPU, "2").UnconstrainedTopologyRequest().Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "2").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 1).Obj()).Obj()).Obj()).Obj(), now).
		Obj()
	tasNew := replacement.Clone().PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
		Request(corev1.ResourceCPU, "3").UnconstrainedTopologyRequest().Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
			Assignment(corev1.ResourceCPU, "default", "3").
			TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node"}, 1).Obj()).Obj()).Obj()).Obj(), now).
		Obj()
	shrinkingNew := small.Clone().Name("new").UID("new").Creation(now.Add(-2*time.Second)).
		Annotation(constants.WorkloadSliceReplacementForAnnotation, "ns/old").Obj()
	unownedOld := oldSlice.DeepCopy()
	unownedOld.OwnerReferences = nil
	unownedNew := newSlice.DeepCopy()
	unownedNew.OwnerReferences = nil

	type step struct {
		add, finish, del *kueue.Workload // finish is a Finished copy of a cached workload
		delIfUnchanged   *kueue.Workload
		wantCPU          string
		wantWorkloads    []workload.Reference
	}
	testCases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		steps        []step
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
		"a replaced slice is deleted if unchanged": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{delIfUnchanged: oldSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
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
		"a chain of three slices is charged once": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{add: newestSlice, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/newest"}},
				{del: newestSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{finish: oldSliceFinished, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{finish: newSliceFinished, wantCPU: "0"},
			},
		},
		"the oldest slice is charged again when the newest leaves after the middle one finished": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{add: newestSlice, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/newest"}},
				{finish: newSliceFinished, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/newest"}},
				{add: newestSlice, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/newest"}},
				{del: newestSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
			},
		},
		"Workloads without a controller are charged": {
			steps: []step{
				{add: unownedOld, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: unownedNew, wantCPU: "5", wantWorkloads: []workload.Reference{"ns/new", "ns/old"}},
			},
		},
		"another Workload at the replaced slice's key is charged": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "3", wantWorkloads: []workload.Reference{"ns/new"}},
				{add: otherAtOldKey, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/new", "ns/old"}},
				{add: newSlice, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/new", "ns/old"}},
			},
		},
		"a Workload of another namespace is charged": {
			steps: []step{
				{add: otherNamespace, wantCPU: "1", wantWorkloads: []workload.Reference{"other-ns/old"}},
				{add: replacesOtherNamespace, wantCPU: "4", wantWorkloads: []workload.Reference{"ns/new", "other-ns/old"}},
			},
		},
		"a Workload naming itself is charged": {
			steps: []step{
				{add: selfReplacing, wantCPU: "1", wantWorkloads: []workload.Reference{"ns/self"}},
			},
		},
		"a replacement that shrinks is covered by the slice it replaces": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: shrinkingNew, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{finish: oldSliceFinished, wantCPU: "1", wantWorkloads: []workload.Reference{"ns/new"}},
			},
		},
		"a replacement that shrinks arrives before the slice it replaces": {
			steps: []step{
				{add: shrinkingNew, wantCPU: "1", wantWorkloads: []workload.Reference{"ns/new"}},
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
			},
		},
		"a replacement that shrinks and leaves first charges nothing more": {
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: shrinkingNew, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{del: shrinkingNew, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
			},
		},
		"TAS slices are both charged": {
			steps: []step{
				{add: tasOld, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: tasNew, wantCPU: "5", wantWorkloads: []workload.Reference{"ns/new", "ns/old"}},
			},
		},
		"gate off charges both": {
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
			steps: []step{
				{add: oldSlice, wantCPU: "2", wantWorkloads: []workload.Reference{"ns/old"}},
				{add: newSlice, wantCPU: "5", wantWorkloads: []workload.Reference{"ns/new", "ns/old"}},
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for fg, enabled := range tc.featureGates {
				features.SetFeatureGateDuringTest(t, fg, enabled)
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
				case s.delIfUnchanged != nil:
					if !cache.DeleteWorkloadIfUnchanged(log, s.delIfUnchanged) {
						t.Fatalf("Step %d: DeleteWorkloadIfUnchanged did not delete the workload", i)
					}
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
