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
	"time"

	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
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
func BootstrapCerts(kubeConfig *rest.Config, cfg config.Configuration) error {
	log := ctrl.Log.WithName("cert-bootstrap")

	// DNSName is <service name>.<namespace>.svc
	var dnsName = fmt.Sprintf("%s.%s.svc", *cfg.InternalCertManagement.WebhookServiceName, *cfg.Namespace)

	// Derive webhook configuration names from the webhook service name
	webhookBaseName := deriveWebhookBaseName(*cfg.InternalCertManagement.WebhookServiceName)
	mutatingWebhookName := buildWebhookConfigurationName(webhookBaseName, "mutating")
	validatingWebhookName := buildWebhookConfigurationName(webhookBaseName, "validating")

	// Create a minimal bootstrap manager with leader election.
	log.Info("Creating bootstrap manager for certificate generation")
	bootstrapMgr, err := ctrl.NewManager(kubeConfig, ctrl.Options{
		LeaderElection:          true,
		LeaderElectionID:        "kueue-bootstrap-cert-generation",
		LeaderElectionNamespace: *cfg.Namespace,
		HealthProbeBindAddress:  "0",
		Metrics:                 metricsserver.Options{BindAddress: "0"},
	})
	if err != nil {
		return fmt.Errorf("unable to create bootstrap manager: %w", err)
	}

	certsReady := make(chan struct{})

	// Add cert rotator to bootstrap manager.
	// Cert-rotator will handle both cert generation AND CA bundle injection
	// for all webhooks (admission + CRD conversion) with built-in retry logic.
	err = cert.AddRotator(bootstrapMgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: *cfg.Namespace,
			Name:      *cfg.InternalCertManagement.WebhookSecretName,
		},
		CertDir:        cfg.Webhook.CertDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        certsReady,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.Validating,
			Name: validatingWebhookName,
		}, {
			Type: cert.Mutating,
			Name: mutatingWebhookName,
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
		}},
		RequireLeaderElection: true,
	})
	if err != nil {
		return fmt.Errorf("unable to add cert rotator to bootstrap manager: %w", err)
	}

	bootstrapCtx, bootstrapCancel := context.WithCancel(context.Background())
	defer bootstrapCancel()

	go func() {
		log.Info("Starting bootstrap manager")
		if err := bootstrapMgr.Start(bootstrapCtx); err != nil {
			log.Error(err, "Bootstrap manager failed")
		}
	}()

	// Wait for cert-rotator to complete cert generation and CA injection
	log.Info("Waiting for certificate generation and CA injection to complete")
	<-certsReady
	log.Info("Certificates ready and CA bundles injected")

	log.Info("Stopping bootstrap manager")
	bootstrapCancel()

	time.Sleep(1 * time.Second)

	log.Info("Certificate bootstrap complete")
	return nil
}

// ManageCerts signals that certs are ready (they were already set up by BootstrapCerts).
func ManageCerts(mgr ctrl.Manager, cfg config.Configuration, setupFinished chan struct{}) error {
	// Certs are already ready from bootstrap, just signal completion
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
