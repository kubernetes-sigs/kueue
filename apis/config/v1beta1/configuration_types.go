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

package v1beta1

import (
	"time"

	corev1 "k8s.io/api/core/v1"
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
	// jobs that don't set the label kueue.x-k8s.io/queue-name.
	// If set to true, then those jobs will be suspended and never started unless
	// they are assigned a queue and eventually admitted. This also applies to
	// jobs created before starting the kueue controller.
	// Defaults to false; therefore, those jobs are not managed and if they are created
	// unsuspended, they will start immediately.
	ManageJobsWithoutQueueName bool `json:"manageJobsWithoutQueueName"`

	// ManagedJobsNamespaceSelector provides a namespace-based mechanism to exempt jobs
	// from management by Kueue.
	//
	// It provides a strong exemption for the Pod-based integrations (pod, deployment, statefulset, etc.),
	// For Pod-based integrations, only jobs whose namespaces match ManagedJobsNamespaceSelector are
	// eligible to be managed by Kueue.  Pods, deployments, etc. in non-matching namespaces will
	// never be managed by Kueue, even if they have a kueue.x-k8s.io/queue-name label.
	// This strong exemption ensures that Kueue will not interfere with the basic operation
	// of system namespace.
	//
	// For all other integrations, ManagedJobsNamespaceSelector provides a weaker exemption
	// by only modulating the effects of ManageJobsWithoutQueueName.  For these integrations,
	// a job that has a kueue.x-k8s.io/queue-name label will always be managed by Kueue. Jobs without
	// a kueue.x-k8s.io/queue-name label will be managed by Kueue only when ManageJobsWithoutQueueName is
	// true and the job's namespace matches ManagedJobsNamespaceSelector.
	ManagedJobsNamespaceSelector *metav1.LabelSelector `json:"managedJobsNamespaceSelector,omitempty"`

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
	// Deprecated: This field will be removed on v1beta2, use VisibilityOnDemand
	// (https://kueue.sigs.k8s.io/docs/tasks/manage/monitor_pending_workloads/pending_workloads_on_demand/)
	// instead.
	QueueVisibility *QueueVisibility `json:"queueVisibility,omitempty"`

	// MultiKueue controls the behaviour of the MultiKueue AdmissionCheck Controller.
	MultiKueue *MultiKueue `json:"multiKueue,omitempty"`

	// FairSharing controls the Fair Sharing semantics across the cluster.
	FairSharing *FairSharing `json:"fairSharing,omitempty"`

	// admissionFairSharing indicates configuration of FairSharing with the `AdmissionTime` mode on
	AdmissionFairSharing *AdmissionFairSharing `json:"admissionFairSharing,omitempty"`

	// Resources provides additional configuration options for handling the resources.
	Resources *Resources `json:"resources,omitempty"`

	// FeatureGates is a map of feature names to bools that allows to override the
	// default enablement status of a feature. The map cannot be used in conjunction
	// with passing the list of features via the command line argument "--feature-gates"
	// for the Kueue Deployment.
	FeatureGates map[string]bool `json:"featureGates,omitempty"`

	// ObjectRetentionPolicies provides configuration options for automatic deletion
	// of Kueue-managed objects. A nil value disables all automatic deletions.
	// +optional
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

	// RecoveryTimeout defines an opt-in timeout, measured since the
	// last transition to the PodsReady=false condition after a Workload is Admitted and running.
	// Such a transition may happen when a Pod failed and the replacement Pod
	// is awaited to be scheduled.
	// After exceeding the timeout the corresponding job gets suspended again
	// and requeued after the backoff delay. The timeout is enforced only if waitForPodsReady.enable=true.
	// If not set, there is no timeout.
	// +optional
	RecoveryTimeout *metav1.Duration `json:"recoveryTimeout,omitempty"`
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

	// DispatcherName defines the dispatcher responsible for selecting worker clusters to handle the workload.
	// - If specified, the workload will be handled by the named dispatcher.
	// - If not specified, the workload will be handled by the default ("kueue.x-k8s.io/multikueue-dispatcher-all-at-once") dispatcher.
	// +optional
	DispatcherName *string `json:"dispatcherName,omitempty"`
}

