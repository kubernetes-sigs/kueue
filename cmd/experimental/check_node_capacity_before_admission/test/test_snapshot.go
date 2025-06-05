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

package testsnapshot

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	// "sigs.k8s.io/kueue/cmd/experimental/check_node_capacity_before_admission/pkg/resource_monitor"
	corev1 "k8s.io/api/core/v1"
)

// PodInfo holds information about each pod running on a node
type PodInfo struct {
	Name      string              `json:"name"`
	UUID      string              `json:"uuid"`
	Namespace string              `json:"namespace"`
	Resources corev1.ResourceList `json:"resources"`
}

// NodeUsage holds the resource usage data for a single node
type NodeUsage struct {
	NodeName  string              `json:"node_name"`
	UUID      string              `json:"uuid"`
	Taints    []corev1.Taint      `json:"taints"`
	Labels    map[string]string   `json:"labels"`
	Remaining corev1.ResourceList `json:"remaining"`
	Pods      []PodInfo           `json:"pods"`
}

// Snapshot holds resource data for all nodes and the last check timestamp
type Snapshot struct {
	LastCheck time.Time   `json:"last_check"`
	Nodes     []NodeUsage `json:"nodes"`
}

// SnapshotManager manages the snapshot and provides methods to access and update it
type SnapshotManager struct {
	snapshot Snapshot
	mu       sync.RWMutex
}

// SaveSnapshotToFile saves the current snapshot to a JSON file
func (sm *SnapshotManager) SaveSnapshotToFile(filePath string) error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	// Marshal the snapshot to JSON
	data, err := json.MarshalIndent(sm.snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot: %w", err)
	}

	// Write the JSON data to a file
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	return nil
}

// LoadSnapshotFromFile loads a snapshot from a JSON file
func (sm *SnapshotManager) LoadSnapshotFromFile(filePath string) error {
	// Read the JSON file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Read the file contents
	content, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Parse the JSON content
	var snapshot Snapshot
	if err := json.Unmarshal(content, &snapshot); err != nil {
		return fmt.Errorf("failed to parse JSON: %w", err)
	}

	// Safely update the snapshot in memory
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.snapshot = snapshot

	return nil
}

// GetSnapshot returns the current snapshot
func (sm *SnapshotManager) GetSnapshot() Snapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.snapshot
}
