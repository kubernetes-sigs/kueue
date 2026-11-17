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
	"math"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestClusterQueueResourceMetricsReportUnlimitedAsInf(t *testing.T) {
	defer kueuemetrics.InitMetricVectors(nil)

	formatter := resources.NewResourceFormatter()
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}
	unlimited := resources.NewAmount(math.MaxInt64)
	cq := &clusterQueue{
		Name:              "unlimited-cq",
		AdmittedUsage:     resources.FlavorResourceQuantities{fr: unlimited},
		resourceFormatter: formatter,
		customLabels:      kueuemetrics.NewCustomLabels(nil),
		resourceNode:      NewResourceNode(),
	}
	cq.resourceNode.Quotas[fr] = ResourceQuota{
		Nominal:        unlimited,
		BorrowingLimit: &unlimited,
		LendingLimit:   &unlimited,
	}
	cq.resourceNode.Usage[fr] = unlimited

	cq.reportResourceMetrics(false)

	labels := map[string]string{
		"cohort":        "",
		"cluster_queue": string(cq.Name),
		"flavor":        string(fr.Flavor),
		"resource":      string(fr.Resource),
		"replica_role":  "standalone",
	}
	expectGaugeValue(t, kueuemetrics.ClusterQueueResourceNominalQuota, labels, math.Inf(1))
	expectGaugeValue(t, kueuemetrics.ClusterQueueResourceBorrowingLimit, labels, math.Inf(1))
	expectGaugeValue(t, kueuemetrics.ClusterQueueResourceLendingLimit, labels, math.Inf(1))
	expectGaugeValue(t, kueuemetrics.ClusterQueueResourceReservations, labels, math.Inf(1))
	expectGaugeValue(t, kueuemetrics.ClusterQueueResourceUsage, labels, math.Inf(1))
}

func TestClusterQueueResourceMetricsReportZeroWeightBorrowingAsNaN(t *testing.T) {
	defer kueuemetrics.InitMetricVectors(nil)

	ctx, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient(), WithFairSharing(true))

	setupRecordMetricsHierarchy(ctx, t, log, cache,
		[]*kueue.ResourceFlavor{
			utiltestingapi.MakeResourceFlavor("default").Obj(),
		},
		[]*kueue.Cohort{
			utiltestingapi.MakeCohort("root").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "4").Obj()).
				Obj(),
		},
		[]*kueue.ClusterQueue{
			utiltestingapi.MakeClusterQueue("cq1").
				Cohort("root").
				FairWeight(resource.MustParse("0")).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "0").Obj()).
				Obj(),
		},
	)

	now := time.Now()
	wl := utiltestingapi.MakeWorkload("wl1", "ns").
		Request(corev1.ResourceCPU, "2").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq1").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceCPU, "default", "2").
				Obj()).
			Obj(), now).
		AdmittedAt(true, now).
		Obj()
	if !cache.AddOrUpdateWorkload(ctx, log, wl) {
		t.Fatal("expected workload to be added to cache")
	}

	cache.RecordClusterQueueResourceMetrics(log, "cq1")

	weightedShareLabels := map[string]string{
		"cluster_queue": "cq1",
		"cohort":        "root",
		"replica_role":  "standalone",
	}
	dps := utiltestingmetrics.CollectFilteredGaugeVec(kueuemetrics.ClusterQueueWeightedShare, weightedShareLabels)
	if len(dps) != 1 {
		t.Fatalf("expected one weighted-share metric for cq1, got=%d", len(dps))
	}
	if !math.IsNaN(dps[0].Value) {
		t.Fatalf("expected NaN weighted-share metric for zero-weight borrowing ClusterQueue, got=%v", dps[0].Value)
	}

	if err := cache.DeleteWorkload(log, workload.Key(wl)); err != nil {
		t.Fatalf("unexpected error deleting workload from cache: %v", err)
	}
	cache.RecordClusterQueueResourceMetrics(log, "cq1")
	expectGaugeValue(t, kueuemetrics.ClusterQueueWeightedShare, weightedShareLabels, 0)
}

func TestLocalQueueResourceMetricsReportUnlimitedAsInf(t *testing.T) {
	defer kueuemetrics.InitMetricVectors(nil)

	formatter := resources.NewResourceFormatter()
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}
	unlimited := resources.NewAmount(math.MaxInt64)
	lq := &LocalQueue{
		key:               queue.NewLocalQueueReference("namespace", "unlimited-lq"),
		reservedUsage:     resources.FlavorResourceQuantities{fr: unlimited},
		admittedUsage:     resources.FlavorResourceQuantities{fr: unlimited},
		customLabels:      kueuemetrics.NewCustomLabels(nil),
		resourceFormatter: formatter,
	}

	lq.reportResourceMetrics(map[resources.FlavorResource]ResourceQuota{fr: {}}, nil)

	labels := map[string]string{
		"name":         "unlimited-lq",
		"namespace":    "namespace",
		"flavor":       string(fr.Flavor),
		"resource":     string(fr.Resource),
		"replica_role": "standalone",
	}
	expectGaugeValue(t, kueuemetrics.LocalQueueResourceReservations, labels, math.Inf(1))
	expectGaugeValue(t, kueuemetrics.LocalQueueResourceUsage, labels, math.Inf(1))
}
