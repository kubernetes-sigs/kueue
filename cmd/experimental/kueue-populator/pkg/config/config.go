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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/yaml"
)

type LocalQueueNameMode string

const (
	LocalQueueNameModeStatic         LocalQueueNameMode = "Static"
	LocalQueueNameModeAsClusterQueue LocalQueueNameMode = "AsClusterQueue"
)

// Configuration defines the configuration for the kueue-populator controller.
type Configuration struct {
	// LocalQueueName is the name of the LocalQueue to create.
	// Only used when LocalQueueNameMode is "Static" (default).
	LocalQueueName string `json:"localQueueName,omitempty"`

	// LocalQueueNameMode determines how the LocalQueue name is derived.
	// "Static" (default): uses the value of LocalQueueName.
	// "AsClusterQueue": uses the ClusterQueue's name as the LocalQueue name.
	LocalQueueNameMode LocalQueueNameMode `json:"localQueueNameMode,omitempty"`

	// ManagedJobsNamespaceSelector selects namespaces where the controller creates LocalQueues.
	// It mimics the behavior of the same flag in Kueue.
	ManagedJobsNamespaceSelector *metav1.LabelSelector `json:"managedJobsNamespaceSelector,omitempty"`
}

// Default returns a default Configuration.
func Default() Configuration {
	return Configuration{
		LocalQueueName: "default",
		ManagedJobsNamespaceSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{
				{
					Key:      "kubernetes.io/metadata.name",
					Operator: metav1.LabelSelectorOpNotIn,
					Values:   []string{"kube-system"},
				},
			},
		},
	}
}

// Validate checks the configuration for invalid combinations.
func (c *Configuration) Validate() field.ErrorList {
	var allErrs field.ErrorList
	switch c.LocalQueueNameMode {
	case LocalQueueNameModeStatic, "":
	case LocalQueueNameModeAsClusterQueue:
		if c.LocalQueueName != "" {
			allErrs = append(allErrs, field.Invalid(
				field.NewPath("localQueueName"),
				c.LocalQueueName,
				"localQueueName cannot be set when localQueueNameMode is AsClusterQueue",
			))
		}
	default:
		allErrs = append(allErrs, field.NotSupported(
			field.NewPath("localQueueNameMode"),
			c.LocalQueueNameMode,
			[]string{string(LocalQueueNameModeStatic), string(LocalQueueNameModeAsClusterQueue)},
		))
	}
	return allErrs
}

// Load reads the configuration from the given file path.
// If the path is empty, it returns the default configuration.
func Load(path string) (*Configuration, error) {
	if path == "" {
		cfg := Default()
		return &cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var raw Configuration
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file %s: %w", path, err)
	}
	if errs := raw.Validate(); len(errs) > 0 {
		return nil, fmt.Errorf("invalid configuration: %s", errs.ToAggregate().Error())
	}

	cfg := Default()
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file %s: %w", path, err)
	}
	if cfg.LocalQueueNameMode == LocalQueueNameModeAsClusterQueue {
		cfg.LocalQueueName = ""
	}

	return &cfg, nil
}
