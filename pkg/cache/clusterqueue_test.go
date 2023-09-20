package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestClusterQueueUpdateWithFlavors(t *testing.T) {
	rf := utiltesting.MakeResourceFlavor("x86").Obj()
	cq := utiltesting.MakeClusterQueue("cq").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()

	testcases := []struct {
		name       string
		curStatus  metrics.ClusterQueueStatus
		flavors    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		wantStatus metrics.ClusterQueueStatus
	}{
		{
			name:      "Pending clusterQueue updated existent flavors",
			curStatus: pending,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: active,
		},
		{
			name:       "Active clusterQueue updated with not found flavors",
			curStatus:  active,
			flavors:    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{},
			wantStatus: pending,
		},
		{
			name:      "Terminating clusterQueue updated with existent flavors",
			curStatus: terminating,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: terminating,
		},
		{
			name:       "Terminating clusterQueue updated with not found flavors",
			curStatus:  terminating,
			wantStatus: terminating,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.curStatus
			cq.UpdateWithFlavors(tc.flavors)

			if cq.Status != tc.wantStatus {
				t.Fatalf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
			}
		})
	}
}

func TestCohortCanFit(t *testing.T) {
	cases := map[string]struct {
		c       *Cohort
		request FlavorResourceQuantities
		wantFit bool
	}{
		"full cohort, empty request": {
			c: &Cohort{
				Name: "C",
				RequestableResources: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
			},
			request: FlavorResourceQuantities{},
			wantFit: true,
		},
		"can fit": {
			c: &Cohort{
				Name: "C",
				RequestableResources: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    4,
						corev1.ResourceMemory: 4,
					},
				},
			},
			request: FlavorResourceQuantities{
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: true,
		},
		"full cohort, none fit": {
			c: &Cohort{
				Name: "C",
				RequestableResources: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
			},
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
		},
		"one cannot fit": {
			c: &Cohort{
				Name: "C",
				RequestableResources: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    4,
						corev1.ResourceMemory: 4,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    4,
						corev1.ResourceMemory: 4,
					},
				},
			},
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    2,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
		},
		"missing flavor": {
			c: &Cohort{
				Name: "C",
				RequestableResources: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
			},
			request: FlavorResourceQuantities{
				"f2": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
		},
		"missing resource": {
			c: &Cohort{
				Name: "C",
				RequestableResources: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 5,
					},
				},
				Usage: FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 3,
					},
				},
			},
			request: FlavorResourceQuantities{
				"f1": map[corev1.ResourceName]int64{
					corev1.ResourceCPU:    1,
					corev1.ResourceMemory: 1,
				},
			},
			wantFit: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.c.CanFit(tc.request)
			if got != tc.wantFit {
				t.Errorf("Unexpected result, %v", got)
			}

		})
	}

}

func TestGetPreemptingWorklods(t *testing.T) {
	cases := map[string]struct {
		workloads        []*kueue.Workload
		checks           map[string]AdmissionCheck
		wantPreemptNow   []string
		wantPreemptLater []string
	}{
		"empty": {},
		"one workload no preemption": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", "ns1").Obj(),
			},
		},
		"one workload preempt check pending, preempt anytime": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
			},
			checks: map[string]AdmissionCheck{
				"check1": {
					PreemptionPolicy: kueue.Anytime,
				},
			},
			wantPreemptNow: []string{"ns1/wl1"},
		},
		"one workload preempt check pending, preempt on demand": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
			},
			checks: map[string]AdmissionCheck{
				"check1": {
					PreemptionPolicy: kueue.AfterCheckPassedOrOnDemand,
				},
			},
			wantPreemptLater: []string{"ns1/wl1"},
		},
		"one workload check request, preempt on demand": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", metav1.ConditionUnknown, kueue.CheckStatePreemptionRequired).
					Obj(),
			},
			checks: map[string]AdmissionCheck{
				"check1": {
					PreemptionPolicy: kueue.AfterCheckPassedOrOnDemand,
				},
			},
			wantPreemptNow: []string{"ns1/wl1"},
		},
		"one workload check ready, preempt on demand": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("check1", metav1.ConditionTrue, kueue.CheckStateReady).
					Obj(),
			},
			checks: map[string]AdmissionCheck{
				"check1": {
					PreemptionPolicy: kueue.AfterCheckPassedOrOnDemand,
				},
			},
			wantPreemptNow: []string{"ns1/wl1"},
		},
		"multiple workloads": {
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("wl1", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkOnDemand", metav1.ConditionTrue, kueue.CheckStateReady).
					Obj(),
				utiltesting.MakeWorkload("wl2", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkOnDemand", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
				utiltesting.MakeWorkload("wl3", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkAnytime", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
				utiltesting.MakeWorkload("wl4", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkOnDemand", metav1.ConditionUnknown, kueue.CheckStatePreemptionRequired).
					Obj(),
				utiltesting.MakeWorkload("wl5", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkOnDemand", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
				utiltesting.MakeWorkload("wl6", "ns1").
					SetOrReplaceAdmissionCheck("checkAnytime", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
				utiltesting.MakeWorkload("wl7", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkAnytime", metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkOnDemand", metav1.ConditionUnknown, kueue.CheckStatePending).
					Obj(),
				utiltesting.MakeWorkload("wl8", "ns1").
					SetOrReplaceAdmissionCheck(constants.PreemptionAdmissionCheckName, metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkAnytime", metav1.ConditionUnknown, kueue.CheckStatePending).
					SetOrReplaceAdmissionCheck("checkOnDemand", metav1.ConditionUnknown, kueue.CheckStatePreemptionRequired).
					Obj(),
			},
			checks: map[string]AdmissionCheck{
				"checkOnDemand": {
					PreemptionPolicy: kueue.AfterCheckPassedOrOnDemand,
				},
				"checkAnytime": {
					PreemptionPolicy: kueue.Anytime,
				},
			},
			wantPreemptNow:   []string{"ns1/wl1", "ns1/wl3", "ns1/wl4", "ns1/wl8"},
			wantPreemptLater: []string{"ns1/wl2", "ns1/wl5", "ns1/wl7"},
		},
	}

	sortOpt := cmpopts.SortSlices(func(a, b *workload.Info) bool { return workload.Key(a.Obj) < workload.Key(b.Obj) })
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wlMap := make(map[string]*workload.Info, len(tc.workloads))
			for _, wl := range tc.workloads {
				wlMap[workload.Key(wl)] = workload.NewInfo(wl)
			}

			cq := ClusterQueue{
				Workloads: wlMap,
			}

			gotPrermptNow, gotPreemptLater := cq.GetPreemptingWorkloads(tc.checks)

			wantPreemptNowWorkloads := make([]*workload.Info, len(tc.wantPreemptNow))
			for i, wlKey := range tc.wantPreemptNow {
				wantPreemptNowWorkloads[i] = wlMap[wlKey]
			}
			if diff := cmp.Diff(wantPreemptNowWorkloads, gotPrermptNow, sortOpt); diff != "" {
				t.Errorf("Unexpected preempt now (-want/+got):\n%s", diff)
			}

			wantPreemptLaterWorkloads := make([]*workload.Info, len(tc.wantPreemptLater))
			for i, wlKey := range tc.wantPreemptLater {
				wantPreemptLaterWorkloads[i] = wlMap[wlKey]
			}
			if diff := cmp.Diff(wantPreemptLaterWorkloads, gotPreemptLater, sortOpt); diff != "" {
				t.Errorf("Unexpected preempt later (-want/+got):\n%s", diff)
			}
		})
	}
}
