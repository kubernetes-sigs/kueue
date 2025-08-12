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

package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestCohortLendable(t *testing.T) {
	cache := New(utiltesting.NewFakeClient())

	cq1 := utiltesting.MakeClusterQueue("cq1").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").LendingLimit("8").Append().
				ResourceQuotaWrapper("example.com/gpu").NominalQuota("3").LendingLimit("3").Append().
				Obj(),
		).Cohort("test-cohort").
		ClusterQueue

	cq2 := utiltesting.MakeClusterQueue("cq2").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("default").
				ResourceQuotaWrapper("cpu").NominalQuota("2").LendingLimit("2").Append().
				Obj(),
		).Cohort("test-cohort").
		ClusterQueue

	if err := cache.AddClusterQueue(t.Context(), &cq1); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}
	if err := cache.AddClusterQueue(t.Context(), &cq2); err != nil {
		t.Fatal("Failed to add CQ to cache", err)
	}

	wantLendable := map[corev1.ResourceName]int64{
		corev1.ResourceCPU: 10_000,
		"example.com/gpu":  3,
	}

	lendable := calculateLendable(cache.hm.Cohort("test-cohort"))
	if diff := cmp.Diff(wantLendable, lendable); diff != "" {
		t.Errorf("Unexpected cohort lendable (-want,+got):\n%s", diff)
	}
}