const (
	// MultiKueueDispatcherModeAllAtOnce is the name of dispatcher mode where all worker clusters are considered at once
	// and the first one accepting the workload is selected.
	MultiKueueDispatcherModeAllAtOnce = "kueue.x-k8s.io/multikueue-dispatcher-all-at-once"

	// MultiKueueDispatcherModeIncremental is the name of dispatcher mode where worker clusters are incrementally added to the pool of nominated clusters.
	// The process begins with up to 3 initial clusters and expands the pool by up to 3 clusters at a time (if fewer remain, all are added).
	MultiKueueDispatcherModeIncremental = "kueue.x-k8s.io/multikueue-dispatcher-incremental"
)

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
	// Enable controls the use of internal cert management for the webhook
	// and metrics endpoints.
	// When enabled Kueue is using libraries to generate and
	// self-sign the certificates.
	// When disabled, you need to provide the certificates for
	// the webhooks and metrics through a third party certificate
	// This secret is mounted to the kueue controller manager pod. The mount
	// path for webhooks is /tmp/k8s-webhook-server/serving-certs, whereas for
	// metrics endpoint the expected path is `/etc/kueue/metrics/certs`.
	// The keys and certs are named tls.key and tls.crt.
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
	//
	// Setting this to a negative value will disable client-side ratelimiting.
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
	//  - "kubeflow.org/paddlejob"
	//  - "kubeflow.org/pytorchjob"
	//  - "kubeflow.org/tfjob"
	//  - "kubeflow.org/xgboostjob"
	//  - "kubeflow.org/jaxjob"
	//  - "workload.codeflare.dev/appwrapper"
	//  - "pod"
	//  - "deployment" (requires enabling pod integration)
	//  - "statefulset" (requires enabling pod integration)
	//  - "leaderworkerset.x-k8s.io/leaderworkerset" (requires enabling pod integration)
	Frameworks []string `json:"frameworks,omitempty"`
	// List of GroupVersionKinds that are managed for Kueue by external controllers;
	// the expected format is `Kind.version.group.com`.
	ExternalFrameworks []string `json:"externalFrameworks,omitempty"`
	// PodOptions defines kueue controller behaviour for pod objects
	// Deprecated: This field will be removed on v1beta2, use ManagedJobsNamespaceSelector
	// (https://kueue.sigs.k8s.io/docs/tasks/run/plain_pods/)
	// instead.
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

	// Transformations defines how to transform PodSpec resources into Workload resource requests.
	// This is intended to be a map with Input as the key (enforced by validation code)
	Transformations []ResourceTransformation `json:"transformations,omitempty"`
}

type ResourceTransformationStrategy string

const Retain ResourceTransformationStrategy = "Retain"
const Replace ResourceTransformationStrategy = "Replace"

type ResourceTransformation struct {
	// Input is the name of the input resource.
	Input corev1.ResourceName `json:"input"`

	// Strategy specifies if the input resource should be replaced or retained.
	// Defaults to Retain
	Strategy *ResourceTransformationStrategy `json:"strategy,omitempty"`

	// Outputs specifies the output resources and quantities per unit of input resource.
	// An empty Outputs combined with a `Replace` Strategy causes the Input resource to be ignored by Kueue.
	Outputs corev1.ResourceList `json:"outputs,omitempty"`
}

type PreemptionStrategy string

const (
	LessThanOrEqualToFinalShare PreemptionStrategy = "LessThanOrEqualToFinalShare"
	LessThanInitialShare        PreemptionStrategy = "LessThanInitialShare"
)

type FairSharing struct {
	// enable indicates whether to enable Fair Sharing for all cohorts.
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

type AdmissionFairSharing struct {
	// usageHalfLifeTime indicates the time after which the current usage will decay by a half
	// If set to 0, usage will be reset to 0 immediately.
	UsageHalfLifeTime metav1.Duration `json:"usageHalfLifeTime"`

	// usageSamplingInterval indicates how often Kueue updates consumedResources in FairSharingStatus
	// Defaults to 5min.
	UsageSamplingInterval metav1.Duration `json:"usageSamplingInterval"`

	// resourceWeights assigns weights to resources which then are used to calculate LocalQueue's
	// resource usage and order Workloads.
	// Defaults to 1.
	ResourceWeights map[corev1.ResourceName]float64 `json:"resourceWeights,omitempty"`
}

// ObjectRetentionPolicies holds retention settings for different object types.
type ObjectRetentionPolicies struct {
	// Workloads configures retention for Workloads.
	// A nil value disables automatic deletion of Workloads.
	// +optional
	Workloads *WorkloadRetentionPolicy `json:"workloads,omitempty"`
}

// WorkloadRetentionPolicy defines the policies for when Workloads should be deleted.
type WorkloadRetentionPolicy struct {
	// AfterFinished is the duration to wait after a Workload finishes
	// before deleting it.
	// A duration of 0 will delete immediately.
	// A nil value disables automatic deletion.
	// Represented using metav1.Duration (e.g. "10m", "1h30m").
	// +optional
	AfterFinished *metav1.Duration `json:"afterFinished,omitempty"`

	// AfterDeactivatedByKueue is the duration to wait after *any* Kueue-managed Workload
	// (such as a Job, JobSet, or other custom workload types) has been marked
	// as deactivated by Kueue before automatically deleting it.
	// Deletion of deactivated workloads may cascade to objects not created by
	// Kueue, since deleting the parent Workload owner (e.g. JobSet) can trigger
	// garbage-collection of dependent resources.
	// A duration of 0 will delete immediately.
	// A nil value disables automatic deletion.
	// Represented using metav1.Duration (e.g. "10m", "1h30m").
	// +optional
	AfterDeactivatedByKueue *metav1.Duration `json:"afterDeactivatedByKueue,omitempty"`
}
