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
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleForAFS(t *testing.T) {
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
	}
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)
	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("cq1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "8").
				Resource(corev1.ResourceMemory, "8Gi").Obj()).
			AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("lq-a", "default").
			FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			Obj(),
		*utiltestingapi.MakeLocalQueue("lq-b", "default").
			FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			Obj(),
		*utiltestingapi.MakeLocalQueue("lq-c", "default").
			FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			Obj(),
		*utiltestingapi.MakeLocalQueue("lq-zero", "default").
			FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("0"))}).
			ClusterQueue("cq1").
			Obj(),
	}

	snapshotErr := errors.New("snapshot failed")

	cases := map[string]struct {
		featureGates  map[featuregate.Feature]bool
		initialUsage  map[string]corev1.ResourceList
		workloads     []kueue.Workload
		wantWorkloads []kueue.Workload
		deleteQueue   string
		wantLeft      map[kueue.ClusterQueueReference][]workload.Reference
		// wantErr fails every LocalQueue lookup made to resolve a fair-sharing
		// weight, so the cycles of the case run with a failing snapshot.
		wantErr error
	}{
		"admits workload from less active localqueue": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("8")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("2")},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("8"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "8").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		// A LocalQueue with weight 0 must be the most disadvantaged, so its
		// workload loses admission to a normal-weight queue even though it was
		// submitted first. Both queues start idle on purpose: only 0 usage over
		// 0 weight yields NaN, which sorts ahead of every real value; any
		// non-zero usage already yields +Inf and sorts last.
		"does not prioritize a zero-weight localqueue": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a":    {corev1.ResourceCPU: resource.MustParse("0")},
				"lq-zero": {corev1.ResourceCPU: resource.MustParse("0")},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-z1", "default").
					Queue("lq-zero").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "8").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-z1", "default").
					Queue("lq-zero").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("8"),
						},
					}).
					Obj(),
			},
		},
		// This test shows the expected behavior - deleting another LQ
		// does not impact scheduling to the existing LQ.
		"admits workload when another LQ is deleted": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("0")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("0")},
			},
			deleteQueue: "lq-b",
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		// NOTE: The aim of this test is to document the current implementation.
		// Rejecting the admission when the LocalQueue is deleted might be desired
		// in the long term, but it should be handled independently of AFS.
		"admits workload even if its localqueue was deleted": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("0")},
			},
			deleteQueue: "lq-a",
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "8").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		"without AFS: classic admission decision ignores queue usage": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: false},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("8")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("2")},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "8").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("8"),
						},
					}).
					Obj(),
			},
		},
		"admits one workload from each localqueue when quota is limited": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("4")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("4")},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a2", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(2 * time.Second)).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b2", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(3 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a2", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 4 more needed",
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
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(2 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				// wl-b2 has no Pending condition because SchedulingEquivalenceHashing
				// bulk-moves it to inadmissible before individual evaluation.
				*utiltestingapi.MakeWorkload("wl-b2", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(3 * time.Second)).
					Obj(),
			},
		},
		"schedules normally when queues have equal usage": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("2")},
				"lq-b": {corev1.ResourceCPU: resource.MustParse("2")},
				"lq-c": {corev1.ResourceCPU: resource.MustParse("2")},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-c1", "default").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "4").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "3").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "3").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-c1", "default").
					Queue("lq-c").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "1").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		"admits workload from lq-b with uninitialized cache": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			initialUsage: map[string]corev1.ResourceList{
				"lq-a": {corev1.ResourceCPU: resource.MustParse("8")},
			},
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("8"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-b1", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "8").
						Obj()).
					Creation(now.Add(1 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionTrue,
						Reason:             "QuotaReserved",
						Message:            "Quota reserved in ClusterQueue cq1",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						Message:            "The workload is admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					Admission(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(
								utiltestingapi.MakePodSetAssignment("one").
									Assignment(corev1.ResourceCPU, "default", "8").
									Count(1).
									Obj(),
							).
							Obj(),
					).
					Obj(),
			},
		},
		// Nothing but the scheduler puts a popped head back, so wl-a2 is only
		// still queued at the end if every failed cycle requeued it.
		"snapshot fails; the popped head is requeued": {
			featureGates: map[featuregate.Feature]bool{features.AdmissionFairSharing: true},
			wantErr:      snapshotErr,
			workloads: []kueue.Workload{
				// Admitted so that the snapshot resolves a LocalQueue
				// weight, and a second entry so the harness runs two cycles.
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "4").
								Count(1).
								Obj()).
							Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a2", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl-a1", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("cq1").
							PodSets(utiltestingapi.MakePodSetAssignment("one").
								Assignment(corev1.ResourceCPU, "default", "4").
								Count(1).
								Obj()).
							Obj(), now).
					Obj(),
				*utiltestingapi.MakeWorkload("wl-a2", "default").
					Queue("lq-a").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"cq1": {"default/wl-a2"},
			},
		},
	}

	scenarios := []map[featuregate.Feature]bool{
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: true,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: true,
		},
	}

	for name, tc := range cases {
		for _, scenario := range scenarios {
			t.Run(
				fmt.Sprintf("%s WorkloadRequestUseMergePatch:%t observability:%t", name, scenario[features.WorkloadRequestUseMergePatch], scenario[features.UnadmittedWorkloadsObservability]),
				func(t *testing.T) {
					features.SetFeatureGatesDuringTest(t, scenario)
					features.SetFeatureGatesDuringTest(t, tc.featureGates)

					wantWorkloads := make([]kueue.Workload, len(tc.wantWorkloads))
					for i := range tc.wantWorkloads {
						wantWorkloads[i] = *tc.wantWorkloads[i].DeepCopy()
					}
					if !scenario[features.UnadmittedWorkloadsObservability] {
						utiltesting.AdjustWorkloadsForDisabledObservabilityInScheduler(wantWorkloads)
					}

					clientBuilder := utiltesting.NewClientBuilder().
						WithLists(
							&kueue.WorkloadList{Items: tc.workloads},
							&kueue.ClusterQueueList{Items: clusterQueues},
							&kueue.LocalQueueList{Items: queues}).
						WithObjects(
							utiltesting.MakeNamespace("default"),
						).
						WithStatusSubresource(&kueue.Workload{}).
						WithInterceptorFuncs(interceptor.Funcs{
							SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge,
							Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
								if _, isLocalQueue := obj.(*kueue.LocalQueue); isLocalQueue && errors.Is(tc.wantErr, snapshotErr) {
									return tc.wantErr
								}
								return c.Get(ctx, key, obj, opts...)
							},
						})
					cl := clientBuilder.Build()

					cqCache := schdcache.New(cl, schdcache.WithFairSharing(tc.featureGates[features.AdmissionFairSharing]), schdcache.WithAdmissionFairSharing(afsConfig))
					qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithAdmissionFairSharing(afsConfig))

					ctx, log := utiltesting.ContextWithLog(t)
					for _, q := range queues {
						if err := qManager.AddLocalQueue(ctx, &q); err != nil {
							t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
						}
					}
					for lqName, resources := range tc.initialUsage {
						lqKey := utilqueue.LocalQueueReference(fmt.Sprintf("default/%s", lqName))
						qManager.AfsUsageLedger.SetForTest(lqKey, resources, fakeClock.Now())
					}
					for _, rf := range resourceFlavors {
						cqCache.AddOrUpdateResourceFlavor(log, rf)
					}
					for _, cq := range clusterQueues {
						if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
							t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
						}
						if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
							t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
						}
					}
					recorder := &utiltesting.EventRecorder{}
					var preemptionFairSharing *config.FairSharing
					if tc.featureGates[features.AdmissionFairSharing] {
						preemptionFairSharing = &config.FairSharing{}
					}
					scheduler := New(qManager, cqCache, cl, recorder,
						WithFairSharing(preemptionFairSharing),
						WithAdmissionFairSharing(afsConfig),
						WithClock(t, fakeClock),
						WithPreemptionExpectations(preemptexpectations.New()))
					wg := sync.WaitGroup{}
					scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
						func() { wg.Add(1) },
						func() { wg.Done() },
					))

					ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
					go qManager.CleanUpOnContext(ctx)
					defer cancel()

					for range len(tc.workloads) {
						scheduler.schedule(ctx)
						wg.Wait()
					}

					gotWorkloads := &kueue.WorkloadList{}
					err := cl.List(ctx, gotWorkloads)
					if err != nil {
						t.Fatalf("Unexpected list workloads error: %v", err)
					}

					defaultWorkloadCmpOpts := cmp.Options{
						cmpopts.EquateEmpty(),
						cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
						cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.ResourceVersion"),
					}

					if diff := cmp.Diff(wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
						t.Errorf("Unexpected workloads (-want,+got):\n%s", diff)
					}

					if diff := cmp.Diff(tc.wantLeft, qManager.Dump(), cmpDump...); diff != "" {
						t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
					}
				},
			)
		}
	}
}

