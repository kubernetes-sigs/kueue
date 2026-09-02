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

	corev1 "k8s.io/api/core/v1"

	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
)

func TestClusterQueueResourceMetricsReportPastFloat64AsInf(t *testing.T) {
	defer kueuemetrics.InitMetricVectors(nil)

	formatter := resources.NewResourceFormatter()
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}
	unlimited := pastFloat64()
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

func TestLocalQueueResourceMetricsReportPastFloat64AsInf(t *testing.T) {
	defer kueuemetrics.InitMetricVectors(nil)

	formatter := resources.NewResourceFormatter()
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}
	unlimited := pastFloat64()
	lq := &LocalQueue{
		key:               queue.NewLocalQueueReference("namespace", "unlimited-lq"),
		totalReserved:     resources.FlavorResourceQuantities{fr: unlimited},
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
