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
	"context"
	"fmt"
	"strings"

	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
)

const (
	caName               = "kueue-ca"
	caOrganization       = "kueue"
	webhookServiceSuffix = "-webhook-service"
)

// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=mutatingwebhookconfigurations,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingwebhookconfigurations,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="apiextensions.k8s.io",resources=customresourcedefinitions,verbs=get;list;watch;update

// BootstrapCerts creates a minimal manager to generate certificates and inject CA bundles.
// This function blocks until certificates are ready and CA bundles are injected into CRDs.
func BootstrapCerts(ctx context.Context, kubeConfig *rest.Config, cfg config.Configuration) error {
	log := ctrl.Log.WithName("cert-bootstrap")

	// Create a minimal bootstrap manager with leader election.
	log.Info("Creating bootstrap manager for certificate generation")
	bootstrapMgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		HealthProbeBindAddress: cfg.Health.HealthProbeBindAddress,
		LivenessEndpointName:   cfg.Health.LivenessEndpointName,
	})
	if err != nil {
		return fmt.Errorf("unable to create bootstrap manager: %w", err)
	}

	if err := bootstrapMgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check for bootstrap manager: %w", err)
	}

	certsReady := make(chan struct{})

	// Add cert rotator to bootstrap manager using shared config.
	rotatorConfig := buildCertRotatorConfig(cfg, "cert-rotator-bootstrap", certsReady)
	err = cert.AddRotator(bootstrapMgr, rotatorConfig)
	if err != nil {
		return fmt.Errorf("unable to add cert rotator to bootstrap manager: %w", err)
	}

	bootstrapCtx, bootstrapCancel := context.WithCancel(ctx)
	defer bootstrapCancel()

	managerStopped := make(chan struct{})
	go func() {
		log.Info("Starting bootstrap manager")
		if err := bootstrapMgr.Start(bootstrapCtx); err != nil {
			log.Error(err, "Bootstrap manager failed")
		}
		close(managerStopped)
	}()

	// Wait for cert-rotator to complete cert generation and CA injection
	log.Info("Waiting for certificate generation and CA injection to complete")
	<-certsReady
	log.Info("Certificates ready and CA bundles injected")

	log.Info("Stopping bootstrap manager")
	bootstrapCancel()

	log.Info("Waiting for the bootstrap manager to stop")
	<-managerStopped

	log.Info("Certificate bootstrap complete")
	return nil
}

// ManageCerts adds the cert rotator to the main manager for ongoing certificate rotation.
func ManageCerts(mgr ctrl.Manager, cfg config.Configuration, setupFinished chan struct{}) error {
	// Certs are already ready from BootstrapCerts, so we signal immediately
	// The rotator runs in background for ongoing rotation.
	rotatorReady := make(chan struct{})
	rotatorConfig := buildCertRotatorConfig(cfg, "cert-rotator", rotatorReady)

	if err := cert.AddRotator(mgr, rotatorConfig); err != nil {
		return fmt.Errorf("unable to add cert rotator to manager: %w", err)
	}

	// Signal that certs are ready (they were set up by BootstrapCerts).
	close(setupFinished)
	return nil
}

// deriveWebhookBaseName extracts the base name from a webhook service name
func deriveWebhookBaseName(webhookServiceName string) string {
	return strings.TrimSuffix(webhookServiceName, webhookServiceSuffix)
}

// buildWebhookConfigurationName constructs a webhook configuration name
// from a base name and webhook type suffix.
func buildWebhookConfigurationName(baseName, webhookType string) string {
	return fmt.Sprintf("%s-%s-webhook-configuration", baseName, webhookType)
}

// buildCertRotatorConfig creates common CertRotator configuration.
func buildCertRotatorConfig(cfg config.Configuration, controllerName string, certsReady chan struct{}) *cert.CertRotator {
	dnsName := fmt.Sprintf("%s.%s.svc", *cfg.InternalCertManagement.WebhookServiceName, *cfg.Namespace)
	webhookBaseName := deriveWebhookBaseName(*cfg.InternalCertManagement.WebhookServiceName)

	return &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: *cfg.Namespace,
			Name:      *cfg.InternalCertManagement.WebhookSecretName,
		},
		CertDir:        cfg.Webhook.CertDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        certsReady,
		ControllerName: controllerName,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.Validating,
			Name: buildWebhookConfigurationName(webhookBaseName, "validating"),
		}, {
			Type: cert.Mutating,
			Name: buildWebhookConfigurationName(webhookBaseName, "mutating"),
		}, {
			Type: cert.CRDConversion,
			Name: "localqueues.kueue.x-k8s.io",
		}, {
			Type: cert.CRDConversion,
			Name: "clusterqueues.kueue.x-k8s.io",
		}, {
			Type: cert.CRDConversion,
			Name: "workloads.kueue.x-k8s.io",
		}, {
			Type: cert.CRDConversion,
			Name: "cohorts.kueue.x-k8s.io",
		}, {
			Type: cert.CRDConversion,
			Name: "multikueueclusters.kueue.x-k8s.io",
		}},
		RequireLeaderElection: false,
	}
}
