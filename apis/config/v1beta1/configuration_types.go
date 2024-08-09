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

// +k8s:defaulter-gen=true
// +kubebuilder:object:root=true

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
	// jobs that don't set the annotation kueue.x-k8s.io/queue-name.
	// If set to true, then those jobs will be suspended and never started unless
	// they are assigned a queue and eventually admitted. This also applies to
	// jobs created before starting the kueue controller.
	// Defaults to false; therefore, those jobs are not managed and if they are created
	// unsuspended, they will start immediately.
	ManageJobsWithoutQueueName bool `json:"manageJobsWithoutQueueName"`

	// InternalCertManagement is configuration for internalCertManagement
	InternalCertManagement *InternalCertManagement `json:"internalCertManagement,omitempty"`

	// WaitForPodsReady is configuration to provide a time-based all-or-nothing
	// scheduling semantics for Jobs, by ensuring all pods are ready (running
	// and passing the readiness probe) within the specified time. If the timeout
	// is exceeded, then the workload is evicted.
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

	// MultiKueue controls the behaviour of the MultiKueue AdmissionCheck Controller.
	MultiKueue *MultiKueue `json:"multiKueue,omitempty"`

	// FairSharing controls the fair sharing semantics across the cluster.
	FairSharing *FairSharing `json:"fairSharing,omitempty"`

	// Resources provides additional configuration options for handling the resources.
	Resources *Resources `json:"resources,omitempty"`

	// ObjectRetentionPolicies provides configuration options for retention of Kueue owned
	// objects. A nil value will disable automatic deletion for all objects.
	ObjectRetentionPolicies *ObjectRetentionPolicies `json:"objectRetentionPolicies,omitempty"`
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

// WaitForPodsReady defines configuration for the Wait For Pods Ready feature,
// which is used to ensure that all Pods are ready within the specified time.
type WaitForPodsReady struct {
	// Enable indicates whether to enable wait for pods ready feature.
	// Defaults to false.
	Enable bool `json:"enable,omitempty"`

	// Timeout defines the time for an admitted workload to reach the
	// PodsReady=true condition. When the timeout is exceeded, the workload
	// evicted and requeued in the same cluster queue.
	// Defaults to 5min.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// BlockAdmission when true, cluster queue will block admissions for all
	// subsequent jobs until the jobs reach the PodsReady=true condition.
	// This setting is only honored when `Enable` is set to true.
	BlockAdmission *bool `json:"blockAdmission,omitempty"`

	// RequeuingStrategy defines the strategy for requeuing a Workload.
	// +optional
	RequeuingStrategy *RequeuingStrategy `json:"requeuingStrategy,omitempty"`
}

type MultiKueue struct {
	// GCInterval defines the time interval between two consecutive garbage collection runs.
	// Defaults to 1min. If 0, the garbage collection is disabled.
	// +optional
	GCInterval *metav1.Duration `json:"gcInterval"`

	// Origin defines a label value used to track the creator of workloads in the worker
	// clusters.
	// This is used by multikueue in components like its garbage collector to identify
	// remote objects that ware created by this multikueue manager cluster and delete
	// them if their local counterpart no longer exists.
	// +optional
	Origin *string `json:"origin,omitempty"`

	// WorkerLostTimeout defines the time a local workload's multikueue admission check state is kept Ready
	// if the connection with its reserving worker cluster is lost.
	//
	// Defaults to 15 minutes.
	// +optional
	WorkerLostTimeout *metav1.Duration `json:"workerLostTimeout,omitempty"`
}

