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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

func TestApply(t *testing.T) {
	// temp dir
	tmpDir, err := os.MkdirTemp("", "temp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	namespaceOverWriteConfig := filepath.Join(tmpDir, "namespace-overwrite.yaml")
	if err := os.WriteFile(namespaceOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-tenant-a
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8080
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	ctrlManagerConfigSpecOverWriteConfig := filepath.Join(tmpDir, "ctrl-manager-config-spec-overwrite.yaml")
	if err := os.WriteFile(ctrlManagerConfigSpecOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :38081
metrics:
  bindAddress: :38080
leaderElection:
  leaderElect: true
  resourceName: test-id
webhook:
  port: 9444
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	certOverWriteConfig := filepath.Join(tmpDir, "cert-overwrite.yaml")
	if err := os.WriteFile(certOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8080
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
webhook:
  port: 9443
internalCertManagement:
  enable: true
  webhookServiceName: kueue-tenant-a-webhook-service
  webhookSecretName: kueue-tenant-a-webhook-server-cert
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	disableCertOverWriteConfig := filepath.Join(tmpDir, "disable-cert-overwrite.yaml")
	if err := os.WriteFile(disableCertOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8080
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
webhook:
  port: 9443
internalCertManagement:
  enable: false
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	leaderElectionDisabledConfig := filepath.Join(tmpDir, "leaderElection-disabled.yaml")
	if err := os.WriteFile(leaderElectionDisabledConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8080
leaderElection:
  leaderElect: false
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	waitForPodsReadyEnabledConfig := filepath.Join(tmpDir, "waitForPodsReady-enabled.yaml")
	if err := os.WriteFile(waitForPodsReadyEnabledConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
waitForPodsReady:
  enable: true
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	clientConnectionConfig := filepath.Join(tmpDir, "clientConnection.yaml")
	if err := os.WriteFile(clientConnectionConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8080
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
webhook:
  port: 9443
clientConnection:
  qps: 50
  burst: 100
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	integrationsConfig := filepath.Join(tmpDir, "integrations.yaml")
	if err := os.WriteFile(integrationsConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
integrations:
  frameworks: 
  - a
  - b
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	defaultControlOptions := ctrl.Options{
		Port:                   config.DefaultWebhookPort,
		HealthProbeBindAddress: config.DefaultHealthProbeBindAddress,
		MetricsBindAddress:     config.DefaultMetricsBindAddress,
		LeaderElectionID:       config.DefaultLeaderElectionID,
		LeaderElection:         true,
	}

	enableDefaultInternalCertManagement := &config.InternalCertManagement{
		Enable:             pointer.Bool(true),
		WebhookServiceName: pointer.String(config.DefaultWebhookServiceName),
		WebhookSecretName:  pointer.String(config.DefaultWebhookSecretName),
	}

	ctrlOptsCmpOpts := []cmp.Option{
		cmpopts.IgnoreUnexported(ctrl.Options{}),
		cmpopts.IgnoreFields(ctrl.Options{}, "Scheme", "Logger"),
	}

	configCmpOpts := []cmp.Option{
		cmpopts.IgnoreFields(config.Configuration{}, "ControllerManagerConfigurationSpec"),
	}

	defaultClientConnection := &config.ClientConnection{
		QPS:   pointer.Float32(config.DefaultClientConnectionQPS),
		Burst: pointer.Int32(config.DefaultClientConnectionBurst),
	}

	defaultIntegrations := &config.Integrations{
		Frameworks: []string{job.FrameworkName},
	}

	testcases := []struct {
		name              string
		configFile        string
		wantConfiguration config.Configuration
		wantOptions       ctrl.Options
	}{
		{
			name:       "default config",
			configFile: "",
			wantConfiguration: config.Configuration{
				Namespace:              pointer.String(config.DefaultNamespace),
				InternalCertManagement: enableDefaultInternalCertManagement,
				ClientConnection:       defaultClientConnection,
				Integrations:           defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				Port:                   config.DefaultWebhookPort,
				HealthProbeBindAddress: config.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     config.DefaultMetricsBindAddress,
				LeaderElectionID:       "",
				LeaderElection:         false,
			},
		},
		{
			name:       "namespace overwrite config",
			configFile: namespaceOverWriteConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String("kueue-tenant-a"),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations:               defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "ControllerManagerConfigurationSpec overwrite config",
			configFile: ctrlManagerConfigSpecOverWriteConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations:               defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress: ":38081",
				MetricsBindAddress:     ":38080",
				Port:                   9444,
				LeaderElection:         true,
				LeaderElectionID:       "test-id",
			},
		},
		{
			name:       "cert options overwrite config",
			configFile: certOverWriteConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &config.InternalCertManagement{
					Enable:             pointer.Bool(true),
					WebhookServiceName: pointer.String("kueue-tenant-a-webhook-service"),
					WebhookSecretName:  pointer.String("kueue-tenant-a-webhook-server-cert"),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "disable cert overwrite config",
			configFile: disableCertOverWriteConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &config.InternalCertManagement{
					Enable: pointer.Bool(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "leaderElection disabled config",
			configFile: leaderElectionDisabledConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String("kueue-system"),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations:               defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				Port:                   config.DefaultWebhookPort,
				HealthProbeBindAddress: config.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     config.DefaultMetricsBindAddress,
				LeaderElectionID:       "",
				LeaderElection:         false,
			},
		},
		{
			name:       "enable waitForPodsReady config",
			configFile: waitForPodsReadyEnabledConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				WaitForPodsReady: &config.WaitForPodsReady{
					Enable:  true,
					Timeout: &metav1.Duration{Duration: 5 * time.Minute},
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				Port:                   config.DefaultWebhookPort,
				HealthProbeBindAddress: config.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     config.DefaultMetricsBindAddress,
			},
		},
		{
			name:       "clientConnection config",
			configFile: clientConnectionConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection: &config.ClientConnection{
					QPS:   pointer.Float32(50),
					Burst: pointer.Int32(100),
				},
				Integrations: defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "integrations config",
			configFile: integrationsConfig,
			wantConfiguration: config.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: config.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  pointer.String(config.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations: &config.Integrations{
					Frameworks: []string{"a", "b"},
				},
			},
			wantOptions: ctrl.Options{
				Port:                   config.DefaultWebhookPort,
				HealthProbeBindAddress: config.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     config.DefaultMetricsBindAddress,
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			options, cfg := apply(tc.configFile)
			if diff := cmp.Diff(tc.wantConfiguration, cfg, configCmpOpts...); diff != "" {
				t.Errorf("Unexpected config (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantOptions, options, ctrlOptsCmpOpts...); diff != "" {
				t.Errorf("Unexpected options (-want +got):\n%s", diff)
			}
		})
	}
}
