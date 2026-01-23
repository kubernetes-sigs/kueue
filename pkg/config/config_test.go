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
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsfake "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	resourcev1 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	runtimeconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

func defaultControlCacheOptions(namespace string) ctrlcache.Options {
	return ctrlcache.Options{
		ByObject: map[ctrlclient.Object]ctrlcache.ByObject{
			objectKeySecret: {
				Namespaces: map[string]ctrlcache.Config{
					namespace: {},
				},
			},
		},
	}
}

func defaultControlOptions(namespace string) ctrl.Options {
	return ctrl.Options{
		Cache:                  defaultControlCacheOptions(namespace),
		HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
		Metrics: metricsserver.Options{
			BindAddress: configapi.DefaultMetricsBindAddress,
		},
		LeaderElection:                true,
		LeaderElectionID:              configapi.DefaultLeaderElectionID,
		LeaderElectionResourceLock:    resourcelock.LeasesResourceLock,
		LeaderElectionReleaseOnCancel: true,
		LeaseDuration:                 ptr.To(configapi.DefaultLeaderElectionLeaseDuration),
		RenewDeadline:                 ptr.To(configapi.DefaultLeaderElectionRenewDeadline),
		RetryPeriod:                   ptr.To(configapi.DefaultLeaderElectionRetryPeriod),
	}
}

