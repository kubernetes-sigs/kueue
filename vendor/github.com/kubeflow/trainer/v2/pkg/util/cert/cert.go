/*
Copyright 2024 The Kubeflow Authors.

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
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
)

const (
	certDir          = "/tmp/k8s-webhook-server/serving-certs"
	caName           = "kubeflow-trainer-ca"
	caOrganization   = "kubeflow-trainer"
	defaultNamespace = "kubeflow-system"
)

func GetOperatorNamespace() string {
	if data, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
		if ns := strings.TrimSpace(string(data)); len(ns) > 0 {
			return ns
		}
	}
	return defaultNamespace
}

type Config struct {
	WebhookServiceName                 string
	WebhookSecretName                  string
	ValidatingWebhookConfigurationName string
	MutatingWebhookConfigurationName   string
}

//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update
//+kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingwebhookconfigurations,verbs=get;list;watch;update
//+kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=mutatingwebhookconfigurations,verbs=get;list;watch;update

// ManageCerts creates all certs for webhooks.
func ManageCerts(mgr ctrl.Manager, cfg Config, setupFinished chan struct{}) error {

	ns := GetOperatorNamespace()
	// DNSName is <service name>.<namespace>.svc
	dnsName := fmt.Sprintf("%s.%s.svc", cfg.WebhookServiceName, ns)

	return cert.AddRotator(mgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: ns,
			Name:      cfg.WebhookSecretName,
		},
		CertDir:        certDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        setupFinished,
		Webhooks: []cert.WebhookInfo{
			{
				Type: cert.Validating,
				Name: cfg.ValidatingWebhookConfigurationName,
			},
			{
				Type: cert.Mutating,
				Name: cfg.MutatingWebhookConfigurationName,
			},
		},
		// When Kubeflow Trainer is running in the leader election mode,
		// we expect webhook server will run in primary and secondary instance
		RequireLeaderElection: false,
	})
}

// SetupTLSConfig creates a TLS config with automatic certificate rotation support.
// It creates a cert watcher, adds it to the manager, and returns a TLS config
// that will automatically pick up rotated certificates.
func SetupTLSConfig(mgr ctrl.Manager, enableHTTP2 bool) (*tls.Config, error) {
	certWatcher, err := certwatcher.New(certDir+"/tls.crt", certDir+"/tls.key")
	if err != nil {
		return nil, fmt.Errorf("error creating cert watcher: %w", err)
	}

	if err := mgr.Add(certWatcher); err != nil {
		return nil, fmt.Errorf("error adding cert watcher to manager: %w", err)
	}

	tlsConfig := &tls.Config{
		GetCertificate: certWatcher.GetCertificate,
	}

	// Disable HTTP/2 unless explicitly enabled (CVE-2023-44487, CVE-2023-39325)
	if !enableHTTP2 {
		tlsConfig.NextProtos = []string{"http/1.1"}
	}

	return tlsConfig, nil
}
