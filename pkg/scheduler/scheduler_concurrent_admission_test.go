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
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/util/routine"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestScheduleConcurrentAdmission(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	resourceFlavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("on-demand").Obj(),
		utiltestingapi.MakeResourceFlavor("spot").Obj(),
	}
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("concurrent-cq").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{{
					Key:      "dep",
					Operator: metav1.LabelSelectorOpIn,
					Values:   []string{"eng"},
				}},
			}).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "10").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "10").Obj(),
			).
			Obj(),
	}
	queues := []kueue.LocalQueue{
		*utiltestingapi.MakeLocalQueue("concurrent-queue", "eng-alpha").ClusterQueue("concurrent-cq").Obj(),
	}

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool

		workloads []kueue.Workload

		additionalClusterQueues []kueue.ClusterQueue
		additionalLocalQueues   []kueue.LocalQueue

		wantAssignments map[workload.Reference]kueue.Admission
		wantWorkloads   []kueue.Workload
		wantLeft        map[kueue.ClusterQueueReference][]workload.Reference
	}{
		"concurrent admission: more favorable variant evicts less favorable sibling": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-less-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-more-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("candidate-more-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            ". Pending the migration of 1 workload(s)",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
				*utiltestingapi.MakeWorkload("sibling-less-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "spot", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             "FlavorMigration",
						Message:            "Evicted to accommodate a workload (UID: candidate-uid) due to migration to more favorable resource flavor",
						LastTransitionTime: metav1.NewTime(now),
					}).
					SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "FlavorMigration", Count: 1}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"concurrent-cq": {"eng-alpha/candidate-more-favorable"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/sibling-less-favorable": *utiltestingapi.MakeAdmission("concurrent-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "10").
						Obj()).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.ConcurrentAdmission: true},
		},
		"concurrent admission: less favorable variant is skipped when more favorable sibling is admitted": {
			workloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-more-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-less-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Obj(),
			},
			wantWorkloads: []kueue.Workload{
				*utiltestingapi.MakeWorkload("sibling-more-favorable", "eng-alpha").
					UID("sibling-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("concurrent-cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "on-demand", "10").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("on-demand").
					Obj(),
				*utiltestingapi.MakeWorkload("candidate-less-favorable", "eng-alpha").
					UID("candidate-uid").
					Queue("concurrent-queue").
					Request(corev1.ResourceCPU, "10").
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent-workload", "parent-uid").
					AllowedFlavors("spot").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						Message:            "A more favorable variant is already admitted",
						LastTransitionTime: metav1.NewTime(now),
					}).
					ResourceRequests(kueue.PodSetRequest{
						Name: "main",
						Resources: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
					}).
					Obj(),
			},
			wantLeft: map[kueue.ClusterQueueReference][]workload.Reference{
				"concurrent-cq": {"eng-alpha/candidate-less-favorable"},
			},
			wantAssignments: map[workload.Reference]kueue.Admission{
				"eng-alpha/sibling-more-favorable": *utiltestingapi.MakeAdmission("concurrent-cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "on-demand", "10").
						Obj()).
					Obj(),
			},
			featureGates: map[featuregate.Feature]bool{features.ConcurrentAdmission: true},
		},
	}

	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)
				metrics.AdmissionCyclePreemptionSkips.Reset()
				features.SetFeatureGatesDuringTest(t, tc.featureGates)

				ctx, log := utiltesting.ContextWithLog(t)

				allQueues := append(queues, tc.additionalLocalQueues...)
				allClusterQueues := append(clusterQueues, tc.additionalClusterQueues...)

				clientBuilder := utiltesting.NewClientBuilder().
					WithLists(&kueue.WorkloadList{Items: tc.workloads}, &kueue.LocalQueueList{Items: allQueues}).
					WithObjects(
						utiltesting.MakeNamespaceWrapper("default").Obj(),
						utiltesting.MakeNamespaceWrapper("eng-alpha").Label("dep", "eng").Obj(),
					).
					WithStatusSubresource(&kueue.Workload{}).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge,
					})

				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}
				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)

				for _, q := range allQueues {
					if err := qManager.AddLocalQueue(ctx, &q); err != nil {
						t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
					}
				}
				for i := range resourceFlavors {
					cqCache.AddOrUpdateResourceFlavor(log, resourceFlavors[i])
				}
				for _, cq := range allClusterQueues {
					if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
					}
					if err := qManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
					if err := cl.Create(ctx, &cq); err != nil {
						t.Errorf("couldn't create the cluster queue: %v", err)
					}
				}

				scheduler := New(qManager, cqCache, cl, recorder,
					WithClock(t, fakeClock), WithPreemptionExpectations(preemptexpectations.New()))
				wg := sync.WaitGroup{}
				scheduler.setAdmissionRoutineWrapper(routine.NewWrapper(
					func() { wg.Add(1) },
					func() { wg.Done() },
				))

				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				go qManager.CleanUpOnContext(ctx)
				defer cancel()

				scheduler.schedule(ctx)
				wg.Wait()

				gotAssignments := make(map[workload.Reference]kueue.Admission)
				snapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				for cqName, c := range snapshot.ClusterQueues() {
					for name, w := range c.Workloads {
						switch {
						case !workload.HasQuotaReservation(w.Obj):
							t.Errorf("Workload %s is not admitted by a clusterQueue, but it is found as member of clusterQueue %s in the cache", name, cqName)
						case w.Obj.Status.Admission.ClusterQueue != cqName:
							t.Errorf("Workload %s is admitted by clusterQueue %s, but it is found as member of clusterQueue %s in the cache", name, w.Obj.Status.Admission.ClusterQueue, cqName)
						default:
							gotAssignments[name] = *w.Obj.Status.Admission
						}
					}
				}

				gotWorkloads := &kueue.WorkloadList{}
				err = cl.List(ctx, gotWorkloads)
				if err != nil {
					t.Fatalf("Unexpected list workloads error: %v", err)
				}

				defaultWorkloadCmpOpts := cmp.Options{
					cmpopts.EquateEmpty(),
					cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
					cmpopts.IgnoreFields(kueue.Workload{}, "ObjectMeta.ResourceVersion", "ObjectMeta.CreationTimestamp"),
					cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
					cmpopts.SortSlices(func(a, b kueue.Workload) bool { return a.Name < b.Name }),
				}

				if diff := cmp.Diff(tc.wantWorkloads, gotWorkloads.Items, defaultWorkloadCmpOpts); diff != "" {
					t.Errorf("Unexpected workloads (-want,+got):\n%s", diff)
				}

				if len(gotAssignments) == 0 {
					gotAssignments = nil
				}
				if diff := cmp.Diff(tc.wantAssignments, gotAssignments); diff != "" {
					t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
				}

				qDump := qManager.Dump()
				cmpDump := cmp.Options{
					cmpopts.SortSlices(func(a, b string) bool { return a < b }),
				}
				if diff := cmp.Diff(tc.wantLeft, qDump, cmpDump...); diff != "" {
					t.Errorf("Unexpected elements left in the queue (-want,+got):\n%s", diff)
				}
			})
		}
	}
}
