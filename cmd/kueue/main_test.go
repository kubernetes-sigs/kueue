/*
Copyright 2021 The Kubernetes Authors.

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

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

func TestValidateIntegrationsName(t *testing.T) {
	tmpDir := t.TempDir()

	integrationsConfig := filepath.Join(tmpDir, "integrations.yaml")
	if err := os.WriteFile(integrationsConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
integrations:
  frameworks: 
  - batch/job
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	badIntegrationsConfig := filepath.Join(tmpDir, "badIntegrations.yaml")
	if err := os.WriteFile(badIntegrationsConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
integrations:
  frameworks:
  - unregistered/jobframework
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	enableDefaultInternalCertManagement := &config.InternalCertManagement{
		Enable:             ptr.To(true),
		WebhookServiceName: ptr.To(config.DefaultWebhookServiceName),
		WebhookSecretName:  ptr.To(config.DefaultWebhookSecretName),
	}

	configCmpOpts := []cmp.Option{
		cmpopts.IgnoreFields(config.Configuration{}, "ControllerManager"),
	}

	defaultClientConnection := &config.ClientConnection{
		QPS:   ptr.To(config.DefaultClientConnectionQPS),
		Burst: ptr.To(config.DefaultClientConnectionBurst),
	}

	testcases := []struct {
		name              string
		configFile        string
		wantConfiguration config.Configuration
		wantError         error
	}{
		{
			name:       "integrations config",
			configFile: integrationsConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations: &config.Integrations{
					// referencing job.FrameworkName ensures the link of job package
					// therefore the batch/framework should be registered
					Frameworks: []string{job.FrameworkName},
					PodOptions: &config.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "kubernetes.io/metadata.name",
									Operator: metav1.LabelSelectorOpNotIn,
									Values:   []string{"kube-system", "kueue-system"},
								},
							},
						},
						PodSelector: &metav1.LabelSelector{},
					},
				},
				QueueVisibility: &config.QueueVisibility{
					UpdateIntervalSeconds: config.DefaultQueueVisibilityUpdateIntervalSeconds,
					ClusterQueues: &config.ClusterQueueVisibility{
						MaxCount: config.DefaultClusterQueuesMaxCount,
					},
				},
				MultiKueue: &config.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: config.DefaultMultiKueueGCInterval},
					Origin:            ptr.To(config.DefaultMultiKueueOrigin),
					WorkerLostTimeout: &metav1.Duration{Duration: config.DefaultMultiKueueWorkerLostTimeout},
				},
			},
		},
		{
			name:       "bad integrations config",
			configFile: badIntegrationsConfig,
			wantError:  fmt.Errorf("integrations.frameworks: Unsupported value: \"unregistered/jobframework\": supported values: \"batch/job\", \"jobset.x-k8s.io/jobset\", \"kubeflow.org/mpijob\", \"kubeflow.org/mxjob\", \"kubeflow.org/paddlejob\", \"kubeflow.org/pytorchjob\", \"kubeflow.org/tfjob\", \"kubeflow.org/xgboostjob\", \"pod\", \"ray.io/raycluster\", \"ray.io/rayjob\""),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg, err := apply(tc.configFile)
			if tc.wantError == nil {
				if err != nil {
					t.Errorf("Unexpected error:%s", err)
				}
				if diff := cmp.Diff(tc.wantConfiguration, cfg, configCmpOpts...); diff != "" {
					t.Errorf("Unexpected config (-want +got):\n%s", diff)
				}
			} else {
				if diff := cmp.Diff(tc.wantError.Error(), err.Error()); diff != "" {
					t.Errorf("Unexpected error (-want +got):\n%s", diff)
				}
			}
		})
	}
}
