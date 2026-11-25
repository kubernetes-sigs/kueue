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

package cert

import (
	"testing"
)

func TestDeriveWebhookBaseName(t *testing.T) {
	tests := map[string]struct {
		webhookServiceName string
		expectedBaseName   string
	}{
		"default kueue service name": {
			webhookServiceName: "kueue-webhook-service",
			expectedBaseName:   "kueue",
		},
		"custom release name": {
			webhookServiceName: "test-kueue-webhook-service",
			expectedBaseName:   "test-kueue",
		},
		"production release name": {
			webhookServiceName: "prod-kueue-webhook-service",
			expectedBaseName:   "prod-kueue",
		},
		"complex release name": {
			webhookServiceName: "my-company-staging-kueue-webhook-service",
			expectedBaseName:   "my-company-staging-kueue",
		},
		"service name without expected suffix": {
			webhookServiceName: "kueue-service",
			expectedBaseName:   "kueue-service",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := deriveWebhookBaseName(tt.webhookServiceName)
			if result != tt.expectedBaseName {
				t.Errorf("deriveWebhookBaseName(%q) = %q, want %q",
					tt.webhookServiceName, result, tt.expectedBaseName)
			}
		})
	}
}

func TestBuildWebhookConfigurationName(t *testing.T) {
	tests := map[string]struct {
		baseName     string
		webhookType  string
		expectedName string
	}{
		"mutating webhook for kueue": {
			baseName:     "kueue",
			webhookType:  "mutating",
			expectedName: "kueue-mutating-webhook-configuration",
		},
		"validating webhook for kueue": {
			baseName:     "kueue",
			webhookType:  "validating",
			expectedName: "kueue-validating-webhook-configuration",
		},
		"mutating webhook for custom release": {
			baseName:     "custome-kueue-name",
			webhookType:  "mutating",
			expectedName: "custome-kueue-name-mutating-webhook-configuration",
		},
		"validating webhook for custom release": {
			baseName:     "custome-kueue-name",
			webhookType:  "validating",
			expectedName: "custome-kueue-name-validating-webhook-configuration",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			result := buildWebhookConfigurationName(tt.baseName, tt.webhookType)
			if result != tt.expectedName {
				t.Errorf("buildWebhookConfigurationName(%q, %q) = %q, want %q",
					tt.baseName, tt.webhookType, result, tt.expectedName)
			}
		})
	}
}