func TestLoad(t *testing.T) {
	testScheme := runtime.NewScheme()
	err := configapi.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()

	namespaceOverWriteConfig := filepath.Join(tmpDir, "namespace-overwrite.yaml")
	if err := os.WriteFile(namespaceOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-tenant-a
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8443
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	emptyConfig := filepath.Join(tmpDir, "empty-config.yaml")
	if err := os.WriteFile(emptyConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	ctrlManagerConfigSpecOverWriteConfig := filepath.Join(tmpDir, "ctrl-manager-config-spec-overwrite.yaml")
	if err := os.WriteFile(ctrlManagerConfigSpecOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
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
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8443
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
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8443
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
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8443
leaderElection:
  leaderElect: false
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	waitForPodsReadyEnabledConfig := filepath.Join(tmpDir, "waitForPodsReady-enabled.yaml")
	if err := os.WriteFile(waitForPodsReadyEnabledConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
waitForPodsReady:
  timeout: 50s
  blockAdmission: true
  recoveryTimeout: 3m
  requeuingStrategy:
    timestamp: Creation
    backoffLimitCount: 10
    backoffBaseSeconds: 30
    backoffMaxSeconds: 1800
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	clientConnectionConfig := filepath.Join(tmpDir, "clientConnection.yaml")
	if err := os.WriteFile(clientConnectionConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8443
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
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
health:
  healthProbeBindAddress: :8081
  readinessEndpointName: ready
  livenessEndpointName: live
metrics:
  bindAddress: :8443
pprofBindAddress: :8083
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
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
integrations:
  frameworks:
  - batch/job
  externalFrameworks:
  - Foo.v1.example.com
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	multiKueueConfig := filepath.Join(tmpDir, "multiKueue.yaml")
	if err := os.WriteFile(multiKueueConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
multiKueue:
  gcInterval: 1m30s
  origin: multikueue-manager1
  workerLostTimeout: 10m
  dispatcherName: kueue.x-k8s.io/multikueue-dispatcher-incremental
  clusterProfile:
    credentialsProviders:
      - name: test-provider
        execConfig:
          command: /usr/bin/test-command
          apiVersion: client.authentication.k8s.io/v1
          interactiveMode: Never
          args:
            - arg1
            - arg2
          env:
            - name: TEST_ENV
              value: test-value
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	resourceTransformConfig := filepath.Join(tmpDir, "resourceXForm.yaml")
	if err := os.WriteFile(resourceTransformConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
resources:
  transformations:
  - input: nvidia.com/mig-1g.5gb
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 5Gi
      example.com/credits: 10
  - input: nvidia.com/mig-2g.10gb
    strategy: Replace
    outputs:
      example.com/accelerator-memory: 10Gi
      example.com/credits: 15
  - input: cpu
    strategy: Retain
    outputs:
      example.com/credits: 1
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	invalidConfig := filepath.Join(tmpDir, "invalid-config.yaml")
	if err := os.WriteFile(invalidConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespaces: kueue-system
invalidField: invalidValue
health:
  healthProbeBindAddress: :8081
metrics:
  bindAddress: :8443
leaderElection:
  leaderElect: true
  resourceName: c1f6bfd2.kueue.x-k8s.io
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	objectRetentionPoliciesConfig := filepath.Join(tmpDir, "objectRetentionPolicies.yaml")
	if err := os.WriteFile(objectRetentionPoliciesConfig, []byte(`apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
objectRetentionPolicies:
  workloads:
    afterFinished: 30m
    afterDeactivatedByKueue: 30m
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	enableDefaultInternalCertManagement := &configapi.InternalCertManagement{
		Enable:             ptr.To(true),
		WebhookServiceName: ptr.To(configapi.DefaultWebhookServiceName),
		WebhookSecretName:  ptr.To(configapi.DefaultWebhookSecretName),
	}

	ctrlOptsCmpOpts := cmp.Options{
		cmpopts.IgnoreUnexported(ctrl.Options{}, logr.Logger{}),
		cmpopts.IgnoreUnexported(webhook.DefaultServer{}),
		cmpopts.IgnoreUnexported(ctrlcache.Options{}),
		cmpopts.IgnoreUnexported(net.ListenConfig{}),
		cmpopts.IgnoreFields(ctrl.Options{}, "Scheme", "Logger"),
		cmpopts.IgnoreFields(webhook.Options{}, "TLSOpts"),
	}

	// Ignore the controller manager section since it's side effect is checked against
	// the content of  the resulting options
	configCmpOpts := cmp.Options{
		cmpopts.IgnoreFields(configapi.Configuration{}, "ControllerManager"),
	}

	defaultClientConnection := &configapi.ClientConnection{
		QPS:   ptr.To(configapi.DefaultClientConnectionQPS),
		Burst: ptr.To(configapi.DefaultClientConnectionBurst),
	}

	defaultIntegrations := &configapi.Integrations{
		Frameworks: []string{job.FrameworkName},
	}

	defaultManagedJobsNamespaceSelector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{
			{
				Key:      corev1.LabelMetadataName,
				Operator: metav1.LabelSelectorOpNotIn,
				Values:   []string{"kube-system", "kueue-system"},
			},
		},
	}

	defaultMultiKueue := &configapi.MultiKueue{
		GCInterval:        &metav1.Duration{Duration: configapi.DefaultMultiKueueGCInterval},
		Origin:            ptr.To(configapi.DefaultMultiKueueOrigin),
		WorkerLostTimeout: &metav1.Duration{Duration: configapi.DefaultMultiKueueWorkerLostTimeout},
		DispatcherName:    ptr.To(configapi.MultiKueueDispatcherModeAllAtOnce),
	}

	testcases := []struct {
		name                 string
		configFile           string
		enableClusterProfile bool
		withClusterProfile   bool
		wantConfiguration    configapi.Configuration
		wantOptions          ctrl.Options
		wantError            error
	}{
		{
			name:       "default config",
			configFile: "",
			wantConfiguration: configapi.Configuration{
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
		},
		{
			name:       "empty config",
			configFile: emptyConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
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
				Integrations: &configapi.Integrations{
					Frameworks: []string{job.FrameworkName},
				},
				MultiKueue: defaultMultiKueue,
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-tenant-a"},
						},
					},
				},
			},
			wantOptions: defaultControlOptions("kueue-tenant-a"),
		},
		{
			name:       "ControllerManagerConfigurationSpec overwrite config",
			configFile: ctrlManagerConfigSpecOverWriteConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName:   false,
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: ctrl.Options{
				Cache:                  defaultControlCacheOptions(configapi.DefaultNamespace),
				HealthProbeBindAddress: ":38081",
				Metrics: metricsserver.Options{
					BindAddress: ":38080",
				},
				LeaderElection:                true,
				LeaderElectionID:              "test-id",
				LeaderElectionResourceLock:    resourcelock.LeasesResourceLock,
				LeaderElectionReleaseOnCancel: true,
				LeaseDuration:                 ptr.To(configapi.DefaultLeaderElectionLeaseDuration),
				RenewDeadline:                 ptr.To(configapi.DefaultLeaderElectionRenewDeadline),
				RetryPeriod:                   ptr.To(configapi.DefaultLeaderElectionRetryPeriod),
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
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
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
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
		},
		{
			name:       "leaderElection disabled config",
			configFile: leaderElectionDisabledConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                    ptr.To("kueue-system"),
				ManageJobsWithoutQueueName:   false,
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: ctrl.Options{
				Cache:                  defaultControlCacheOptions("kueue-system"),
				HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
				Metrics: metricsserver.Options{
					BindAddress: configapi.DefaultMetricsBindAddress,
				},
				LeaderElectionID:              configapi.DefaultLeaderElectionID,
				LeaderElectionResourceLock:    resourcelock.LeasesResourceLock,
				LeaderElectionReleaseOnCancel: false,
				LeaseDuration:                 ptr.To(configapi.DefaultLeaderElectionLeaseDuration),
				RenewDeadline:                 ptr.To(configapi.DefaultLeaderElectionRenewDeadline),
				RetryPeriod:                   ptr.To(configapi.DefaultLeaderElectionRetryPeriod),
				LeaderElection:                false,
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
					BlockAdmission:  ptr.To(true),
					Timeout:         metav1.Duration{Duration: 50 * time.Second},
					RecoveryTimeout: &metav1.Duration{Duration: 3 * time.Minute},
					RequeuingStrategy: &configapi.RequeuingStrategy{
						Timestamp:          ptr.To(configapi.CreationTimestamp),
						BackoffLimitCount:  ptr.To[int32](10),
						BackoffBaseSeconds: ptr.To[int32](30),
						BackoffMaxSeconds:  ptr.To[int32](1800),
					},
				},
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
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
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
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
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: ctrl.Options{
				Cache:                  defaultControlCacheOptions(configapi.DefaultNamespace),
				HealthProbeBindAddress: configapi.DefaultHealthProbeBindAddress,
				ReadinessEndpointName:  "ready",
				LivenessEndpointName:   "live",
				Metrics: metricsserver.Options{
					BindAddress: configapi.DefaultMetricsBindAddress,
				},
				PprofBindAddress:              ":8083",
				LeaderElection:                true,
				LeaderElectionID:              configapi.DefaultLeaderElectionID,
				LeaderElectionNamespace:       "namespace",
				LeaderElectionResourceLock:    "lock",
				LeaderElectionReleaseOnCancel: true,
				LeaseDuration:                 ptr.To(time.Second * 100),
				RenewDeadline:                 ptr.To(time.Second * 15),
				RetryPeriod:                   ptr.To(time.Second * 30),
				Controller: runtimeconfig.Controller{
					GroupKindConcurrency: map[string]int{
						"workload": 5,
					},
					CacheSyncTimeout: 3,
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
					Frameworks:         []string{job.FrameworkName},
					ExternalFrameworks: []string{"Foo.v1.example.com"},
				},
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
		},

		{
			name:       "multiKueue config",
			configFile: multiKueueConfig,
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
				MultiKueue: &configapi.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: 90 * time.Second},
					Origin:            ptr.To("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: 10 * time.Minute},
					DispatcherName:    ptr.To(configapi.MultiKueueDispatcherModeIncremental),
					ClusterProfile: &configapi.ClusterProfile{
						CredentialsProviders: []configapi.ClusterProfileCredentialsProvider{
							{
								Name: "test-provider",
								ExecConfig: clientcmdapi.ExecConfig{
									Command:         "/usr/bin/test-command",
									APIVersion:      "client.authentication.k8s.io/v1",
									InteractiveMode: clientcmdapi.NeverExecInteractiveMode,
									Args:            []string{"arg1", "arg2"},
									Env: []clientcmdapi.ExecEnvVar{
										{Name: "TEST_ENV", Value: "test-value"},
									},
								},
							},
						},
					},
				},
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
		},
		{
			name:       "resourceTransform config",
			configFile: resourceTransformConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName:   false,
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				Resources: &configapi.Resources{
					Transformations: []configapi.ResourceTransformation{
						{
							Input:    corev1.ResourceName("nvidia.com/mig-1g.5gb"),
							Strategy: ptr.To(configapi.Replace),
							Outputs: corev1.ResourceList{
								corev1.ResourceName("example.com/accelerator-memory"): resourcev1.MustParse("5Gi"),
								corev1.ResourceName("example.com/credits"):            resourcev1.MustParse("10"),
							},
						},
						{
							Input:    corev1.ResourceName("nvidia.com/mig-2g.10gb"),
							Strategy: ptr.To(configapi.Replace),
							Outputs: corev1.ResourceList{
								corev1.ResourceName("example.com/accelerator-memory"): resourcev1.MustParse("10Gi"),
								corev1.ResourceName("example.com/credits"):            resourcev1.MustParse("15"),
							},
						},
						{
							Input:    corev1.ResourceCPU,
							Strategy: ptr.To(configapi.Retain),
							Outputs: corev1.ResourceList{
								corev1.ResourceName("example.com/credits"): resourcev1.MustParse("1"),
							},
						},
					},
				},
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
		},
		{
			name:       "invalid config",
			configFile: invalidConfig,
			wantError: runtime.NewStrictDecodingError([]error{
				errors.New("unknown field \"invalidField\""),
				errors.New("unknown field \"namespaces\""),
			}),
		},
		{
			name:       "objectRetentionPolicies config",
			configFile: objectRetentionPoliciesConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName:   false,
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterFinished:           &metav1.Duration{Duration: 30 * time.Minute},
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 30 * time.Minute},
					},
				},
			},
			wantOptions: defaultControlOptions(configapi.DefaultNamespace),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			options, cfg, err := Load(testScheme, tc.configFile)
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

func TestTLSOptionsFeatureGate(t *testing.T) {
	testScheme := runtime.NewScheme()
	err := configapi.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()

	tlsConfigWithCipherSuites := filepath.Join(tmpDir, "tls-with-ciphers.yaml")
	if err := os.WriteFile(tlsConfigWithCipherSuites, []byte(`apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
tls:
  minVersion: VersionTLS12
  cipherSuites:
    - TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256
    - TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	tlsConfigTLS13 := filepath.Join(tmpDir, "tls13.yaml")
	if err := os.WriteFile(tlsConfigTLS13, []byte(`apiVersion: config.kueue.x-k8s.io/v1beta2
kind: Configuration
namespace: kueue-system
tls:
  minVersion: VersionTLS13
webhook:
  port: 9443
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	testcases := []struct {
		name              string
		configFile        string
		featureGateValue  bool
		wantConfiguration configapi.Configuration
		verifyTLSApplied  bool
	}{
		{
			name:             "TLS config applied when feature gate enabled",
			configFile:       tlsConfigWithCipherSuites,
			featureGateValue: true,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To(configapi.DefaultWebhookServiceName),
					WebhookSecretName:  ptr.To(configapi.DefaultWebhookSecretName),
				},
				ClientConnection: &configapi.ClientConnection{
					QPS:   ptr.To(configapi.DefaultClientConnectionQPS),
					Burst: ptr.To(configapi.DefaultClientConnectionBurst),
				},
				Integrations: &configapi.Integrations{
					Frameworks: []string{job.FrameworkName},
				},
				MultiKueue: &configapi.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: configapi.DefaultMultiKueueGCInterval},
					Origin:            ptr.To(configapi.DefaultMultiKueueOrigin),
					WorkerLostTimeout: &metav1.Duration{Duration: configapi.DefaultMultiKueueWorkerLostTimeout},
					DispatcherName:    ptr.To(configapi.MultiKueueDispatcherModeAllAtOnce),
				},
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS12",
						CipherSuites: []string{
							"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
							"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
						},
					},
				},
			},
			verifyTLSApplied: true,
		},
		{
			name:             "TLS config NOT applied when feature gate disabled",
			configFile:       tlsConfigWithCipherSuites,
			featureGateValue: false,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To(configapi.DefaultWebhookServiceName),
					WebhookSecretName:  ptr.To(configapi.DefaultWebhookSecretName),
				},
				ClientConnection: &configapi.ClientConnection{
					QPS:   ptr.To(configapi.DefaultClientConnectionQPS),
					Burst: ptr.To(configapi.DefaultClientConnectionBurst),
				},
				Integrations: &configapi.Integrations{
					Frameworks: []string{job.FrameworkName},
				},
				MultiKueue: &configapi.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: configapi.DefaultMultiKueueGCInterval},
					Origin:            ptr.To(configapi.DefaultMultiKueueOrigin),
					WorkerLostTimeout: &metav1.Duration{Duration: configapi.DefaultMultiKueueWorkerLostTimeout},
					DispatcherName:    ptr.To(configapi.MultiKueueDispatcherModeAllAtOnce),
				},
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS12",
						CipherSuites: []string{
							"TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256",
							"TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384",
						},
					},
				},
			},
			verifyTLSApplied: false,
		},
		{
			name:             "TLS 1.3 config applied when feature gate enabled",
			configFile:       tlsConfigTLS13,
			featureGateValue: true,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To(configapi.DefaultWebhookServiceName),
					WebhookSecretName:  ptr.To(configapi.DefaultWebhookSecretName),
				},
				ClientConnection: &configapi.ClientConnection{
					QPS:   ptr.To(configapi.DefaultClientConnectionQPS),
					Burst: ptr.To(configapi.DefaultClientConnectionBurst),
				},
				Integrations: &configapi.Integrations{
					Frameworks: []string{job.FrameworkName},
				},
				MultiKueue: &configapi.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: configapi.DefaultMultiKueueGCInterval},
					Origin:            ptr.To(configapi.DefaultMultiKueueOrigin),
					WorkerLostTimeout: &metav1.Duration{Duration: configapi.DefaultMultiKueueWorkerLostTimeout},
					DispatcherName:    ptr.To(configapi.MultiKueueDispatcherModeAllAtOnce),
				},
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS13",
					},
				},
			},
			verifyTLSApplied: true,
		},
		{
			name:             "TLS 1.3 config NOT applied when feature gate disabled",
			configFile:       tlsConfigTLS13,
			featureGateValue: false,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement: &configapi.InternalCertManagement{
					Enable:             ptr.To(true),
					WebhookServiceName: ptr.To(configapi.DefaultWebhookServiceName),
					WebhookSecretName:  ptr.To(configapi.DefaultWebhookSecretName),
				},
				ClientConnection: &configapi.ClientConnection{
					QPS:   ptr.To(configapi.DefaultClientConnectionQPS),
					Burst: ptr.To(configapi.DefaultClientConnectionBurst),
				},
				Integrations: &configapi.Integrations{
					Frameworks: []string{job.FrameworkName},
				},
				MultiKueue: &configapi.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: configapi.DefaultMultiKueueGCInterval},
					Origin:            ptr.To(configapi.DefaultMultiKueueOrigin),
					WorkerLostTimeout: &metav1.Duration{Duration: configapi.DefaultMultiKueueWorkerLostTimeout},
					DispatcherName:    ptr.To(configapi.MultiKueueDispatcherModeAllAtOnce),
				},
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-system"},
						},
					},
				},
				ControllerManager: configapi.ControllerManager{
					TLS: &configapi.TLSOptions{
						MinVersion: "VersionTLS13",
					},
				},
			},
			verifyTLSApplied: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			// Set the feature gate for this test
			features.SetFeatureGateDuringTest(t, features.TLSOptions, tc.featureGateValue)

			options, cfg, err := Load(testScheme, tc.configFile)
			if err != nil {
				t.Fatalf("Unexpected error loading config: %v", err)
			}

			// Call AddWebhookSettingsTo to configure webhook server with TLS options
			AddWebhookSettingsTo(&options, &cfg)

			// Compare the loaded configuration
			configCmpOpts := cmp.Options{
				cmpopts.IgnoreFields(configapi.Configuration{}, "ControllerManager"),
			}
			if diff := cmp.Diff(tc.wantConfiguration, cfg, configCmpOpts...); diff != "" {
				t.Errorf("Unexpected config (-want +got):\n%s", diff)
			}

			// Verify webhook server was created
			if options.WebhookServer == nil {
				t.Fatal("Expected WebhookServer to be created, but it was nil")
			}

			// Verify TLS options application based on feature gate
			defaultServer, ok := options.WebhookServer.(*webhook.DefaultServer)
			if !ok {
				t.Fatalf("Expected WebhookServer to be *webhook.DefaultServer, got %T", options.WebhookServer)
			}

			// Check if TLSOpts are applied or not based on feature gate
			if tc.verifyTLSApplied {
				if len(defaultServer.Options.TLSOpts) == 0 {
					t.Error("Expected TLSOpts to be applied when feature gate is enabled, but got none")
				}
			} else {
				if len(defaultServer.Options.TLSOpts) > 0 {
					t.Errorf("Expected TLSOpts NOT to be applied when feature gate is disabled, but got %d options", len(defaultServer.Options.TLSOpts))
				}
			}
		})
	}
}

func TestEncode(t *testing.T) {
	testScheme := runtime.NewScheme()
	err := configapi.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	defaultConfig := &configapi.Configuration{}
	testScheme.Default(defaultConfig)

	testcases := []struct {
		name       string
		scheme     *runtime.Scheme
		cfg        *configapi.Configuration
		wantResult map[string]any
	}{

		{
			name:   "empty",
			scheme: testScheme,
			cfg:    &configapi.Configuration{},
			wantResult: map[string]any{
				"apiVersion":                 "config.kueue.x-k8s.io/v1beta2",
				"kind":                       "Configuration",
				"manageJobsWithoutQueueName": false,
				"health":                     map[string]any{},
				"metrics":                    map[string]any{},
				"webhook":                    map[string]any{},
			},
		},
		{
			name:   "default",
			scheme: testScheme,
			cfg:    defaultConfig,
			wantResult: map[string]any{
				"apiVersion": "config.kueue.x-k8s.io/v1beta2",
				"kind":       "Configuration",
				"namespace":  configapi.DefaultNamespace,
				"webhook": map[string]any{
					"port":    int64(configapi.DefaultWebhookPort),
					"certDir": configapi.DefaultWebhookCertDir,
				},
				"metrics": map[string]any{
					"bindAddress": configapi.DefaultMetricsBindAddress,
				},
				"health": map[string]any{
					"healthProbeBindAddress": configapi.DefaultHealthProbeBindAddress,
				},
				"leaderElection": map[string]any{
					"leaderElect":       true,
					"leaseDuration":     configapi.DefaultLeaderElectionLeaseDuration.String(),
					"renewDeadline":     configapi.DefaultLeaderElectionRenewDeadline.String(),
					"retryPeriod":       configapi.DefaultLeaderElectionRetryPeriod.String(),
					"resourceLock":      resourcelock.LeasesResourceLock,
					"resourceName":      configapi.DefaultLeaderElectionID,
					"resourceNamespace": "",
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
				"managedJobsNamespaceSelector": map[string]any{
					"matchExpressions": []any{map[string]any{
						"key":      corev1.LabelMetadataName,
						"operator": "NotIn",
						"values":   []any{"kube-system", "kueue-system"},
					}},
				},
				"integrations": map[string]any{
					"frameworks": []any{"batch/job"},
				},
				"multiKueue": map[string]any{
					"gcInterval":        "1m0s",
					"origin":            "multikueue",
					"workerLostTimeout": "15m0s",
					"dispatcherName":    configapi.MultiKueueDispatcherModeAllAtOnce,
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
			gotMap := map[string]any{}
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

func TestWaitForPodsReadyIsEnabled(t *testing.T) {
	cases := map[string]struct {
		cfg  *configapi.Configuration
		want bool
	}{
		"waitforpodsready.Enabled() is false": {
			cfg: &configapi.Configuration{},
		},
		"waitforpodsready.Enabled() is true": {
			cfg: &configapi.Configuration{
				WaitForPodsReady: &configapi.WaitForPodsReady{},
			},
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := waitforpodsready.Enabled(tc.cfg.WaitForPodsReady)
			if tc.want != got {
				t.Errorf("Unexpected result from waitforpodsready.Enabled()\nwant:\n%v\ngot:%v\n", tc.want, got)
			}
		})
	}
}

func TestConfigureClusterProfileCacheWithClient(t *testing.T) {
	multiclusterCRD := &apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{
			Name: "clusterprofiles.multicluster.x-k8s.io",
		},
	}

	testCases := map[string]struct {
		crdPresent       bool
		failedGetCRD     bool
		wantError        bool
		wantOptionsCache func(string) ctrlcache.Options
	}{
		"clusterProfile CRD not present": {
			wantOptionsCache: defaultControlCacheOptions,
		},
		"clusterProfile cache added to ByObject": {
			crdPresent: true,
			wantOptionsCache: func(namespace string) ctrlcache.Options {
				cOpts := defaultControlOptions(namespace)
				cOpts.Cache.ByObject[objectKeyClusterProfile] = ctrlcache.ByObject{
					Namespaces: map[string]ctrlcache.Config{
						namespace: {},
					},
				}
				return cOpts.Cache
			},
		},
		"error failed loading the ClusterProfile CRD": {
			crdPresent:   true,
			failedGetCRD: true,
			wantError:    true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			opts := &ctrl.Options{
				Cache: defaultControlCacheOptions(configapi.DefaultNamespace),
			}
			cfg := &configapi.Configuration{
				Namespace: ptr.To(configapi.DefaultNamespace),
			}

			var objects []runtime.Object
			if tc.crdPresent {
				objects = append(objects, multiclusterCRD)
			}

			fake := apiextensionsfake.NewClientset(objects...)
			fake.PrependReactor("get", "customresourcedefinitions", func(action clienttesting.Action) (handled bool, ret runtime.Object, err error) {
				getAction := action.(clienttesting.GetAction)
				if getAction.GetName() == multiclusterCRD.Name && tc.failedGetCRD {
					return true, nil, apierrors.NewBadRequest("testing error getting CRD")
				}
				return false, nil, nil
			})

			err := configureClusterProfileCacheWithClient(ctx, log, opts, fake, *cfg)
			if tc.wantError {
				if err == nil {
					t.Error("Expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error:%s", err)
				}
				if diff := cmp.Diff(tc.wantOptionsCache(configapi.DefaultNamespace), opts.Cache); diff != "" {
					t.Errorf("Unexpected options cache (-want +got):\n%s", diff)
				}
			}
		})
	}
}

func TestConfigureClusterProfileCache(t *testing.T) {
	testCases := map[string]struct {
		kubeConfig *rest.Config
	}{
		"error creating CRD client with empty kubeConfig": {
			kubeConfig: &rest.Config{},
		},
		"error creating CRD client with invalid kubeConfig": {
			kubeConfig: &rest.Config{Host: "http://invalid-host"},
		},
		"valid kubeConfig but no clusterProfile CRD": {
			kubeConfig: &rest.Config{
				Host:        "https://127.0.0.1:6443",
				BearerToken: "fake-token",
				TLSClientConfig: rest.TLSClientConfig{
					Insecure: true,
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			opts := &ctrl.Options{
				Cache: ctrlcache.Options{},
			}
			cfg := configapi.Configuration{Namespace: ptr.To(configapi.DefaultNamespace)}
			err := ConfigureClusterProfileCache(ctx, log, opts, tc.kubeConfig, cfg)

			if err == nil {
				t.Error("Expected error but got none")
			}
		})
	}
}
