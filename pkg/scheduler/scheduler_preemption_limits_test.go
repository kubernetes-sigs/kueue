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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestGetAssignmentsPreemptionLimits(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	rfOnDemand := utiltestingapi.MakeResourceFlavor("on-demand").Obj()
	rfSpot := utiltestingapi.MakeResourceFlavor("spot").Obj()
	preemptionCfg := utiltestingalpha.MakePreemptionConfig("limits-cfg").
		Rule("within-cq", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).
		Obj()
	preemptionLimit := utiltestingalpha.MakePreemptionLimit("per-workload", kueuealpha.ByPreempteeWorkloadLimitAggregation).
		UID("per-workload-uid").
		Limit(1).WindowSeconds(60).Obj()
	limitExhaustedVictim := utiltestingapi.MakeWorkload("v-od-limit-exhausted", "default").
		Queue("limits-lq").
		Request(corev1.ResourceCPU, "1").
		SimpleReserveQuota("limits-cq", "on-demand", now).
		AdmittedAt(true, now).
		Obj()
	incoming := utiltestingapi.MakeWorkload("incoming", "default").
		Queue("limits-lq").
		Request(corev1.ResourceCPU, "1").
		Obj()

	cases := map[string]struct {
		spotQuota    string
		wantRetryAt  *time.Time
		wantBlocking *policy.BlockingPreemptionLimit
	}{
		"returns retryAt and blockingLimit when all flavors are blocked by expiring limit": {
			spotQuota:   "0",
			wantRetryAt: new(now.Add(10 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-workload",
				Aggregation: kueuealpha.ByPreempteeWorkloadLimitAggregation,
				RetryAt:     new(now.Add(10 * time.Second)),
			},
		},
		"returns nil blockingLimit, preserves retryAt, and leaves preemptor drained when fallback flavor fits": {
			spotQuota:   "1",
			wantRetryAt: new(now.Add(10 * time.Second)),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			cq := utiltestingapi.MakeClusterQueue("limits-cq").
				Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, tc.spotQuota).Obj(),
				).
				Obj()
			lq := utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj()
			cl := utiltesting.NewClientBuilder().WithObjects(
				utiltesting.MakeNamespace("default"),
				rfOnDemand, rfSpot, cq, lq, preemptionCfg, preemptionLimit, limitExhaustedVictim, incoming,
			).Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, rfOnDemand)
			cqCache.AddOrUpdateResourceFlavor(log, rfSpot)
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("adding ClusterQueue to cache: %v", err)
			}

			limitTracker := limits.NewPreemptionLimitTracker()
			limitTracker.Sync(preemptionLimit)
			limitTracker.Record(now.Add(-50*time.Second), policy.ConsumedLimits{
				"per-workload": policy.NewConsumedLimit(preemptionLimit, "default/v-od-limit-exhausted"),
			})

			qManager := qcache.NewManagerForUnitTests(cl, cqCache)
			sched := New(
				qManager, cqCache, cl, &utiltesting.EventRecorder{},
				WithClock(t, testingclock.NewFakeClock(now)),
				WithPreemptionExpectations(preemptexpectations.New()),
				WithPreemptionLimitTracker(limitTracker),
			)
			sched.preemptor.SnapshotPreemptionLimits()
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}

			wlInfo := workload.NewInfo(log, incoming)
			wlInfo.ClusterQueue = "limits-cq"
			result, err := sched.getAssignments(ctx, log, wlInfo, snap)
			if err != nil {
				t.Fatalf("getAssignments() unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantRetryAt, result.retryAt); diff != "" {
				t.Errorf("getAssignments retryAt (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBlocking, result.blockingLimit); diff != "" {
				t.Errorf("getAssignments blockingLimit (-want,+got):\n%s", diff)
			}
			if leaked := sched.preemptor.TakeRetryAt(); leaked != nil {
				t.Errorf("preemptor.TakeRetryAt() = %v after getAssignments, want nil", leaked)
			}
			if leakedBlocking := sched.preemptor.TakeBlockingLimit(); leakedBlocking != nil {
				t.Errorf("preemptor.TakeBlockingLimit() = %v after getAssignments, want nil", leakedBlocking)
			}
		})
	}
}

