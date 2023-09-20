/*
Copyright 2023 The Kubernetes Authors.

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

package preemption

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type preemptionUpdateRecord struct {
	Workload string
	State    bool
}

const (
	testingNamespace  = "ns1"
	testingCQName     = "cq1"
	testingFlavorName = "rf1"
)

func TestOneRun(t *testing.T) {
	rf1 := utiltesting.MakeResourceFlavor(testingFlavorName).Obj()

	qCPU2b2 := utiltesting.MakeClusterQueue(testingCQName).
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas(testingFlavorName).Resource(corev1.ResourceCPU, "2", "2").Obj(),
		).
		Preemption(kueue.ClusterQueuePreemption{
			ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
		}).
		Cohort("c1").
		Obj()

	resourceFlavors := []*kueue.ResourceFlavor{rf1}
	clusterQueues := []*kueue.ClusterQueue{qCPU2b2}

	otherWl := utiltesting.MakeWorkload("wl2", testingNamespace).
		Request(corev1.ResourceCPU, "2").
		Priority(2).
		ReserveQuota(
			utiltesting.MakeAdmission(testingCQName).
				PodSets(
					kueue.PodSetAssignment{
						Name: "main",
						Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
							corev1.ResourceCPU: testingFlavorName,
						},
						ResourceUsage: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Count: ptr.To[int32](1),
					},
				).
				Obj(),
		).
		SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStateReady).
		Obj()

	cases := map[string]struct {
		workloads       []*kueue.Workload
		admissionChecks []*kueue.AdmissionCheck

		wantEvents           []preemptionUpdateRecord
		wantEvictedWorkloads []string
	}{
		"nothing to do": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStateReady).
					Obj(),
			},
		},
		"finish the preemption": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					Obj(),
			},
			wantEvents: []preemptionUpdateRecord{
				{Workload: "ns1/wl1", State: true},
			},
		},
		"the preemption is no longer possible": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					Priority(1).
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					Obj(),
				otherWl.DeepCopy(),
			},
			wantEvents: []preemptionUpdateRecord{
				{Workload: "ns1/wl1", State: false},
			},
		},
		"the eviction is triggered": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					Priority(3).
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					Obj(),
				otherWl.DeepCopy(),
			},
			wantEvictedWorkloads: []string{"ns1/wl2"},
		},
		"AfterCheckPassedOrOnDemand Pending": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					Priority(3).
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", kueue.CheckStatePending).
					Obj(),
				otherWl.DeepCopy(),
			},
			admissionChecks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").Policy(kueue.AfterCheckPassedOrOnDemand).Obj(),
			},
		},
		"AfterCheckPassedOrOnDemand CheckStatePreemptionRequired": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					Priority(3).
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", kueue.CheckStatePreemptionRequired).
					Obj(),
				otherWl.DeepCopy(),
			},
			admissionChecks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").Policy(kueue.AfterCheckPassedOrOnDemand).Obj(),
			},
			wantEvictedWorkloads: []string{"ns1/wl2"},
		},
		"AfterCheckPassedOrOnDemand Ready": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					Priority(3).
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", kueue.CheckStateReady).
					Obj(),
				otherWl.DeepCopy(),
			},
			admissionChecks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").Policy(kueue.AfterCheckPassedOrOnDemand).Obj(),
			},
			wantEvictedWorkloads: []string{"ns1/wl2"},
		},
		"Anytime Pending": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", testingNamespace).
					Request(corev1.ResourceCPU, "1").
					Priority(3).
					ReserveQuota(
						utiltesting.MakeAdmission(testingCQName).
							PodSets(
								kueue.PodSetAssignment{
									Name: "main",
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: testingFlavorName,
									},
									ResourceUsage: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
									Count: ptr.To[int32](1),
								},
							).
							Obj(),
					).
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", kueue.CheckStatePending).
					Obj(),
				otherWl.DeepCopy(),
			},
			admissionChecks: []*kueue.AdmissionCheck{
				utiltesting.MakeAdmissionCheck("check1").Policy(kueue.Anytime).Obj(),
			},
			wantEvictedWorkloads: []string{"ns1/wl2"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			fakeClient := utiltesting.NewFakeClient()
			cache := cache.New(fakeClient)
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(rf.DeepCopy())
			}

			for _, ac := range tc.admissionChecks {
				cache.AddOrUpdateAdmissionCheck(ac)
			}

			for _, cq := range clusterQueues {
				if err := cache.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatal(err)
				}
			}

			for _, wl := range tc.workloads {
				cache.AddOrUpdateWorkload(wl)
			}

			// create the controller
			controller := NewController(cache)

			// setup any additional parts
			controller.preemptor = preemption.New(fakeClient, record.NewFakeRecorder(10))
			var updates []preemptionUpdateRecord
			controller.updateCheckStatus = func(_ context.Context, _ client.Client, wl *kueue.Workload, successful bool) error {
				updates = append(updates, preemptionUpdateRecord{Workload: client.ObjectKeyFromObject(wl).String(), State: successful})
				return nil
			}

			var evictedWorkloads []string
			controller.preemptor.OverrideApply(func(_ context.Context, wl *kueue.Workload) error {
				evictedWorkloads = append(evictedWorkloads, client.ObjectKeyFromObject(wl).String())
				return nil
			})

			controller.run(ctx)

			if diff := cmp.Diff(tc.wantEvents, updates); diff != "" {
				t.Errorf("Unexpected events (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvictedWorkloads, evictedWorkloads); diff != "" {
				t.Errorf("Unexpected evicted workloads (-want/+got):\n%s", diff)
			}
		})
	}
}
