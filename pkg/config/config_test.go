/*
Copyright 2023 The Kubernetes Authors.

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
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	runtimeconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
)

func TestLoad(t *testing.T) {
	test_scheme := runtime.NewScheme()
	err := configapi.AddToScheme(test_scheme)
	if err != nil {
		t.Fatal(err)
	}

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

	fullControllerConfig := filepath.Join(tmpDir, "fullControllerConfig.yaml")
	if err := os.WriteFile(fullControllerConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
  readinessEndpointName: ready
  livenessEndpointName: live
metrics:
  bindAddress: :8080
pprofBindAddress: :8082
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
  resourceNamespace: namespace
  resourceLock: lock
  leaseDuration: 100s
  renewDeadline: 15s
  retryPeriod: 30s
webhook:
  port: 9443
  host: host
  certDir: certDir
controller:
  groupKindConcurrency:
    workload: 5
  cacheSyncTimeout: 3
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
  - batch/job
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	defaultControlOptions := ctrl.Options{
		HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
		MetricsBindAddress:     configapi.DefaultMetricsBindAddress,
		LeaderElectionID:       configapi.DefaultLeaderElectionID,
		LeaderElection:         true,
		WebhookServer: &webhook.DefaultServer{
			Options: webhook.Options{
				Port: configapi.DefaultWebhookPort,
			},
		},
	}

	enableDefaultInternalCertManagement := &configapi.InternalCertManagement{
		Enable:             ptr.To(true),
		WebhookServiceName: ptr.To(configapi.DefaultWebhookServiceName),
		WebhookSecretName:  ptr.To(configapi.DefaultWebhookSecretName),
	}

	ctrlOptsCmpOpts := []cmp.Option{
		cmpopts.IgnoreUnexported(ctrl.Options{}),
		cmpopts.IgnoreUnexported(webhook.DefaultServer{}),
		cmpopts.IgnoreFields(ctrl.Options{}, "Scheme", "Logger"),
	}

	// Ignore the controller manager section since it's side effect is checked against
	// the content of  the resulting options
	configCmpOpts := []cmp.Option{
		cmpopts.IgnoreFields(configapi.Configuration{}, "ControllerManager"),
	}

	defaultClientConnection := &configapi.ClientConnection{
		QPS:   ptr.To[float32](configapi.DefaultClientConnectionQPS),
		Burst: ptr.To[int32](configapi.DefaultClientConnectionBurst),
	}

	defaultIntegrations := &configapi.Integrations{
		Frameworks: []string{job.FrameworkName},
	}

	testcases := []struct {
		name              string
		configFile        string
		wantConfiguration configapi.Configuration
		wantOptions       ctrl.Options
		wantError         error
	}{
		{
			name:       "default config",
			configFile: "",
			wantConfiguration: configapi.Configuration{
				Namespace:              ptr.To(configapi.DefaultNamespace),
				InternalCertManagement: enableDefaultInternalCertManagement,
				ClientConnection:       defaultClientConnection,
				Integrations:           defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     configapi.DefaultMetricsBindAddress,
				LeaderElectionID:       "",
				LeaderElection:         false,
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port: configapi.DefaultWebhookPort,
					},
				},
			},
		},
		{
			name:       "bad path",
			configFile: ".",
			wantError: &fs.PathError{
				Op:   "read",
				Path: ".",
				Err:  errors.New("is a directory"),
			},
		},
		{
			name:       "namespace overwrite config",
			configFile: namespaceOverWriteConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To("kueue-tenant-a"),
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
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations:               defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress: ":38081",
				MetricsBindAddress:     ":38080",
				LeaderElection:         true,
				LeaderElectionID:       "test-id",
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port: 9444,
					},
				},
			},
		},
		{
			name:       "cert options overwrite config",
			configFile: certOverWriteConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To("kueue-tenant-a-webhook-service"),
					WebhookSecretName:  ptr.To("kueue-tenant-a-webhook-server-cert"),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "disable cert overwrite config",
			configFile: disableCertOverWriteConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable: ptr.To(false),
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "leaderElection disabled config",
			configFile: leaderElectionDisabledConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To("kueue-system"),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations:               defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     configapi.DefaultMetricsBindAddress,
				LeaderElectionID:       "",
				LeaderElection:         false,
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port: configapi.DefaultWebhookPort,
					},
				},
			},
		},
		{
			name:       "enable waitForPodsReady config",
			configFile: waitForPodsReadyEnabledConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable:         true,
					BlockAdmission: ptr.To(true),
					Timeout:        &metav1.Duration{Duration: 5 * time.Minute},
				},
				ClientConnection: defaultClientConnection,
				Integrations:     defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     configapi.DefaultMetricsBindAddress,
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port: configapi.DefaultWebhookPort,
					},
				},
			},
		},
		{
			name:       "clientConnection config",
			configFile: clientConnectionConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection: &configapi.ClientConnection{
					QPS:   ptr.To[float32](50),
					Burst: ptr.To[int32](100),
				},
				Integrations: defaultIntegrations,
			},
			wantOptions: defaultControlOptions,
		},
		{
			name:       "fullController config",
			configFile: fullControllerConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection: &configapi.ClientConnection{
					QPS:   ptr.To[float32](50),
					Burst: ptr.To[int32](100),
				},
				Integrations: defaultIntegrations,
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress:     configapi.DefaultHealthProbeBindAddress,
				ReadinessEndpointName:      "ready",
				LivenessEndpointName:       "live",
				MetricsBindAddress:         configapi.DefaultMetricsBindAddress,
				PprofBindAddress:           ":8082",
				LeaderElection:             true,
				LeaderElectionID:           configapi.DefaultLeaderElectionID,
				LeaderElectionNamespace:    "namespace",
				LeaderElectionResourceLock: "lock",
				LeaseDuration:              ptr.To(time.Second * 100),
				RenewDeadline:              ptr.To(time.Second * 15),
				RetryPeriod:                ptr.To(time.Second * 30),
				Controller: runtimeconfig.Controller{
					GroupKindConcurrency: map[string]int{
						"workload": 5,
					},
					CacheSyncTimeout: 3,
				},
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						Host:    "host",
						CertDir: "certDir",
					},
				},
			},
		},
		{
			name:       "integrations config",
			configFile: integrationsConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				Integrations: &configapi.Integrations{
					// referencing job.FrameworkName ensures the link of job package
					// therefore the batch/framework should be registered
					Frameworks: []string{job.FrameworkName},
				},
			},
			wantOptions: ctrl.Options{
				HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
				MetricsBindAddress:     configapi.DefaultMetricsBindAddress,
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port: configapi.DefaultWebhookPort,
					},
				},
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			options, cfg, err := Load(test_scheme, tc.configFile)
			if tc.wantError == nil {
				if err != nil {
					t.Errorf("Unexpected error:%s", err)
				}
				if diff := cmp.Diff(tc.wantConfiguration, cfg, configCmpOpts...); diff != "" {
					t.Errorf("Unexpected config (-want +got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantOptions, options, ctrlOptsCmpOpts...); diff != "" {
					t.Errorf("Unexpected options (-want +got):\n%s", diff)
				}
			} else {
				if diff := cmp.Diff(tc.wantError.Error(), err.Error()); diff != "" {
					t.Errorf("Unexpected error (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestEncode(t *testing.T) {
	test_scheme := runtime.NewScheme()
	err := configapi.AddToScheme(test_scheme)
	if err != nil {
		t.Fatal(err)
	}

	defaultConfig := &configapi.Configuration{}
	test_scheme.Default(defaultConfig)

	testcases := []struct {
		name       string
		scheme     *runtime.Scheme
		cfg        *configapi.Configuration
		wantResult map[string]any
	}{

		{
			name:   "empty",
			scheme: test_scheme,
			cfg:    &configapi.Configuration{},
			wantResult: map[string]any{
				"apiVersion":                 "config.kueue.x-k8s.io/v1beta1",
				"kind":                       "Configuration",
				"manageJobsWithoutQueueName": false,
				"health":                     map[string]any{},
				"metrics":                    map[string]any{},
				"webhook":                    map[string]any{},
			},
		},
		{
			name:   "default",
			scheme: test_scheme,
			cfg:    defaultConfig,
			wantResult: map[string]any{
				"apiVersion": "config.kueue.x-k8s.io/v1beta1",
				"kind":       "Configuration",
				"namespace":  configapi.DefaultNamespace,
				"webhook": map[string]any{
					"port": int64(configapi.DefaultWebhookPort),
				},
				"metrics": map[string]any{
					"bindAddress": configapi.DefaultMetricsBindAddress,
				},
				"health": map[string]any{
					"healthProbeBindAddress": configapi.DefaultHealthProbeBindAddress,
				},
				"internalCertManagement": map[string]any{
					"enable":             true,
					"webhookServiceName": configapi.DefaultWebhookServiceName,
					"webhookSecretName":  configapi.DefaultWebhookSecretName,
				},
				"clientConnection": map[string]any{
					"burst": int64(configapi.DefaultClientConnectionBurst),
					"qps":   int64(configapi.DefaultClientConnectionQPS),
				},
				"manageJobsWithoutQueueName": false,
				"integrations": map[string]any{
					"frameworks": []any{"batch/job"},
				},
			},
		},
	}
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Encode(tc.scheme, tc.cfg)
			if err != nil {
				t.Errorf("Unexpected error:%s", err)
			}
			gotMap := map[string]interface{}{}
			err = yaml.Unmarshal([]byte(got), &gotMap)
			if err != nil {
				t.Errorf("Unable to unmarshal result:%s", err)
			}
			if diff := cmp.Diff(tc.wantResult, gotMap); diff != "" {
				t.Errorf("Unexpected result (-want +got):\n%s", diff)
			}
		})
	}
}
