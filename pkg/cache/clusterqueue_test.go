package cache

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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
		request workload.FlavorResourceQuantities
		wantFit bool
	}{
		"full cohort, empty request": {
			c: &Cohort{
				Name: "C",
				RequestableResources: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: workload.FlavorResourceQuantities{
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
			request: workload.FlavorResourceQuantities{},
			wantFit: true,
		},
		"can fit": {
			c: &Cohort{
				Name: "C",
				RequestableResources: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: workload.FlavorResourceQuantities{
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
			request: workload.FlavorResourceQuantities{
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
				RequestableResources: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: workload.FlavorResourceQuantities{
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
			request: workload.FlavorResourceQuantities{
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
				RequestableResources: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
					"f2": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: workload.FlavorResourceQuantities{
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
			request: workload.FlavorResourceQuantities{
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
				RequestableResources: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
				Usage: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    5,
						corev1.ResourceMemory: 5,
					},
				},
			},
			request: workload.FlavorResourceQuantities{
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
				RequestableResources: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 5,
					},
				},
				Usage: workload.FlavorResourceQuantities{
					"f1": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 3,
					},
				},
			},
			request: workload.FlavorResourceQuantities{
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
