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

	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestQuotaResourcesToReserveClampsAtZero(t *testing.T) {
	const gpu = corev1.ResourceName("example.com/gpu")
	fr := resources.FlavorResource{Flavor: "default", Resource: gpu}

	cases := map[string]struct {
		alreadyUsed int64
		want        int64
	}{
		"within the borrowing limit reserves what is left": {alreadyUsed: 12, want: 3},
		"over the borrowing limit reserves nothing":        {alreadyUsed: 20, want: 0},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache := schdcache.New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			cqObj := utiltestingapi.MakeClusterQueue("cq").
				Cohort("coh").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					ResourceQuotaWrapper(gpu).NominalQuota("10").BorrowingLimit("5").Append().
					Obj()).
				Obj()
			if err := cache.AddClusterQueue(ctx, cqObj); err != nil {
				t.Fatalf("AddClusterQueue: %v", err)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}
			cq := snapshot.ClusterQueue("cq")
			cq.AddUsage(workload.Usage{Quota: workload.ResourceUsage{Assigned: resources.FlavorResourceQuantities{fr: resources.NewAmount(tc.alreadyUsed)}}})

			e := &entry{assignment: flavorassigner.Assignment{
				Borrowing: 1,
				PodSets: []flavorassigner.PodSetAssignment{{
					Name:   kueue.DefaultPodSetName,
					Status: *flavorassigner.NewStatus("insufficient quota"),
					Flavors: flavorassigner.ResourceAssignment{
						gpu: &flavorassigner.FlavorAssignment{Name: "default", Mode: flavorassigner.Preempt},
					},
				}},
				Usage: workload.Usage{Quota: workload.ResourceUsage{
					Assigned: resources.FlavorResourceQuantities{fr: resources.NewAmount(4)},
				}},
			}}

			got := quotaResourcesToReserve(e, cq)
			if got[fr].Int64() != tc.want {
				t.Errorf("reserved %d, want %d", got[fr].Int64(), tc.want)
			}
		})
	}
}
