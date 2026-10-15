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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

// The scheduler's admission-write operation must refuse even quota reservation
// while a Finished or non-Finished ancestor still needs remote cleanup.
func TestReserveQuotaWaitsForSliceEvictionCleanup(t *testing.T) {
	for name, tc := range map[string]struct {
		finished, complete, sameSecondEviction, otherChain, workerQueue bool
		wantBlocked                                                     bool
	}{
		"evicted predecessor":                {wantBlocked: true},
		"replaced predecessor":               {finished: true, wantBlocked: true},
		"completed historical eviction":      {finished: true, complete: true},
		"second eviction in the same second": {complete: true, sameSecondEviction: true, wantBlocked: true},
		"unrelated chain":                    {otherChain: true},
		"worker queue without MultiKueue":    {workerQueue: true},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, true)
			ctx, log := utiltesting.ContextWithLog(t)
			now := time.Now().Truncate(time.Second)
			root := utiltestingapi.MakeWorkload("root", "default").
				Annotation(workloadslicing.EnabledAnnotationKey, "true").
				Label(constants.JobUIDLabel, "job-uid").
				Condition(metav1.Condition{Type: kueue.WorkloadEvicted, Status: metav1.ConditionTrue, LastTransitionTime: metav1.NewTime(now)}).Obj()
			if tc.finished {
				root.Status.Conditions = append(root.Status.Conditions, metav1.Condition{Type: kueue.WorkloadFinished, Status: metav1.ConditionTrue, Reason: kueue.WorkloadSliceReplaced})
			}
			if tc.complete {
				root.Annotations[constants.MultiKueueEvictionCleanupAnnotation] = workloadslicing.EvictionCleanupToken(root)
			}
			if tc.sameSecondEviction {
				root.Status.SchedulingStats = &kueue.SchedulingStats{Evictions: []kueue.WorkloadSchedulingStatsEviction{{Reason: kueue.WorkloadEvictedByPreemption, Count: 1}}}
			}
			chain := "root"
			if tc.otherChain {
				chain = "other-root"
			}
			pending := utiltestingapi.MakeWorkload("pending", "default").Queue("lq").
				Annotation(workloadslicing.EnabledAnnotationKey, "true").
				Annotation(kueue.WorkloadSliceNameAnnotation, chain).
				Label(constants.JobUIDLabel, "job-uid").
				ControllerReference(schema.GroupVersionKind{Group: "ray.io", Version: "v1", Kind: "RayCluster"}, "ray", "job-uid").
				PodSets(*utiltestingapi.MakePodSet("main", 1).Request(corev1.ResourceCPU, "1").Obj()).Obj()
			stale := root.DeepCopy()
			stale.Status.Conditions = nil
			cl := utiltesting.NewClientBuilder().WithObjects(pending, stale).WithStatusSubresource(&kueue.Workload{}).Build()
			direct := utiltesting.NewClientBuilder().WithObjects(root).Build()
			cache := schdcache.New(cl)
			ac := utiltestingapi.MakeAdmissionCheck("mk").ControllerName(kueue.MultiKueueControllerName).Active(metav1.ConditionTrue).Obj()
			cache.AddOrUpdateAdmissionCheck(log, ac)
			cq := utiltestingapi.MakeClusterQueue("cq").ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
			if !tc.workerQueue {
				cq.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{AdmissionChecks: []kueue.AdmissionCheckStrategyRule{{Name: "mk"}}}
			}
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatal(err)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatal(err)
			}
			admission := utiltestingapi.MakeAdmission("cq").PodSets(utiltestingapi.MakePodSetAssignment("main").Assignment(corev1.ResourceCPU, "default", "1").Count(1).Obj()).Obj()
			scheduler := &Scheduler{client: cl, apiReader: direct, clock: testingclock.NewFakeClock(now)}
			err = scheduler.patchWorkloadAdmission(ctx, log, pending, snapshot.ClusterQueue("cq"), admission)
			if tc.wantBlocked && err == nil {
				t.Fatal("quota reservation succeeded before cleanup")
			}
			if !tc.wantBlocked && err != nil {
				t.Fatal(err)
			}
			actual := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(pending), actual); err != nil {
				t.Fatal(err)
			}
			if workload.HasQuotaReservation(actual) == tc.wantBlocked {
				t.Fatalf("quotaReserved=%v, blocked=%v", workload.HasQuotaReservation(actual), tc.wantBlocked)
			}
			if tc.wantBlocked {
				root.Annotations[constants.MultiKueueEvictionCleanupAnnotation] = workloadslicing.EvictionCleanupToken(root)
				if err := direct.Update(ctx, root); err != nil {
					t.Fatal(err)
				}
				// A new scheduler instance must recover solely from the persisted ACK.
				restarted := &Scheduler{client: cl, apiReader: direct, clock: testingclock.NewFakeClock(now)}
				if err := restarted.patchWorkloadAdmission(ctx, log, actual, snapshot.ClusterQueue("cq"), admission); err != nil {
					t.Fatal(err)
				}
				if !workload.HasQuotaReservation(actual) {
					t.Fatal("reservation did not resume after cleanup")
				}
			}
		})
	}
}
