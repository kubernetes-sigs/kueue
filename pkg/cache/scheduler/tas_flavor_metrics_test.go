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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
)

const (
	testFlavorName  kueue.ResourceFlavorReference = "gpu-flavor"
	testTopologyRef kueue.TopologyReference        = "default"
	testGPUResource corev1.ResourceName            = "nvidia.com/gpu"
	testRackLabel                                   = "cloud.com/topology-rack"
)

func newTestTASFlavorCache(levels []string) *TASFlavorCache {
	tc := NewTASCache(utiltesting.NewFakeClient())
	return tc.NewTASFlavorCache(
		topologyInformation{Levels: levels},
		flavorInformation{TopologyName: testTopologyRef},
	)
}

func collectTASDomainMetrics(flavor string) []testingmetrics.MetricDataPoint {
	return testingmetrics.CollectFilteredGaugeVec(
		metrics.TASDomainUsage,
		map[string]string{"flavor": flavor},
	)
}

var sortMetrics = cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool {
	return a.Less(&b)
})

func wantDP(flavor, domain, domainID, resource string, value float64) testingmetrics.MetricDataPoint {
	return testingmetrics.MetricDataPoint{
		Labels: map[string]string{
			"flavor":    flavor,
			"domain":    domain,
			"domain_id": domainID,
			"resource":  resource,
		},
		Value: value,
	}
}

// TestReportDomainMetricsSingleHost verifies that a single-level (hostname only)
// topology reports usage at the host level.
func TestReportDomainMetricsSingleHost(t *testing.T) {
	cache := newTestTASFlavorCache([]string{corev1.LabelHostname})
	defer metrics.ClearTASDomainUsageForFlavor(string(testFlavorName))

	// Two pods on host-1, each requesting 4 GPUs → 8 GPUs total.
	cache.addUsage("wl1", []workload.TopologyDomainRequests{
		{
			Values:            []string{"host-1"},
			SinglePodRequests: resources.Requests{testGPUResource: 4},
			Count:             2,
		},
	})
	cache.reportDomainMetrics(testFlavorName)

	want := []testingmetrics.MetricDataPoint{
		wantDP(string(testFlavorName), corev1.LabelHostname, "host-1", string(testGPUResource), 8),
	}
	if diff := cmp.Diff(want, collectTASDomainMetrics(string(testFlavorName)), sortMetrics); diff != "" {
		t.Errorf("unexpected metric data (-want +got):\n%s", diff)
	}
}

// TestReportDomainMetricsMultipleHosts verifies that multiple leaf domains are
// reported independently.
func TestReportDomainMetricsMultipleHosts(t *testing.T) {
	cache := newTestTASFlavorCache([]string{corev1.LabelHostname})
	defer metrics.ClearTASDomainUsageForFlavor(string(testFlavorName))

	cache.addUsage("wl1", []workload.TopologyDomainRequests{
		{Values: []string{"host-1"}, SinglePodRequests: resources.Requests{testGPUResource: 8}, Count: 1},
		{Values: []string{"host-2"}, SinglePodRequests: resources.Requests{testGPUResource: 4}, Count: 1},
	})
	cache.reportDomainMetrics(testFlavorName)

	want := []testingmetrics.MetricDataPoint{
		wantDP(string(testFlavorName), corev1.LabelHostname, "host-1", string(testGPUResource), 8),
		wantDP(string(testFlavorName), corev1.LabelHostname, "host-2", string(testGPUResource), 4),
	}
	if diff := cmp.Diff(want, collectTASDomainMetrics(string(testFlavorName)), sortMetrics); diff != "" {
		t.Errorf("unexpected metric data (-want +got):\n%s", diff)
	}
}

// TestReportDomainMetricsWorkloadRemoval verifies that removing a workload
// causes its domain metrics to disappear (usage reaches zero → series cleared).
func TestReportDomainMetricsWorkloadRemoval(t *testing.T) {
	cache := newTestTASFlavorCache([]string{corev1.LabelHostname})
	defer metrics.ClearTASDomainUsageForFlavor(string(testFlavorName))

	cache.addUsage("wl1", []workload.TopologyDomainRequests{
		{Values: []string{"host-1"}, SinglePodRequests: resources.Requests{testGPUResource: 8}, Count: 1},
	})
	cache.reportDomainMetrics(testFlavorName)

	if got := collectTASDomainMetrics(string(testFlavorName)); len(got) != 1 {
		t.Fatalf("expected 1 metric after add, got %d", len(got))
	}

	cache.removeUsage("wl1")
	cache.reportDomainMetrics(testFlavorName)

	// After removal the domain still appears with value 0 (idle but known).
	want := []testingmetrics.MetricDataPoint{
		wantDP(string(testFlavorName), corev1.LabelHostname, "host-1", string(testGPUResource), 0),
	}
	if diff := cmp.Diff(want, collectTASDomainMetrics(string(testFlavorName)), sortMetrics); diff != "" {
		t.Errorf("unexpected metric data after removal (-want +got):\n%s", diff)
	}
}

// TestReportDomainMetricsMultiLevelTopology verifies that a two-level topology
// (rack → host) produces metrics at both the rack level and the host level.
// Rack-1 holds host-1 and host-2; rack-2 holds host-3.
func TestReportDomainMetricsMultiLevelTopology(t *testing.T) {
	cache := newTestTASFlavorCache([]string{testRackLabel, corev1.LabelHostname})
	defer metrics.ClearTASDomainUsageForFlavor(string(testFlavorName))

	cache.addUsage("wl1", []workload.TopologyDomainRequests{
		{Values: []string{"rack-1", "host-1"}, SinglePodRequests: resources.Requests{testGPUResource: 8}, Count: 1},
		{Values: []string{"rack-1", "host-2"}, SinglePodRequests: resources.Requests{testGPUResource: 8}, Count: 1},
		{Values: []string{"rack-2", "host-3"}, SinglePodRequests: resources.Requests{testGPUResource: 4}, Count: 1},
	})
	cache.reportDomainMetrics(testFlavorName)

	want := []testingmetrics.MetricDataPoint{
		// Rack-level aggregates (domain = rack label key, domain_id = just the rack value).
		wantDP(string(testFlavorName), testRackLabel, "rack-1", string(testGPUResource), 16),
		wantDP(string(testFlavorName), testRackLabel, "rack-2", string(testGPUResource), 4),
		// Host-level leaf domains (domain = hostname label key, domain_id = full path).
		wantDP(string(testFlavorName), corev1.LabelHostname, "rack-1,host-1", string(testGPUResource), 8),
		wantDP(string(testFlavorName), corev1.LabelHostname, "rack-1,host-2", string(testGPUResource), 8),
		wantDP(string(testFlavorName), corev1.LabelHostname, "rack-2,host-3", string(testGPUResource), 4),
	}
	if diff := cmp.Diff(want, collectTASDomainMetrics(string(testFlavorName)), sortMetrics); diff != "" {
		t.Errorf("unexpected metric data (-want +got):\n%s", diff)
	}
}

// TestReportDomainMetricsEmptyLevels verifies that a cache with no topology
// levels neither panics nor emits any metrics.
func TestReportDomainMetricsEmptyLevels(t *testing.T) {
	cache := newTestTASFlavorCache([]string{})
	cache.reportDomainMetrics(testFlavorName)
	// Verified by absence of panic; no metrics should appear.
	if got := collectTASDomainMetrics(string(testFlavorName)); len(got) != 0 {
		t.Errorf("expected 0 metrics for empty topology, got %d", len(got))
	}
}
