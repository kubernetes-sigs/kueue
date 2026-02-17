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
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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
			FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			Obj(),
		*utiltestingapi.MakeLocalQueue("lq-b", "default").
			FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			Obj(),
		*utiltestingapi.MakeLocalQueue("lq-c", "default").
			FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
			ClusterQueue("cq1").
			Obj(),
	}

	cases := map[string]struct {
		enableFairSharing bool
		initialUsage      map[string]corev1.ResourceList
		workloads         []kueue.Workload
		wantWorkloads     []kueue.Workload
	}{
		"admits workload from less active localqueue": {
			enableFairSharing: true,
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
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
		"without AFS: classic admission decision ignores queue usage": {
			enableFairSharing: false,
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
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
			enableFairSharing: true,
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
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 4 more needed",
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
				*utiltestingapi.MakeWorkload("wl-b2", "default").
					Queue("lq-b").
					PodSets(*utiltestingapi.MakePodSet("one", 1).
						Request(corev1.ResourceCPU, "4").
						Obj()).
					Creation(now.Add(3 * time.Second)).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 4 more needed",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "one",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("4"),
						},
					}).
					Obj(),
			},
		},
		"schedules normally when queues have equal usage": {
			enableFairSharing: true,
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
			enableFairSharing: true,
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
						Reason:             "Pending",
						Message:            "couldn't assign flavors to pod set one: insufficient unused quota for cpu in flavor default, 8 more needed",
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
	}

	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				features.SetFeatureGateDuringTest(t, features.AdmissionFairSharing, tc.enableFairSharing)

				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(
						&kueue.WorkloadList{Items: tc.workloads},
						&kueue.ClusterQueueList{Items: clusterQueues},
						&kueue.LocalQueueList{Items: queues}).
					WithObjects(
						utiltesting.MakeNamespace("default"),
					).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
				cl := clientBuilder.Build()

				cqCache := schdcache.New(cl, schdcache.WithFairSharing(tc.enableFairSharing), schdcache.WithAdmissionFairSharing(afsConfig))
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithAdmissionFairSharing(afsConfig))

				ctx, log := utiltesting.ContextWithLog(t)
				for _, q := range queues {
					if err := qManager.AddLocalQueue(ctx, &q); err != nil {
						t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
					}
				}
				for lqName, resources := range tc.initialUsage {
					lqKey := utilqueue.LocalQueueReference(fmt.Sprintf("default/%s", lqName))
					qManager.AfsConsumedResources.Set(lqKey, resources, fakeClock.Now())
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
				if tc.enableFairSharing {
					preemptionFairSharing = &config.FairSharing{}
				}
				scheduler := New(qManager, cqCache, cl, recorder,
					WithFairSharing(preemptionFairSharing),
					WithAdmissionFairSharing(afsConfig),
					WithClock(t, fakeClock))
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

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
					t.Errorf("Unexpected workloads (-want,+got):\n%s", diff)
				}
			})
		}
	}
}
