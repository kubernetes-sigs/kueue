package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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

func TestClusterQueueUpdate(t *testing.T) {
	resourceFlavors := []*kueue.ResourceFlavor{
		{ObjectMeta: metav1.ObjectMeta{Name: "on-demand"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "spot"}},
	}
	clusterQueue :=
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "50", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	newClusterQueue :=
		*utiltesting.MakeClusterQueue("eng-alpha").
			QueueingStrategy(kueue.StrictFIFO).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			}).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").
					Resource(corev1.ResourceCPU, "100", "50").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").
					Resource(corev1.ResourceCPU, "100", "0").Obj(),
			).Obj()
	cases := []struct {
		name                         string
		cq                           *kueue.ClusterQueue
		newcq                        *kueue.ClusterQueue
		wantLastAssignmentGeneration int64
	}{
		{
			name:                         "RGs not change",
			cq:                           &clusterQueue,
			newcq:                        clusterQueue.DeepCopy(),
			wantLastAssignmentGeneration: 1,
		},
		{
			name:                         "RGs changed",
			cq:                           &clusterQueue,
			newcq:                        &newClusterQueue,
			wantLastAssignmentGeneration: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
					tc.cq,
				)
			cl := clientBuilder.Build()
			cqCache := New(cl)
			// Workloads are loaded into queues or clusterQueues as we add them.
			for _, rf := range resourceFlavors {
				cqCache.AddOrUpdateResourceFlavor(rf)
			}
			if err := cqCache.AddClusterQueue(ctx, tc.cq); err != nil {
				t.Fatalf("Inserting clusterQueue %s in cache: %v", tc.cq.Name, err)
			}
			if err := cqCache.UpdateClusterQueue(tc.newcq); err != nil {
				t.Fatalf("Updating clusterQueue %s in cache: %v", tc.newcq.Name, err)
			}
			snapshot := cqCache.Snapshot()
			if diff := cmp.Diff(
				tc.wantLastAssignmentGeneration,
				snapshot.ClusterQueues["eng-alpha"].AllocatableResourceGeneration); diff != "" {
				t.Errorf("Unexpected assigned clusterQueues in cache (-want,+got):\n%s", diff)
			}
		})
	}
}