type RequeuingStrategy struct {
	// Timestamp defines the timestamp used for re-queuing a Workload
	// that was evicted due to Pod readiness. The possible values are:
	//
	// - `Eviction` (default) indicates from Workload `Evicted` condition with `PodsReadyTimeout` reason.
	// - `Creation` indicates from Workload .metadata.creationTimestamp.
	//
	// +optional
	Timestamp *RequeuingTimestamp `json:"timestamp,omitempty"`

	// BackoffLimitCount defines the maximum number of re-queuing retries.
	// Once the number is reached, the workload is deactivated (`.spec.activate`=`false`).
	// When it is null, the workloads will repeatedly and endless re-queueing.
	//
	// Every backoff duration is about "b*2^(n-1)+Rand" where:
	// - "b" represents the base set by "BackoffBaseSeconds" parameter,
	// - "n" represents the "workloadStatus.requeueState.count",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and
	// other workloads will have a chance to be admitted.
	// By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).
	//
	// Defaults to null.
	// +optional
	BackoffLimitCount *int32 `json:"backoffLimitCount,omitempty"`

	// BackoffBaseSeconds defines the base for the exponential backoff for
	// re-queuing an evicted workload.
	//
	// Defaults to 60.
	// +optional
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`

	// BackoffMaxSeconds defines the maximum backoff time to re-queue an evicted workload.
	//
	// Defaults to 3600.
	// +optional
	BackoffMaxSeconds *int32 `json:"backoffMaxSeconds,omitempty"`
}

type RequeuingTimestamp string

const (
	// CreationTimestamp timestamp (from Workload .metadata.creationTimestamp).
	CreationTimestamp RequeuingTimestamp = "Creation"

	// EvictionTimestamp timestamp (from Workload .status.conditions).
	EvictionTimestamp RequeuingTimestamp = "Eviction"
)

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
	//  - "ray.io/raycluster"
	//  - "jobset.x-k8s.io/jobset"
	//  - "kubeflow.org/mxjob"
	//  - "kubeflow.org/paddlejob"
	//  - "kubeflow.org/pytorchjob"
	//  - "kubeflow.org/tfjob"
	//  - "kubeflow.org/xgboostjob"
	//  - "pod"
	Frameworks []string `json:"frameworks,omitempty"`
	// List of GroupVersionKinds that are managed for Kueue by external controllers;
	// the expected format is `Kind.version.group.com`.
	ExternalFrameworks []string `json:"externalFrameworks,omitempty"`
	// PodOptions defines kueue controller behaviour for pod objects
	PodOptions *PodIntegrationOptions `json:"podOptions,omitempty"`

	// labelKeysToCopy is a list of label keys that should be copied from the job into the
	// workload object. It is not required for the job to have all the labels from this
	// list. If a job does not have some label with the given key from this list, the
	// constructed workload object will be created without this label. In the case
	// of creating a workload from a composable job (pod group), if multiple objects
	// have labels with some key from the list, the values of these labels must
	// match or otherwise the workload creation would fail. The labels are copied only
	// during the workload creation and are not updated even if the labels of the
	// underlying job are changed.
	LabelKeysToCopy []string `json:"labelKeysToCopy,omitempty"`
}

type PodIntegrationOptions struct {
	// NamespaceSelector can be used to omit some namespaces from pod reconciliation
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`
	// PodSelector can be used to choose what pods to reconcile
	PodSelector *metav1.LabelSelector `json:"podSelector,omitempty"`
}

type QueueVisibility struct {
	// ClusterQueues is configuration to expose the information
	// about the top pending workloads in the cluster queue.
	ClusterQueues *ClusterQueueVisibility `json:"clusterQueues,omitempty"`

	// UpdateIntervalSeconds specifies the time interval for updates to the structure
	// of the top pending workloads in the queues.
	// The minimum value is 1.
	// Defaults to 5.
	UpdateIntervalSeconds int32 `json:"updateIntervalSeconds,omitempty"`
}

type ClusterQueueVisibility struct {
	// MaxCount indicates the maximal number of pending workloads exposed in the
	// cluster queue status.  When the value is set to 0, then ClusterQueue
	// visibility updates are disabled.
	// The maximal value is 4000.
	// Defaults to 10.
	MaxCount int32 `json:"maxCount,omitempty"`
}

type Resources struct {
	// ExcludedResourcePrefixes defines which resources should be ignored by Kueue
	ExcludeResourcePrefixes []string `json:"excludeResourcePrefixes,omitempty"`
}

type PreemptionStrategy string

const (
	LessThanOrEqualToFinalShare PreemptionStrategy = "LessThanOrEqualToFinalShare"
	LessThanInitialShare        PreemptionStrategy = "LessThanInitialShare"
)

type FairSharing struct {
	// enable indicates whether to enable fair sharing for all cohorts.
	// Defaults to false.
	Enable bool `json:"enable"`

	// preemptionStrategies indicates which constraints should a preemption satisfy.
	// The preemption algorithm will only use the next strategy in the list if the
	// incoming workload (preemptor) doesn't fit after using the previous strategies.
	// Possible values are:
	// - LessThanOrEqualToFinalShare: Only preempt a workload if the share of the preemptor CQ
	//   with the preemptor workload is less than or equal to the share of the preemptee CQ
	//   without the workload to be preempted.
	//   This strategy might favor preemption of smaller workloads in the preemptee CQ,
	//   regardless of priority or start time, in an effort to keep the share of the CQ
	//   as high as possible.
	// - LessThanInitialShare: Only preempt a workload if the share of the preemptor CQ
	//   with the incoming workload is strictly less than the share of the preemptee CQ.
	//   This strategy doesn't depend on the share usage of the workload being preempted.
	//   As a result, the strategy chooses to preempt workloads with the lowest priority and
	//   newest start time first.
	// The default strategy is ["LessThanOrEqualToFinalShare", "LessThanInitialShare"].
	PreemptionStrategies []PreemptionStrategy `json:"preemptionStrategies,omitempty"`
}

type ObjectRetentionPolicies struct {
	// FinishedWorkloadRetention is the duration to retain finished Workloads.
	// A duration of 0 will delete finished Workloads immediately.
	// A nil value will disable automatic deletion.
	// The value is represented using the metav1.Duration format, allowing for flexible
	// specification of time units (e.g., "24h", "1h30m", "30s").
	//
	// Defaults to null.
	// +optional
	FinishedWorkloadRetention *metav1.Duration `json:"finishedWorkloadRetention"`
}