func TestSchedulePreemptionLimits(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.ConfigurablePreemptions:      true,
		features.ConfigurablePreemptionLimits: true,
	})
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	fakeClock := testingclock.NewFakeClock(now)
	ignoreEventMessageCmpOpts := cmp.Options{cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")}

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
	}
	preemptionCfg := utiltestingalpha.MakePreemptionConfig("limits-cfg").
		Rule("within-cq", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).
		Obj()
	perWorkloadLimit := utiltestingalpha.MakePreemptionLimit("per-workload", kueuealpha.ByPreempteeWorkloadLimitAggregation).
		UID("per-workload-uid").
		Limit(1).WindowSeconds(60).Obj()
	vODConsumed := policy.ConsumedLimits{"per-workload": policy.NewConsumedLimit(perWorkloadLimit, "default/v-od")}
	vSpotConsumed := policy.ConsumedLimits{"per-workload": policy.NewConsumedLimit(perWorkloadLimit, "default/v-spot")}

	cases := map[string]scheduleTestCase{
		"PreemptionLimits are ignored when ConfigurablePreemptionLimits feature gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.ConfigurablePreemptionLimits: false,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("blocked-cq").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("blocked-lq", "sales").ClusterQueue("blocked-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				utiltestingalpha.MakePreemptionLimit("blocked", kueuealpha.GlobalLimitAggregation).UID("blocked-uid").Limit(0).WindowSeconds(60).Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("blocked-lq").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("blocked-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("blocked-lq").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("blocked-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted by sales/new because of preemption config limits-cfg rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.ConfigurablePreemptionReason,
						Message:            "Preempted by sales/new because of preemption config limits-cfg rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPreemption,
						UnderlyingCause: "limits-cfg",
						Count:           1,
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"blocked-cq": {"sales/new"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"sales/victim": *utiltestingapi.MakeAdmission("blocked-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj(),
			},
		},
		"configurable preemption within a PreemptionLimit is issued while blocked ClusterQueue reports BlockingPreemptionLimit": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limited-cq").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					Obj(),
				*utiltestingapi.MakeClusterQueue("blocked-cq").
					Label("preemption", "blocked").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limited-lq", "sales").ClusterQueue("limited-cq").Obj(),
				*utiltestingapi.MakeLocalQueue("blocked-lq", "sales").ClusterQueue("blocked-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).UID("global-uid").Limit(1).WindowSeconds(60).Obj(),
				utiltestingalpha.MakePreemptionLimit("blocked", kueuealpha.GlobalLimitAggregation).
					UID("blocked-uid").
					PreemptorClusterQueueSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"preemption": "blocked"}}).
					Limit(0).
					WindowSeconds(60).
					Obj(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("limited-lq").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("limited-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("blocked", "sales").
					Queue("blocked-lq").
					Request(corev1.ResourceCPU, "1").
					Obj(),
				*utiltestingapi.MakeWorkload("blocked-victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("blocked-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("blocked", "sales").
					Queue("blocked-lq").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed; preemptions blocked by PreemptionLimit blocked (aggregation Global)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("blocked-victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("blocked-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("new", "sales").
					Queue("limited-lq").
					Request(corev1.ResourceCPU, "1").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("1"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("victim", "sales").
					Request(corev1.ResourceCPU, "1").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("limited-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "1").
							Obj()).
						Obj(), now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted by sales/new because of preemption config limits-cfg rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.ConfigurablePreemptionReason,
						Message:            "Preempted by sales/new because of preemption config limits-cfg rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPreemption,
						UnderlyingCause: "limits-cfg",
						Count:           1,
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				// Preemptor is not admitted in this cycle; it waits for the victim's eviction to complete.
				"limited-cq": {"sales/new"},
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"blocked-cq": {"sales/blocked"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				// Removal from cache for the preempted workload is deferred until we receive Workload updates.
				"sales/victim": *utiltestingapi.MakeAdmission("limited-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj(),
				"sales/blocked-victim": *utiltestingapi.MakeAdmission("blocked-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "default", "1").
						Obj()).
					Obj(),
			},
		},
		"standalone ClusterQueue requeues workload after earliest PreemptionLimit expiry across flavors": {
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limits-cq").
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "1").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				perWorkloadLimit.DeepCopy(),
			},
			recordedPreemptions: []recordedPreemption{
				{at: now.Add(-35 * time.Second), consumed: vODConsumed},
				{at: now.Add(-50 * time.Second), consumed: vSpotConsumed},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("v-spot", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "spot", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed, insufficient unused quota for cpu in flavor spot, 1 more needed; preemptions blocked by PreemptionLimit per-workload (aggregation ByPreempteeWorkload) until 2026-01-01T00:00:10Z",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name:      kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("v-spot", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("limits-cq", "spot", now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v-od": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "1").Obj()).
					Obj(),
				"default/v-spot": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "1").Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"limits-cq": {"default/incoming"},
			},
			wantClusterQueueRequeueDelays: map[kueue.ClusterQueueReference]time.Duration{
				"limits-cq": 10 * time.Second,
			},
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       client.ObjectKey{Namespace: "default", Name: "incoming"},
					Reason:    kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					EventType: corev1.EventTypeWarning,
					Message:   "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 1 more needed, insufficient unused quota for cpu in flavor spot, 1 more needed; preemptions blocked by PreemptionLimit per-workload (aggregation ByPreempteeWorkload) until 2026-01-01T00:00:10Z",
				},
			},
		},
		// Full count (4 CPU) exceeds max capacity (2 CPU) without checking preemption; only the
		// reduced-count attempts (2..1 CPU) invoke preemption and hit the exhausted limit on v-od,
		// so QuotaReserved keeps the full-count ExceedsMaxQuota message while preserving retryAt.
		"partially admissible workload blocked by PreemptionLimit at reduced counts arms retry delay": {
			featureGates: map[featuregate.Feature]bool{
				features.PartialAdmission: true,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limits-cq").
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				perWorkloadLimit.DeepCopy(),
			},
			recordedPreemptions: []recordedPreemption{
				{at: now.Add(-50 * time.Second), consumed: vODConsumed},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
							SetMinimumCount(1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
							SetMinimumCount(1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
						Message:            "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor on-demand, previously considered podsets requests (0) + current podset request (4) > maximum capacity (2); preemptions blocked by PreemptionLimit per-workload (aggregation ByPreempteeWorkload) until 2026-01-01T00:00:10Z",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name:      kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v-od": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()).
					Obj(),
			},
			wantInadmissibleLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"limits-cq": {"default/incoming"},
			},
			wantClusterQueueRequeueDelays: map[kueue.ClusterQueueReference]time.Duration{
				"limits-cq": 10 * time.Second,
			},
			eventCmpOpts: ignoreEventMessageCmpOpts,
			wantEvents: []utiltesting.EventRecord{
				utiltesting.MakeEventRecord("default", "incoming", kueue.WorkloadQuotaReservedReasonExceedsMaxQuota, corev1.EventTypeWarning).Obj(),
			},
		},
		// Same setup as above, without a consumed PreemptionLimit: the reduced count (2 CPU)
		// preempts v-od rather than failing with ExceedsMaxQuota.
		"partially admissible workload within PreemptionLimit preempts at reduced counts": {
			featureGates: map[featuregate.Feature]bool{
				features.PartialAdmission: true,
			},
			additionalClusterQueues: []kueue.ClusterQueue{
				*utiltestingapi.MakeClusterQueue("limits-cq").
					Annotation(kueuealpha.PreemptionConfigNameAnnotation, "limits-cfg").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "2").Obj(),
					).
					Obj(),
			},
			additionalLocalQueues: []kueue.LocalQueue{
				*utiltestingapi.MakeLocalQueue("limits-lq", "default").ClusterQueue("limits-cq").Obj(),
			},
			objects: []client.Object{
				preemptionCfg,
				perWorkloadLimit.DeepCopy(),
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
							SetMinimumCount(1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("incoming", "default").
					Queue("limits-lq").
					PodSets(
						*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
							SetMinimumCount(1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads,
						Message:            "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor on-demand, 2 more needed. Pending the preemption of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadAdmittedReasonNoReservation,
						Message:            "The workload has no reservation",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name:      kueue.DefaultPodSetName,
						Resources: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("v-od", "default").
					Queue("limits-lq").
					Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("limits-cq", "on-demand", now).
					AdmittedAt(true, now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPreemption,
						Message:            "Preempted by default/incoming because of preemption config limits-cfg rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.ConfigurablePreemptionReason,
						Message:            "Preempted by default/incoming because of preemption config limits-cfg rule within-cq/0",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPreemption,
						UnderlyingCause: "limits-cfg",
						Count:           1,
					}).
					Obj(),
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"default/v-od": *utiltestingapi.MakeAdmission("limits-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "2").Obj()).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"limits-cq": {"default/incoming"},
			},
		},
	}

	runScheduleTestCases(t, scheduleTestConfig{
		resourceFlavors: resourceFlavors,
		fakeClock:       fakeClock,
	}, cases)
}
