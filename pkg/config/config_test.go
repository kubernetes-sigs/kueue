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
	resourcev1 "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	runtimeconfig "sigs.k8s.io/controller-runtime/pkg/config"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"

	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

func TestLoad(t *testing.T) {
	testScheme := runtime.NewScheme()
	err := configapi.AddToScheme(testScheme)
	if err != nil {
		t.Fatal(err)
	}

	tmpDir := t.TempDir()

	namespaceOverWriteConfig := filepath.Join(tmpDir, "namespace-overwrite.yaml")
	if err := os.WriteFile(namespaceOverWriteConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
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
apiVersion: config.kueue.x-k8s.io/v1beta1
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
apiVersion: config.kueue.x-k8s.io/v1beta1
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
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
waitForPodsReady:
  enable: true
  timeout: 50s
  blockAdmission: false
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
apiVersion: config.kueue.x-k8s.io/v1beta1
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
apiVersion: config.kueue.x-k8s.io/v1beta1
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
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
integrations:
  frameworks:
  - batch/job
  externalFrameworks:
  - Foo.v1.example.com
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	queueVisibilityConfig := filepath.Join(tmpDir, "queueVisibility.yaml")
	if err := os.WriteFile(queueVisibilityConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
queueVisibility:
  updateIntervalSeconds: 10
  clusterQueues:
    maxCount: 0
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	podIntegrationOptionsConfig := filepath.Join(tmpDir, "podIntegrationOptions.yaml")
	if err := os.WriteFile(podIntegrationOptionsConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
integrations:
  frameworks:
  - pod
  podOptions:
    namespaceSelector:
      matchExpressions:
      - key: kubernetes.io/metadata.name
        operator: NotIn
        values: [ kube-system, kueue-system, prohibited-namespace ]
    podSelector:
      matchExpressions:
      - key: kueue-job
        operator: In
        values: [ "true", "True", "yes" ]
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	multiKueueConfig := filepath.Join(tmpDir, "multiKueue.yaml")
	if err := os.WriteFile(multiKueueConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
multiKueue:
  gcInterval: 1m30s
  origin: multikueue-manager1
  workerLostTimeout: 10m
  dispatcherName: kueue.x-k8s.io/multikueue-dispatcher-incremental
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	resourceTransformConfig := filepath.Join(tmpDir, "resourceXForm.yaml")
	if err := os.WriteFile(resourceTransformConfig, []byte(`
apiVersion: config.kueue.x-k8s.io/v1beta1
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
apiVersion: config.kueue.x-k8s.io/v1beta1
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
	if err := os.WriteFile(objectRetentionPoliciesConfig, []byte(`apiVersion: config.kueue.x-k8s.io/v1beta1
kind: Configuration
namespace: kueue-system
objectRetentionPolicies:
  workloads: 
    afterFinished: 30m
    afterDeactivatedByKueue: 30m
`), os.FileMode(0600)); err != nil {
		t.Fatal(err)
	}

	defaultControlOptions := ctrl.Options{
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
		WebhookServer: &webhook.DefaultServer{
			Options: webhook.Options{
				Port:    configapi.DefaultWebhookPort,
				CertDir: configapi.DefaultWebhookCertDir,
			},
		},
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
	}

	// Ignore the controller manager section since it's side effect is checked against
	// the content of  the resulting options
	configCmpOpts := cmp.Options{
		cmpopts.IgnoreFields(configapi.Configuration{}, "ControllerManager"),
	}

	defaultClientConnection := &configapi.ClientConnection{
		QPS:   ptr.To[float32](configapi.DefaultClientConnectionQPS),
		Burst: ptr.To[int32](configapi.DefaultClientConnectionBurst),
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

	defaultQueueVisibility := &configapi.QueueVisibility{
		UpdateIntervalSeconds: configapi.DefaultQueueVisibilityUpdateIntervalSeconds,
		ClusterQueues: &configapi.ClusterQueueVisibility{
			MaxCount: 10,
		},
	}

	defaultMultiKueue := &configapi.MultiKueue{
		GCInterval:        &metav1.Duration{Duration: configapi.DefaultMultiKueueGCInterval},
		Origin:            ptr.To(configapi.DefaultMultiKueueOrigin),
		WorkerLostTimeout: &metav1.Duration{Duration: configapi.DefaultMultiKueueWorkerLostTimeout},
		DispatcherName:    ptr.To[string](configapi.MultiKueueDispatcherModeAllAtOnce),
	}

	defaultWaitForPodsReady := &configapi.WaitForPodsReady{}

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
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						CertDir: configapi.DefaultWebhookCertDir,
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
				Integrations: &configapi.Integrations{
					Frameworks: []string{job.FrameworkName},
				},
				QueueVisibility: defaultQueueVisibility,
				MultiKueue:      defaultMultiKueue,
				ManagedJobsNamespaceSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      corev1.LabelMetadataName,
							Operator: metav1.LabelSelectorOpNotIn,
							Values:   []string{"kube-system", "kueue-tenant-a"},
						},
					},
				},
				WaitForPodsReady: defaultWaitForPodsReady,
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
				Namespace:                    ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName:   false,
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    9444,
						CertDir: configapi.DefaultWebhookCertDir,
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
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
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
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
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
				Namespace:                    ptr.To("kueue-system"),
				ManageJobsWithoutQueueName:   false,
				InternalCertManagement:       enableDefaultInternalCertManagement,
				ClientConnection:             defaultClientConnection,
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						CertDir: configapi.DefaultWebhookCertDir,
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
					Enable:          true,
					BlockAdmission:  ptr.To(false),
					Timeout:         &metav1.Duration{Duration: 50 * time.Second},
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
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						CertDir: configapi.DefaultWebhookCertDir,
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
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
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
				Integrations:                 defaultIntegrations,
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
					Frameworks:         []string{job.FrameworkName},
					ExternalFrameworks: []string{"Foo.v1.example.com"},
				},
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						CertDir: configapi.DefaultWebhookCertDir,
					},
				},
			},
		},
		{
			name:       "queue visibility config",
			configFile: queueVisibilityConfig,
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
				QueueVisibility: &configapi.QueueVisibility{
					UpdateIntervalSeconds: 10,
					ClusterQueues: &configapi.ClusterQueueVisibility{
						MaxCount: 0,
					},
				},
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						CertDir: configapi.DefaultWebhookCertDir,
					},
				},
			},
		},
		{
			name:       "pod integration options config",
			configFile: podIntegrationOptionsConfig,
			wantConfiguration: configapi.Configuration{
				TypeMeta: metav1.TypeMeta{
					APIVersion: configapi.GroupVersion.String(),
					Kind:       "Configuration",
				},
				Namespace:                  ptr.To(configapi.DefaultNamespace),
				ManageJobsWithoutQueueName: false,
				InternalCertManagement:     enableDefaultInternalCertManagement,
				ClientConnection:           defaultClientConnection,
				QueueVisibility:            defaultQueueVisibility,
				Integrations: &configapi.Integrations{
					Frameworks: []string{
						"pod",
					},
					PodOptions: &configapi.PodIntegrationOptions{
						NamespaceSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      corev1.LabelMetadataName,
									Operator: metav1.LabelSelectorOpNotIn,
									Values:   []string{"kube-system", "kueue-system", "prohibited-namespace"},
								},
							},
						},
						PodSelector: &metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "kueue-job",
									Operator: metav1.LabelSelectorOpIn,
									Values:   []string{"true", "True", "yes"},
								},
							},
						},
					},
				},
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: ctrl.Options{
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
				WebhookServer: &webhook.DefaultServer{
					Options: webhook.Options{
						Port:    configapi.DefaultWebhookPort,
						CertDir: configapi.DefaultWebhookCertDir,
					},
				},
			},
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
				QueueVisibility:            defaultQueueVisibility,
				MultiKueue: &configapi.MultiKueue{
					GCInterval:        &metav1.Duration{Duration: 90 * time.Second},
					Origin:            ptr.To("multikueue-manager1"),
					WorkerLostTimeout: &metav1.Duration{Duration: 10 * time.Minute},
					DispatcherName:    ptr.To[string](configapi.MultiKueueDispatcherModeIncremental),
				},
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				WaitForPodsReady:             defaultWaitForPodsReady,
			},
			wantOptions: defaultControlOptions,
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
				QueueVisibility:              defaultQueueVisibility,
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
				WaitForPodsReady: defaultWaitForPodsReady,
			},
			wantOptions: defaultControlOptions,
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
				QueueVisibility:              defaultQueueVisibility,
				MultiKueue:                   defaultMultiKueue,
				ManagedJobsNamespaceSelector: defaultManagedJobsNamespaceSelector,
				ObjectRetentionPolicies: &configapi.ObjectRetentionPolicies{
					Workloads: &configapi.WorkloadRetentionPolicy{
						AfterFinished:           &metav1.Duration{Duration: 30 * time.Minute},
						AfterDeactivatedByKueue: &metav1.Duration{Duration: 30 * time.Minute},
					},
				},
				WaitForPodsReady: defaultWaitForPodsReady,
			},
			wantOptions: defaultControlOptions,
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
			scheme: testScheme,
			cfg:    defaultConfig,
			wantResult: map[string]any{
				"apiVersion": "config.kueue.x-k8s.io/v1beta1",
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
				"queueVisibility": map[string]any{
					"updateIntervalSeconds": int64(configapi.DefaultQueueVisibilityUpdateIntervalSeconds),
					"clusterQueues":         map[string]any{"maxCount": int64(10)},
				},
				"multiKueue": map[string]any{
					"gcInterval":        "1m0s",
					"origin":            "multikueue",
					"workerLostTimeout": "15m0s",
					"dispatcherName":    string(configapi.MultiKueueDispatcherModeAllAtOnce),
				},
				"waitForPodsReady": map[string]any{},
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
		"cfg.WaitForPodsReadyIsEnabled.enable is false": {
			cfg: &configapi.Configuration{
				WaitForPodsReady: &configapi.WaitForPodsReady{},
			},
		},
		"waitForPodsReady is true": {
			cfg: &configapi.Configuration{
				WaitForPodsReady: &configapi.WaitForPodsReady{
					Enable: true,
				},
			},
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := WaitForPodsReadyIsEnabled(tc.cfg)
			if tc.want != got {
				t.Errorf("Unexpected result from WaitForPodsReadyIsEnabled\nwant:\n%v\ngot:%v\n", tc.want, got)
			}
		})
	}
}
