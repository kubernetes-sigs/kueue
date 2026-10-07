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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
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

func TestClusterQueueDRADevicesReservedMetrics(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.KueueDRAIntegration, true)
	defer kueuemetrics.InitMetricVectors(nil)

	cq := &clusterQueue{
		Name:         "dra-cq",
		customLabels: kueuemetrics.NewCustomLabels(nil),
		draUsage:     make(map[workload.DRADeviceFlavorKey]int64),
	}
	t.Cleanup(func() {
		kueuemetrics.ClearClusterQueueDRADevicesReservedMetrics("dra-cq")
	})

	wl := utiltestingapi.MakeWorkload("wl", "default").
		PodSets(*utiltestingapi.MakePodSet("main", 2).Obj()).
		Admission(utiltestingapi.MakeAdmission("dra-cq").
			PodSets(kueue.PodSetAssignment{
				Name: "main",
				Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
					"example.com/gpu": "flavor-a",
				},
				Count: ptr.To[int32](2),
			}).
			Obj()).
		Obj()

	reqs := []workload.DRADeviceRequest{
		{
			PodSet:          "main",
			DeviceClass:     "gpu.example.com",
			LogicalResource: "example.com/gpu",
			CountPerPod:     2,
		},
	}
	info := workload.NewInfo(logr.Discard(), wl, workload.WithDRADeviceRequests(reqs))

	cq.updateWorkloadDRAUsage(logr.Discard(), info, add)

	labels := map[string]string{
		"cluster_queue": "dra-cq",
		"device_class":  "gpu.example.com",
		"flavor":        "flavor-a",
		"replica_role":  "standalone",
	}
	expectGaugeValue(t, kueuemetrics.ClusterQueueDRADevicesReserved, labels, 4)

	// Removing workload decrements usage to 0
	cq.updateWorkloadDRAUsage(logr.Discard(), info, subtract)
	expectGaugeValue(t, kueuemetrics.ClusterQueueDRADevicesReserved, labels, 0)
}
