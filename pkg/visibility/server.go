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

package visibility

import (
	"context"
	"fmt"
	"net"
	"strings"

	validatingadmissionpolicy "k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	mutatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/mutating"
	validatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/component-base/compatibility"
	"k8s.io/component-base/version"

	generatedopenapi "sigs.k8s.io/kueue/apis/visibility/openapi"
	visibilityv1beta1 "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/visibility/api"

	_ "k8s.io/component-base/metrics/prometheus/restclient" // for client-go metrics registration
)

var (
	// Admission plugins that are enabled by default in the kubeapi server
	// but are not required for the visibility server.
	disabledPlugins = []string{
		validatingadmissionpolicy.PluginName,
		resourcequota.PluginName,
		validatingwebhook.PluginName,
		mutatingwebhook.PluginName,
	}
	certDir = "/visibility"
)

// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=prioritylevelconfigurations,verbs=list;watch
// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=flowschemas,verbs=list;watch
// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=flowschemas/status,verbs=patch

// CreateAndStartVisibilityServer creates visibility server injecting KueueManager and starts it
func CreateAndStartVisibilityServer(ctx context.Context, kueueMgr *queue.Manager) error {
	config := newVisibilityServerConfig()
	if err := applyVisibilityServerOptions(config); err != nil {
		return fmt.Errorf("unable to apply VisibilityServerOptions: %w", err)
	}

	visibilityServer, err := config.Complete().New("visibility-server", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("unable to create visibility server: %w", err)
	}

	if err := api.Install(visibilityServer, kueueMgr); err != nil {
		return fmt.Errorf("unable to install visibility.kueue.x-k8s.io API: %w", err)
	}

	if err := visibilityServer.PrepareRun().RunWithContext(ctx); err != nil {
		return fmt.Errorf("running visibility server: %w", err)
	}

	return nil
}

func applyVisibilityServerOptions(config *genericapiserver.RecommendedConfig) error {
	o := genericoptions.NewRecommendedOptions("", api.Codecs.LegacyCodec(visibilityv1beta1.SchemeGroupVersion))
	o.Etcd = nil
	o.SecureServing.BindPort = 8082
	// The directory where TLS certs will be created
	o.SecureServing.ServerCert.CertDirectory = certDir
	o.Admission.DisablePlugins = disabledPlugins
	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		return fmt.Errorf("error creating self-signed certificates: %v", err)
	}
	return o.ApplyTo(config)
}

func newVisibilityServerConfig() *genericapiserver.RecommendedConfig {
	c := genericapiserver.NewRecommendedConfig(api.Codecs)
	versionInfo := version.Get()
	version := strings.Split(versionInfo.String(), "-")[0]
	// enable OpenAPI schemas
	c.EffectiveVersion = compatibility.NewEffectiveVersionFromString(version, "", "")
	c.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, openapinamer.NewDefinitionNamer(api.Scheme))
	c.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(generatedopenapi.GetOpenAPIDefinitions, openapinamer.NewDefinitionNamer(api.Scheme))
	c.OpenAPIConfig.Info.Title = "Kueue visibility-server"
	c.OpenAPIV3Config.Info.Title = "Kueue visibility-server"
	c.OpenAPIConfig.Info.Version = version
	c.OpenAPIV3Config.Info.Version = version

	c.EnableMetrics = true

	return c
}