func TestShouldApplyEntryPenalty(t *testing.T) {
	// shouldApplyEntryPenalty reads the AdmissionFairSharing gate through
	// afs.Enabled; pin it so the cases below turn on the config and the
	// ClusterQueue mode rather than on the process-global default.
	features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
	now := time.Now().Truncate(time.Second)
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
	}

	pendingWl := utiltestingapi.MakeWorkload("wl", "ns").
		Queue("lq").
		Request(corev1.ResourceCPU, "4").
		Obj()
	reservedWl := utiltestingapi.MakeWorkload("wl", "ns").
		Queue("lq").
		Request(corev1.ResourceCPU, "4").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
		Obj()

	cases := map[string]struct {
		afsConfig     *config.AdmissionFairSharing
		admissionMode kueue.AdmissionMode
		wl            *kueue.Workload
		want          bool
	}{
		"pushes for a first-pass workload on a usage-based ClusterQueue": {
			afsConfig:     afsConfig,
			admissionMode: kueue.UsageBasedAdmissionFairSharing,
			wl:            pendingWl,
			want:          true,
		},
		"skips when no AdmissionFairSharing config is set": {
			admissionMode: kueue.UsageBasedAdmissionFairSharing,
			wl:            pendingWl,
			want:          false,
		},
		"skips for a ClusterQueue without usage-based admission mode": {
			afsConfig: afsConfig,
			wl:        pendingWl,
			want:      false,
		},
		"skips a second-pass workload that already holds a quota reservation": {
			afsConfig:     afsConfig,
			admissionMode: kueue.UsageBasedAdmissionFairSharing,
			wl:            reservedWl,
			want:          false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			s := &Scheduler{admissionFairSharing: tc.afsConfig}
			e := &entry{
				Head: qcache.Head{Info: *workload.NewInfo(log, tc.wl)},
				clusterQueueSnapshot: &schdcache.ClusterQueueSnapshot{
					AdmissionScope: kueue.AdmissionScope{AdmissionMode: tc.admissionMode},
				},
			}

			if got := s.shouldApplyEntryPenalty(e); got != tc.want {
				t.Errorf("shouldApplyEntryPenalty() = %t, want %t", got, tc.want)
			}
		})
	}
}

