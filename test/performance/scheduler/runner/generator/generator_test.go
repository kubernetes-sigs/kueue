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

package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestLoadConfig_StandardScheduler(t *testing.T) {
	testContent := `
# Standard scheduler config without TAS
cohorts:
  - className: cohort
    count: 2
    queuesSets:
      - className: cq
        count: 3
        nominalQuota: 20
        borrowingLimit: 100
        reclaimWithinCohort: Any
        withinClusterQueue: LowerPriority
        workloadsSets:
          - count: 100
            creationIntervalMs: 100
            workloads:
              - className: small
                runtimeMs: 200
                priority: 50
                request: 1
              - className: medium
                runtimeMs: 500
                priority: 100
                request: 5
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	got, err := LoadConfig(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	// Should have cohorts
	if len(got.Cohorts) != 1 {
		t.Errorf("expected 1 cohort, got %d", len(got.Cohorts))
	}

	// Should have default resource flavor
	if got.ResourceFlavor == nil {
		t.Fatal("expected default resource flavor to be set")
	}
	if got.ResourceFlavor.Name != "rf" {
		t.Errorf("expected default resource flavor name 'rf', got %q", got.ResourceFlavor.Name)
	}

	// Should not have topology (standard mode)
	if got.Topology != nil {
		t.Error("expected no topology for standard scheduler config")
	}

	// Verify cohort structure
	cohort := got.Cohorts[0]
	if cohort.ClassName != "cohort" {
		t.Errorf("expected className 'cohort', got %q", cohort.ClassName)
	}
	if cohort.Count != 2 {
		t.Errorf("expected count 2, got %d", cohort.Count)
	}
	if len(cohort.QueuesSets) != 1 {
		t.Errorf("expected 1 queueSet, got %d", len(cohort.QueuesSets))
	}

	// Verify queue structure
	queue := cohort.QueuesSets[0]
	if queue.ClassName != "cq" {
		t.Errorf("expected className 'cq', got %q", queue.ClassName)
	}
	if queue.Count != 3 {
		t.Errorf("expected count 3, got %d", queue.Count)
	}
	if queue.NominalQuota != "20" {
		t.Errorf("expected nominalQuota '20', got %q", queue.NominalQuota)
	}

	// Verify workloads
	if len(queue.WorkloadsSets) != 1 {
		t.Errorf("expected 1 workloadsSet, got %d", len(queue.WorkloadsSets))
	}
	wlSet := queue.WorkloadsSets[0]
	if wlSet.Count != 100 {
		t.Errorf("expected count 100, got %d", wlSet.Count)
	}
	if len(wlSet.Workloads) != 2 {
		t.Errorf("expected 2 workload templates, got %d", len(wlSet.Workloads))
	}
}

func TestLoadConfig_TAS(t *testing.T) {
	testContent := `
# TAS config with topology
topology:
  name: default-topology
  levels:
    - name: block
      count: 1
      nodeLabel: "topology.kubernetes.io/block"
    - name: rack
      count: 10
      nodeLabel: "topology.kubernetes.io/rack"
    - name: node
      count: 64
      nodeLabel: "kubernetes.io/hostname"
      capacity:
        cpu: "96"
        memory: "256Gi"

resourceFlavor:
  name: tas-flavor
  nodeLabel: "tas-node-group"
  topologyName: "default-topology"

cohorts:
  - className: tas-cohort
    count: 1
    queuesSets:
      - className: tas-cq
        count: 2
        nominalQuota: 50
        borrowingLimit: 200
        reclaimWithinCohort: Any
        withinClusterQueue: LowerPriority
        workloadsSets:
          - count: 50
            creationIntervalMs: 200
            workloads:
              - className: tas-workload
                runtimeMs: 1000
                priority: 100
                request: 2
                podCount: 8
                tasConstraint: required
                tasLevel: rack
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	got, err := LoadConfig(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	// Should have topology
	if got.Topology == nil {
		t.Fatal("expected topology to be set")
	}
	if got.Topology.Name != "default-topology" {
		t.Errorf("expected topology name 'default-topology', got %q", got.Topology.Name)
	}
	if len(got.Topology.Levels) != 3 {
		t.Errorf("expected 3 topology levels, got %d", len(got.Topology.Levels))
	}

	// Should have custom resource flavor
	if got.ResourceFlavor == nil {
		t.Fatal("expected resource flavor to be set")
	}
	if got.ResourceFlavor.Name != "tas-flavor" {
		t.Errorf("expected resource flavor name 'tas-flavor', got %q", got.ResourceFlavor.Name)
	}
	if got.ResourceFlavor.NodeLabel != "tas-node-group" {
		t.Errorf("expected node label 'tas-node-group', got %q", got.ResourceFlavor.NodeLabel)
	}

	// Should have cohorts
	if len(got.Cohorts) != 1 {
		t.Errorf("expected 1 cohort, got %d", len(got.Cohorts))
	}

	// Verify TAS workload template
	cohort := got.Cohorts[0]
	queue := cohort.QueuesSets[0]
	wlSet := queue.WorkloadsSets[0]
	if len(wlSet.Workloads) != 1 {
		t.Fatalf("expected 1 workload template, got %d", len(wlSet.Workloads))
	}

	wl := wlSet.Workloads[0]
	if wl.PodCount != 8 {
		t.Errorf("expected podCount 8, got %d", wl.PodCount)
	}
	if wl.TASConstraint != "required" {
		t.Errorf("expected tasConstraint 'required', got %q", wl.TASConstraint)
	}
	if wl.TASLevel != "rack" {
		t.Errorf("expected tasLevel 'rack', got %q", wl.TASLevel)
	}
}

func TestLoadConfig_EmptyCohorts(t *testing.T) {
	testContent := `
# Config with no cohorts
cohorts: []
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	_, err := LoadConfig(fPath)
	if err == nil {
		t.Fatal("expected error for empty cohorts, got nil")
	}
	if err.Error() != "config must contain at least one cohort" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	_, err := LoadConfig("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	testContent := `
cohorts:
  - className: invalid
    count: not-a-number
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	_, err := LoadConfig(fPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

func TestLoadConfig_TASBalancedPlacement(t *testing.T) {
	testContent := `
topology:
  name: test-topology
  levels:
    - name: node
      count: 16
      nodeLabel: "kubernetes.io/hostname"
      capacity:
        cpu: "96"
        memory: "256Gi"

resourceFlavor:
  name: test-flavor
  nodeLabel: "tas-node-group"
  topologyName: "default-topology"

cohorts:
  - className: balanced-cohort
    count: 1
    queuesSets:
      - className: balanced-cq
        count: 1
        nominalQuota: 100
        borrowingLimit: 0
        reclaimWithinCohort: Never
        withinClusterQueue: Never
        workloadsSets:
          - count: 10
            creationIntervalMs: 100
            workloads:
              - className: balanced-wl
                runtimeMs: 500
                priority: 100
                request: 1
                podCount: 16
                tasConstraint: balanced
                tasLevel: node
                sliceSize: 4
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	got, err := LoadConfig(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	// Verify balanced placement parameters
	wl := got.Cohorts[0].QueuesSets[0].WorkloadsSets[0].Workloads[0]
	if wl.TASConstraint != "balanced" {
		t.Errorf("expected tasConstraint 'balanced', got %q", wl.TASConstraint)
	}
	if wl.SliceSize != 4 {
		t.Errorf("expected sliceSize 4, got %d", wl.SliceSize)
	}
	if wl.PodCount != 16 {
		t.Errorf("expected podCount 16, got %d", wl.PodCount)
	}
}

func TestLoadConfig_TASDRA(t *testing.T) {
	testContent := `
topology:
  name: test-topology
  levels:
    - name: rack
      count: 2
      nodeLabel: "cloud.provider.com/topology-rack"
    - name: node
      count: 16
      nodeLabel: "kubernetes.io/hostname"

dra:
  devicesPerNode: 8

resourceFlavor:
  name: test-flavor
  nodeLabel: "tas-node-group"
  topologyName: "test-topology"

cohorts:
  - className: dra-cohort
    count: 1
    queuesSets:
      - className: dra-cq
        count: 1
        nominalQuota: 20
        borrowingLimit: 100
        reclaimWithinCohort: Any
        withinClusterQueue: LowerPriority
        deviceNominalQuota: "40"
        deviceBorrowingLimit: "200"
        workloadsSets:
          - count: 10
            creationIntervalMs: 100
            workloads:
              - className: dra-wl
                runtimeMs: 200
                priority: 50
                request: 500m
                podCount: 2
                devices: 1
                tasConstraint: required
                tasLevel: cloud.provider.com/topology-rack
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	got, err := LoadConfig(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %v", err)
	}

	if got.DRA == nil || got.DRA.DevicesPerNode != 8 {
		t.Errorf("expected dra.devicesPerNode 8, got %+v", got.DRA)
	}
	qSet := got.Cohorts[0].QueuesSets[0]
	if qSet.DeviceNominalQuota != "40" || qSet.DeviceBorrowingLimit != "200" {
		t.Errorf("expected device quota 40/200, got %q/%q", qSet.DeviceNominalQuota, qSet.DeviceBorrowingLimit)
	}
	if wl := qSet.WorkloadsSets[0].Workloads[0]; wl.Devices != 1 {
		t.Errorf("expected devices 1, got %d", wl.Devices)
	}
}

func TestGenerateNodesRecursive_UniqueHostnames(t *testing.T) {
	levels := []TopologyLevel{
		{Name: "block", Count: 1, NodeLabel: "cloud.provider.com/topology-block"},
		{Name: "rack", Count: 10, NodeLabel: "cloud.provider.com/topology-rack"},
		{Name: "node", Count: 64, NodeLabel: corev1.LabelHostname},
	}
	var nodes []corev1.Node
	generateNodesRecursive(levels, 0, []string{}, "96", "256Gi", &nodes)

	hostnames := sets.New[string]()
	for _, node := range nodes {
		if got := node.Labels[corev1.LabelHostname]; got != node.Name {
			t.Fatalf("node %s: expected hostname label %q, got %q", node.Name, node.Name, got)
		}
		hostnames.Insert(node.Labels[corev1.LabelHostname])
	}
	if hostnames.Len() != 640 {
		t.Errorf("expected 640 distinct hostnames, got %d", hostnames.Len())
	}
}

func TestLoadConfig_InvalidDevices(t *testing.T) {
	testContent := `
cohorts:
  - className: cohort
    count: 1
    queuesSets:
      - className: cq
        count: 1
        nominalQuota: 20
        deviceNominalQuota: 8
        workloadsSets:
          - count: 1
            workloads:
              - className: small
                request: 1
                tasLevel: kubernetes.io/hostname
                devices: 1
`
	tempDir := t.TempDir()
	fPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600)); err != nil {
		t.Fatalf("unable to create test file: %v", err)
	}

	_, err := LoadConfig(fPath)
	if err == nil {
		t.Fatal("expected error for devices without a dra section, got nil")
	}
	if err.Error() != `workload class "small" requests devices but the config has no dra section` {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestValidateDevices(t *testing.T) {
	testCases := map[string]struct {
		config  Config
		wantErr string
	}{
		"valid": {
			config: Config{
				DRA: &DRAConfig{DevicesPerNode: 8},
				Cohorts: []CohortSet{{QueuesSets: []QueuesSet{{
					ClassName:          "cq",
					DeviceNominalQuota: "8",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "small",
						TASLevel:  "kubernetes.io/hostname",
						Devices:   1,
					}}}},
				}}}},
			},
		},
		"zero devicesPerNode": {
			config: Config{
				DRA: &DRAConfig{DevicesPerNode: 0},
				Cohorts: []CohortSet{{QueuesSets: []QueuesSet{{
					ClassName:          "cq",
					DeviceNominalQuota: "8",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "small",
						TASLevel:  "kubernetes.io/hostname",
						Devices:   1,
					}}}},
				}}}},
			},
			wantErr: "dra.devicesPerNode must be positive",
		},
		"negative devices": {
			config: Config{
				DRA: &DRAConfig{DevicesPerNode: 8},
				Cohorts: []CohortSet{{QueuesSets: []QueuesSet{{
					ClassName:          "cq",
					DeviceNominalQuota: "8",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "small",
						TASLevel:  "kubernetes.io/hostname",
						Devices:   -1,
					}}}},
				}}}},
			},
			wantErr: `workload class "small": devices must not be negative`,
		},
		"devices without tasLevel": {
			config: Config{
				DRA: &DRAConfig{DevicesPerNode: 8},
				Cohorts: []CohortSet{{QueuesSets: []QueuesSet{{
					ClassName:          "cq",
					DeviceNominalQuota: "8",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "small",
						Devices:   1,
					}}}},
				}}}},
			},
			wantErr: `workload class "small": devices require tasLevel, since only TAS runs the device check`,
		},
		"devices without dra section": {
			config: Config{
				Cohorts: []CohortSet{{QueuesSets: []QueuesSet{{
					ClassName:          "cq",
					DeviceNominalQuota: "8",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "small",
						TASLevel:  "kubernetes.io/hostname",
						Devices:   1,
					}}}},
				}}}},
			},
			wantErr: `workload class "small" requests devices but the config has no dra section`,
		},
		"devices without deviceNominalQuota": {
			config: Config{
				DRA: &DRAConfig{DevicesPerNode: 8},
				Cohorts: []CohortSet{{QueuesSets: []QueuesSet{{
					ClassName: "cq",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "small",
						TASLevel:  "kubernetes.io/hostname",
						Devices:   1,
					}}}},
				}}}},
			},
			wantErr: `queue class "cq" runs workloads that request devices but has no deviceNominalQuota`,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			err := validateDevices(&tc.config)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateDevices() unexpected error: %v", err)
				}
			} else if err == nil || err.Error() != tc.wantErr {
				t.Fatalf("validateDevices() error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateDevicesNestedCohort(t *testing.T) {
	g := gomega.NewWithT(t)
	config := Config{
		Cohorts: []CohortSet{{
			Children: []CohortSet{{
				QueuesSets: []QueuesSet{{
					ClassName: "child-cq",
					WorkloadsSets: []WorkloadsSet{{Workloads: []WorkloadTemplate{{
						ClassName: "child-wl",
						TASLevel:  "kubernetes.io/hostname",
						Devices:   1,
					}}}},
				}},
			}},
		}},
	}

	g.Expect(validateDevices(&config)).To(gomega.MatchError(`workload class "child-wl" requests devices but the config has no dra section`))
}

func TestLoadAndGenerateNestedCohorts(t *testing.T) {
	g := gomega.NewWithT(t)
	content := `cohorts:
- className: root
  count: 1
  children:
  - className: child
    count: 2
    queuesSets:
    - className: cq
      count: 1
      nominalQuota: 10
      borrowingLimit: 10
      lendingLimit: 5
      borrowWithinCohort:
        policy: LowerPriority
      workloadsSets:
      - count: 1
        initialDelayMs: 100
        workloads:
        - className: wl
          request: 1
  - className: unused
    count: 0
`
	file := filepath.Join(t.TempDir(), "nested.yaml")
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadConfig(file)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	if err != nil {
		return
	}
	queue := got.Cohorts[0].Children[0].QueuesSets[0]
	g.Expect(got.Cohorts[0].Children).To(gomega.HaveLen(2))
	g.Expect(got.Cohorts[0].Children[0].Count).To(gomega.Equal(2))
	g.Expect(got.Cohorts[0].Children[1].Count).To(gomega.Equal(0))
	g.Expect(queue.LendingLimit).To(gomega.Equal("5"))
	g.Expect(queue.BorrowWithinCohort).NotTo(gomega.BeNil())
	g.Expect(queue.BorrowWithinCohort.Policy).To(gomega.Equal(kueue.BorrowWithinCohortPolicyLowerPriority))
	g.Expect(queue.WorkloadsSets[0].InitialDelayMs).To(gomega.Equal(uint(100)))

	cl := utiltesting.NewClientBuilder().Build()
	g.Expect(Generate(t.Context(), cl, got)).NotTo(gomega.HaveOccurred())

	var cohorts kueue.CohortList
	g.Expect(cl.List(t.Context(), &cohorts)).NotTo(gomega.HaveOccurred())

	actualNames := sets.New[string]()
	for _, cohort := range cohorts.Items {
		actualNames.Insert(cohort.Name)
	}
	expectedNames := sets.New(
		"root-0",
		"root-0-child-0",
		"root-0-child-1",
	)
	g.Expect(actualNames).To(gomega.Equal(expectedNames))
}
