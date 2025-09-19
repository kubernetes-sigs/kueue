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
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
)

const (
	caName               = "kueue-ca"
	caOrganization       = "kueue"
	webhookServiceSuffix = "-webhook-service"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=mutatingwebhookconfigurations,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingwebhookconfigurations,verbs=get;list;watch;update

// ManageCerts creates all certs for webhooks. This function is called from main.go.
func ManageCerts(mgr ctrl.Manager, cfg config.Configuration, setupFinished chan struct{}) error {
	// DNSName is <service name>.<namespace>.svc
	var dnsName = fmt.Sprintf("%s.%s.svc", *cfg.InternalCertManagement.WebhookServiceName, *cfg.Namespace)

	// Derive webhook configuration names from the webhook service name
	webhookBaseName := deriveWebhookBaseName(*cfg.InternalCertManagement.WebhookServiceName)
	mutatingWebhookName := buildWebhookConfigurationName(webhookBaseName, "mutating")
	validatingWebhookName := buildWebhookConfigurationName(webhookBaseName, "validating")

	return cert.AddRotator(mgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: *cfg.Namespace,
			Name:      *cfg.InternalCertManagement.WebhookSecretName,
		},
		CertDir:        cfg.Webhook.CertDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        setupFinished,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.Validating,
			Name: validatingWebhookName,
		}, {
			Type: cert.Mutating,
			Name: mutatingWebhookName,
		}},
		// When kueue is running in the leader election mode,
		// we expect webhook server will run in primary and secondary instance
		RequireLeaderElection: false,
	})
}

func WaitForCertsReady(log logr.Logger, certsReady chan struct{}) {
	log.Info("Waiting for certificate generation to complete")
	<-certsReady
	log.Info("Certs ready")
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