// TestRequeueHeadsAfterSnapshotError covers the heads popped by a cycle whose
// snapshot failed: nothing else puts them back, so they are lost unless the
// scheduler requeues them. Regular heads return to the ClusterQueue right away,
// second-pass heads after a backoff step.
func TestRequeueHeadsAfterSnapshotError(t *testing.T) {
	// The LocalQueue weight lookup is the only client call a snapshot makes, so
	// admission fair sharing is what a test has to enable to fail one.
	features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, true)
	afsConfig := &config.AdmissionFairSharing{
		UsageHalfLifeTime:     metav1.Duration{Duration: 10 * time.Second},
		UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
	}
	now := time.Now().Truncate(time.Second)
	ctx, log := utiltesting.ContextWithLog(t)

	ns := utiltesting.MakeNamespaceWrapper(metav1.NamespaceDefault).Obj()
	rf := utiltestingapi.MakeResourceFlavor("rf").Obj()
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(rf.Name).
				Resource(corev1.ResourceCPU, "1").
				Obj(),
		).
		AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
		Obj()
	lq := utiltestingapi.MakeLocalQueue("lq", metav1.NamespaceDefault).ClusterQueue(cq.Name).Obj()
	pending := utiltestingapi.MakeWorkload("pending", metav1.NamespaceDefault).
		Queue(kueue.LocalQueueName(lq.Name)).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		Creation(now).
		Obj()
	// A workload whose topology assignment is still delayed is what the queue
	// manager hands over as a second-pass head.
	secondPass := utiltestingapi.MakeWorkload("second-pass", metav1.NamespaceDefault).
		Queue(kueue.LocalQueueName(lq.Name)).
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceCPU, "1").
			Obj()).
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission(kueue.ClusterQueueReference(cq.Name)).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, kueue.ResourceFlavorReference(rf.Name), "1").
					DelayedTopologyRequest(kueue.DelayedTopologyRequestStatePending).
					Obj()).
				Obj(), now).
		AdmissionCheck(kueue.AdmissionCheckState{Name: "check", State: kueue.CheckStateReady}).
		Obj()

	var snapshotToFail atomic.Bool
	cl := utiltesting.NewClientBuilder().
		WithObjects(ns, rf, cq, lq, pending, secondPass).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if _, isLocalQueue := obj.(*kueue.LocalQueue); isLocalQueue && snapshotToFail.CompareAndSwap(true, false) {
					return errors.New("injected LocalQueue get failure")
				}
				return c.Get(ctx, key, obj, opts...)
			},
		}).
		Build()

	fakeClock := testingclock.NewFakeClock(now)
	cqCache := schdcache.New(cl, schdcache.WithFairSharing(true), schdcache.WithAdmissionFairSharing(afsConfig))
	qManager := qcache.NewManagerForUnitTests(cl, cqCache,
		qcache.WithClock(fakeClock), qcache.WithAdmissionFairSharing(afsConfig))

	cqCache.AddOrUpdateResourceFlavor(log, rf)
	if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
	}
	if err := qManager.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
	}
	if err := qManager.AddLocalQueue(ctx, lq); err != nil {
		t.Fatalf("Inserting queue %s/%s in manager: %v", lq.Namespace, lq.Name, err)
	}

	scheduler := New(qManager, cqCache, cl, &utiltesting.EventRecorder{},
		WithClock(t, fakeClock), WithPreemptionExpectations(preemptexpectations.New()))

	ctx, cancel := context.WithTimeout(ctx, queueingTimeout)
	// Heads blocks on a condition variable, which CleanUpOnContext wakes up so
	// the assertions below fail on a timeout instead of hanging.
	go qManager.CleanUpOnContext(ctx)
	defer cancel()

	if !qManager.QueueSecondPassIfNeeded(ctx, secondPass, 0) {
		t.Fatalf("Failed queueing %q for the second pass", secondPass.Name)
	}
	fakeClock.Step(time.Second)
	if fakeClock.HasWaiters() {
		t.Fatalf("The second pass pre-queue left a timer behind")
	}

	// Armed here rather than at build time so that only a scheduling cycle can
	// consume it.
	snapshotToFail.Store(true)
	scheduler.schedule(ctx)
	if snapshotToFail.Load() {
		t.Fatal("No snapshot read a LocalQueue, so none of them failed")
	}

	// One iteration in, the second pass backoff is two seconds.
	fakeClock.Step(2*time.Second - time.Nanosecond)
	if !fakeClock.HasWaiters() {
		t.Fatalf("The second pass head didn't come back with a two second backoff")
	}
	fakeClock.Step(time.Nanosecond)
	gotHeads := qManager.Heads(ctx)
	gotHeadKeys := slices.Map(gotHeads, func(h *qcache.Head) workload.Reference { return workload.Key(h.Obj) })
	wantHeadKeys := []workload.Reference{workload.Key(pending), workload.Key(secondPass)}
	sortRefs := cmpopts.SortSlices(func(a, b workload.Reference) bool { return a < b })
	if diff := cmp.Diff(wantHeadKeys, gotHeadKeys, sortRefs); diff != "" {
		t.Fatalf("Unexpected heads after the second pass backoff (-want,+got):\n%s", diff)
	}
	for _, head := range gotHeads {
		if workload.Key(head.Obj) == workload.Key(secondPass) && head.SecondPassIteration != 2 {
			t.Errorf("Unexpected second pass iteration: want 2, got %d", head.SecondPassIteration)
		}
	}
}
