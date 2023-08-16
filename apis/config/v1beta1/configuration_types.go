/*
Copyright 2022 The Kubernetes Authors.

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

package v1beta1

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	configv1alpha1 "k8s.io/component-base/config/v1alpha1"
)

//+kubebuilder:object:root=true

// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
	metav1.TypeMeta `json:",inline"`

	// Namespace is the namespace in which kueue is deployed. It is used as part of DNSName of the webhook Service.
	// If not set, the value is set from the file /var/run/secrets/kubernetes.io/serviceaccount/namespace
	// If the file doesn't exist, default value is kueue-system.
	Namespace *string `json:"namespace,omitempty"`

	// ControllerManager returns the configurations for controllers
	ControllerManager `json:",inline"`

	// ManageJobsWithoutQueueName controls whether or not Kueue reconciles
	// batch/v1.Jobs that don't set the annotation kueue.x-k8s.io/queue-name.
	// If set to true, then those jobs will be suspended and never started unless
	// they are assigned a queue and eventually admitted. This also applies to
	// jobs created before starting the kueue controller.
	// Defaults to false; therefore, those jobs are not managed and if they are created
	// unsuspended, they will start immediately.
	ManageJobsWithoutQueueName bool `json:"manageJobsWithoutQueueName"`

	// InternalCertManagement is configuration for internalCertManagement
	InternalCertManagement *InternalCertManagement `json:"internalCertManagement,omitempty"`

	// WaitForPodsReady is configuration to provide simple all-or-nothing
	// scheduling semantics for jobs to ensure they get resources assigned.
	// This is achieved by blocking the start of new jobs until the previously
	// started job has all pods running (ready).
	WaitForPodsReady *WaitForPodsReady `json:"waitForPodsReady,omitempty"`

	// ClientConnection provides additional configuration options for Kubernetes
	// API server client.
	ClientConnection *ClientConnection `json:"clientConnection,omitempty"`

	// Integrations provide configuration options for AI/ML/Batch frameworks
	// integrations (including K8S job).
	Integrations *Integrations `json:"integrations,omitempty"`

	// QueueVisibility is configuration to expose the information about the top
	// pending workloads.
	QueueVisibility *QueueVisibility `json:"queueVisibility,omitempty"`
}

type ControllerManager struct {
	// Webhook contains the controllers webhook configuration
	// +optional
	Webhook ControllerWebhook `json:"webhook,omitempty"`

	// LeaderElection is the LeaderElection config to be used when configuring
	// the manager.Manager leader election
	// +optional
	LeaderElection *configv1alpha1.LeaderElectionConfiguration `json:"leaderElection,omitempty"`

	// Metrics contains the controller metrics configuration
	// +optional
	Metrics ControllerMetrics `json:"metrics,omitempty"`

	// Health contains the controller health configuration
	// +optional
	Health ControllerHealth `json:"health,omitempty"`

	// PprofBindAddress is the TCP address that the controller should bind to
	// for serving pprof.
	// It can be set to "" or "0" to disable the pprof serving.
	// Since pprof may contain sensitive information, make sure to protect it
	// before exposing it to public.
	// +optional
	PprofBindAddress string `json:"pprofBindAddress,omitempty"`

	// Controller contains global configuration options for controllers
	// registered within this manager.
	// +optional
	Controller *ControllerConfigurationSpec `json:"controller,omitempty"`
}

// ControllerWebhook defines the webhook server for the controller.
type ControllerWebhook struct {
	// Port is the port that the webhook server serves at.
	// It is used to set webhook.Server.Port.
	// +optional
	Port *int `json:"port,omitempty"`

	// Host is the hostname that the webhook server binds to.
	// It is used to set webhook.Server.Host.
	// +optional
	Host string `json:"host,omitempty"`

	// CertDir is the directory that contains the server key and certificate.
	// if not set, webhook server would look up the server key and certificate in
	// {TempDir}/k8s-webhook-server/serving-certs. The server key and certificate
	// must be named tls.key and tls.crt, respectively.
	// +optional
	CertDir string `json:"certDir,omitempty"`
}

// ControllerMetrics defines the metrics configs.
type ControllerMetrics struct {
	// BindAddress is the TCP address that the controller should bind to
	// for serving prometheus metrics.
	// It can be set to "0" to disable the metrics serving.
	// +optional
	BindAddress string `json:"bindAddress,omitempty"`

	// EnableClusterQueueResources, if true the cluster queue resource usage and quotas
	// metrics will be reported.
	// +optional
	EnableClusterQueueResources bool `json:"enableClusterQueueResources,omitempty"`
}

// ControllerHealth defines the health configs.
type ControllerHealth struct {
	// HealthProbeBindAddress is the TCP address that the controller should bind to
	// for serving health probes
	// It can be set to "0" or "" to disable serving the health probe.
	// +optional
	HealthProbeBindAddress string `json:"healthProbeBindAddress,omitempty"`

	// ReadinessEndpointName, defaults to "readyz"
	// +optional
	ReadinessEndpointName string `json:"readinessEndpointName,omitempty"`

	// LivenessEndpointName, defaults to "healthz"
	// +optional
	LivenessEndpointName string `json:"livenessEndpointName,omitempty"`
}

// ControllerConfigurationSpec defines the global configuration for
// controllers registered with the manager.
type ControllerConfigurationSpec struct {
	// GroupKindConcurrency is a map from a Kind to the number of concurrent reconciliation
	// allowed for that controller.
	//
	// When a controller is registered within this manager using the builder utilities,
	// users have to specify the type the controller reconciles in the For(...) call.
	// If the object's kind passed matches one of the keys in this map, the concurrency
	// for that controller is set to the number specified.
	//
	// The key is expected to be consistent in form with GroupKind.String(),
	// e.g. ReplicaSet in apps group (regardless of version) would be `ReplicaSet.apps`.
	//
	// +optional
	GroupKindConcurrency map[string]int `json:"groupKindConcurrency,omitempty"`

	// CacheSyncTimeout refers to the time limit set to wait for syncing caches.
	// Defaults to 2 minutes if not set.
	// +optional
	CacheSyncTimeout *time.Duration `json:"cacheSyncTimeout,omitempty"`
}

type WaitForPodsReady struct {
	// Enable when true, indicates that each admitted workload
	// blocks the admission of all other workloads from all queues until it is in the
	// `PodsReady` condition. If false, all workloads start as soon as they are
	// admitted and do not block admission of other workloads. The PodsReady
	// condition is only added if this setting is enabled. It defaults to false.
	Enable bool `json:"enable,omitempty"`

	// Timeout defines the time for an admitted workload to reach the
	// PodsReady=true condition. When the timeout is reached, the workload admission
	// is cancelled and requeued in the same cluster queue. Defaults to 5min.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// BlockAdmission when true, cluster queue will block admissions for all subsequent jobs
	// until the jobs reach the PodsReady=true condition. It defaults to false if Enable is false
	// and defaults to true otherwise.
	BlockAdmission *bool `json:"blockAdmission,omitempty"`
}

type InternalCertManagement struct {

	// Enable controls whether to enable internal cert management or not.
	// Defaults to true. If you want to use a third-party management, e.g. cert-manager,
	// set it to false. See the user guide for more information.
	Enable *bool `json:"enable,omitempty"`

	// WebhookServiceName is the name of the Service used as part of the DNSName.
	// Defaults to kueue-webhook-service.
	WebhookServiceName *string `json:"webhookServiceName,omitempty"`

	// WebhookSecretName is the name of the Secret used to store CA and server certs.
	// Defaults to kueue-webhook-server-cert.
	WebhookSecretName *string `json:"webhookSecretName,omitempty"`
}

type ClientConnection struct {
	// QPS controls the number of queries per second allowed for K8S api server
	// connection.
	QPS *float32 `json:"qps,omitempty"`

	// Burst allows extra queries to accumulate when a client is exceeding its rate.
	Burst *int32 `json:"burst,omitempty"`
}

type Integrations struct {
	// List of framework names to be enabled.
	// Possible options:
	//  - "batch/job"
	//  - "kubeflow.org/mpijob"
	//  - "ray.io/rayjob"
	//  - "jobset.x-k8s.io/jobset"
	//  - "kubeflow.org/pytorchjob"
	//  - "kubeflow.org/tfjob"
	Frameworks []string `json:"frameworks,omitempty"`
}

type QueueVisibility struct {
	// LocalQueues is configuration to expose the information
	// about the top pending workloads in the local queue.
	LocalQueues *LocalQueueVisibility `json:"localQueues,omitempty"`

	// ClusterQueues is configuration to expose the information
	// about the top pending workloads in the cluster queue.
	ClusterQueues *ClusterQueueVisibility `json:"clusterQueues,omitempty"`

	// UpdateInterval specifies the time interval for updates to the structure
	// of the top pending workloads in the queues.
	// Defaults to 5s.
	// +optional
	UpdateInterval *metav1.Duration `json:"updateInterval,omitempty"`
}

type LocalQueueVisibility struct {
	// MaxCount indicates the maximal number of pending workloads exposed in the
	// local queue status. When the value is set to 0, then LocalQueue visibility
	// updates are disabled.
	// The maximal value is 4000.
	// Defaults to 10.
	MaxCount int32 `json:"maxCount,omitempty"`

	// MaxPosition indicates the maximal position of the workload in the cluster
	// queue returned in the head.
	MaxPosition *int32 `json:"maxPosition,omitempty"`
}

type ClusterQueueVisibility struct {
	// MaxCount indicates the maximal number of pending workloads exposed in the
	// cluster queue status.  When the value is set to 0, then LocalQueue
	// visibility updates are disabled.
	// The maximal value is 4000.
	// Defaults to 10.
	MaxCount int32 `json:"maxCount,omitempty"`
}
