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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cfg "sigs.k8s.io/controller-runtime/pkg/config/v1alpha1"
)

//+kubebuilder:object:root=true

// Configuration is the Schema for the kueueconfigurations API
type Configuration struct {
	metav1.TypeMeta `json:",inline"`

	// Namespace is the namespace in which kueue is deployed. It is used as part of DNSName of the webhook Service.
	// Defaults to kueue-system.
	Namespace *string `json:"namespace,omitempty"`

	// ControllerManagerConfigurationSpec returns the configurations for controllers
	cfg.ControllerManagerConfigurationSpec `json:",inline"`

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
