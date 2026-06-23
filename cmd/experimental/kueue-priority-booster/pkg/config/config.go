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

package config

import (
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/yaml"
)

// TimeSharing groups the parameters that control the time-sharing mode
// (no annotation while admitted for Interval, then a negative boost of
// magnitude NegativeBoost). Grouping leaves room for future modes of
// operation as sibling top-level keys.
type TimeSharing struct {
	// Interval is the minimum time a workload must be admitted before a
	// negative priority-boost is applied to enable preemption by same-priority
	// peers. Set to "0" to disable. Examples: "30m", "1h".
	Interval string `json:"interval,omitempty"`

	// NegativeBoost is the magnitude of the negative priority-boost applied
	// after Interval while the workload remains admitted (see README).
	// Defaults to 100000.
	NegativeBoost int32 `json:"negativeBoost,omitempty"`
}

// Configuration defines the configuration for the kueue-priority-booster.
type Configuration struct {
	// TimeSharing holds the parameters of the time-sharing mode.
	TimeSharing TimeSharing `json:"timeSharing,omitempty"`

	// WorkloadSelector, if set, limits which workloads the controller manages.
	// Workloads whose labels do not match are ignored; any annotation set by
	// this controller is cleared, but manually-set annotations are preserved.
	// An empty or nil selector matches all workloads.
	WorkloadSelector *metav1.LabelSelector `json:"workloadSelector,omitempty"`

	// MaxWorkloadPriority, if set, excludes workloads with Spec.Priority greater
	// than this value (nil Spec.Priority is treated as 0). Excluded workloads
	// have controller-managed annotations cleared; manually-set annotations are
	// preserved.
	MaxWorkloadPriority *int32 `json:"maxWorkloadPriority,omitempty"`
}

// Default returns the default configuration.
func Default() Configuration {
	return Configuration{
		TimeSharing: TimeSharing{
			Interval:      "0",
			NegativeBoost: 100000,
		},
	}
}

// ParsedConfiguration holds the parsed, ready-to-use form of Configuration.
type ParsedConfiguration struct {
	TimeSharingInterval time.Duration
	NegativeBoostValue  int32
	WorkloadSelector    labels.Selector
	MaxWorkloadPriority *int32
}

// Parse converts a Configuration into a ParsedConfiguration.
func Parse(cfg Configuration) (*ParsedConfiguration, error) {
	out := &ParsedConfiguration{
		NegativeBoostValue:  cfg.TimeSharing.NegativeBoost,
		MaxWorkloadPriority: cfg.MaxWorkloadPriority,
	}

	if cfg.TimeSharing.Interval != "" && cfg.TimeSharing.Interval != "0" {
		d, err := time.ParseDuration(cfg.TimeSharing.Interval)
		if err != nil {
			return nil, fmt.Errorf("invalid timeSharing.interval %q: %w", cfg.TimeSharing.Interval, err)
		}
		out.TimeSharingInterval = d
	}

	if cfg.WorkloadSelector != nil {
		sel, err := metav1.LabelSelectorAsSelector(cfg.WorkloadSelector)
		if err != nil {
			return nil, fmt.Errorf("invalid workloadSelector: %w", err)
		}
		out.WorkloadSelector = sel
	}

	return out, nil
}

// Load reads the configuration from the given file path.
// If the path is empty, it returns the default configuration.
func Load(path string) (*ParsedConfiguration, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal config file %s: %w", path, err)
		}
	}
	return Parse(cfg)
}
