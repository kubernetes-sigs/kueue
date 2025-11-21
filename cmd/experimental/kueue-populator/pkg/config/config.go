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
	"sigs.k8s.io/yaml"
)

// Configuration defines the configuration for the kueue-populator controller.
type Configuration struct {
	// LocalQueueName is the name of the LocalQueue to create by default.
	LocalQueueName string `json:"localQueueName,omitempty"`

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

// Load reads the configuration from the given file path.
// If the path is empty, it returns the default configuration.
func Load(path string) (*Configuration, error) {
	cfg := Default()
	if path == "" {
		return &cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config file %s: %w", path, err)
	}

	return &cfg, nil
}
