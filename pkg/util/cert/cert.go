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

package cert

import (
	"fmt"

	"github.com/go-logr/logr"
	cert "github.com/open-policy-agent/cert-controller/pkg/rotator"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
)

const (
	certDir        = "/tmp/k8s-webhook-server/serving-certs"
	vwcName        = "kueue-validating-webhook-configuration"
	mwcName        = "kueue-mutating-webhook-configuration"
	caName         = "kueue-ca"
	caOrganization = "kueue"
)

// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=mutatingwebhookconfigurations,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingwebhookconfigurations,verbs=get;list;watch;update
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingadmissionpolicies,verbs=get;list;watch
// +kubebuilder:rbac:groups="admissionregistration.k8s.io",resources=validatingadmissionpolicybindings,verbs=get;list;watch

// ManageCerts creates all certs for webhooks. This function is called from main.go.
func ManageCerts(mgr ctrl.Manager, cfg config.Configuration, setupFinished chan struct{}) error {
	// DNSName is <service name>.<namespace>.svc
	var dnsName = fmt.Sprintf("%s.%s.svc", *cfg.InternalCertManagement.WebhookServiceName, *cfg.Namespace)

	return cert.AddRotator(mgr, &cert.CertRotator{
		SecretKey: types.NamespacedName{
			Namespace: *cfg.Namespace,
			Name:      *cfg.InternalCertManagement.WebhookSecretName,
		},
		CertDir:        certDir,
		CAName:         caName,
		CAOrganization: caOrganization,
		DNSName:        dnsName,
		IsReady:        setupFinished,
		Webhooks: []cert.WebhookInfo{{
			Type: cert.Validating,
			Name: vwcName,
		}, {
			Type: cert.Mutating,
			Name: mwcName,
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
