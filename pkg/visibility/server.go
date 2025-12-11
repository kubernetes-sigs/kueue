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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	validatingadmissionpolicy "k8s.io/apiserver/pkg/admission/plugin/policy/validating"
	"k8s.io/apiserver/pkg/admission/plugin/resourcequota"
	mutatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/mutating"
	validatingwebhook "k8s.io/apiserver/pkg/admission/plugin/webhook/validating"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	genericapiserver "k8s.io/apiserver/pkg/server"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/rest"
	"k8s.io/component-base/compatibility"
	"k8s.io/component-base/version"

	generatedopenapi "sigs.k8s.io/kueue/apis/visibility/openapi"
	visibilityv1beta1 "sigs.k8s.io/kueue/apis/visibility/v1beta1"
	visibilityv1beta2 "sigs.k8s.io/kueue/apis/visibility/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/util/tlsconfig"
	"sigs.k8s.io/kueue/pkg/visibility/storage"

	_ "k8s.io/component-base/metrics/prometheus/restclient" // for client-go metrics registration
)

var (
	scheme         = runtime.NewScheme()
	codecs         = serializer.NewCodecFactory(scheme)
	parameterCodec = runtime.NewParameterCodec(scheme)
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

func init() {
	utilruntime.Must(visibilityv1beta2.AddToScheme(scheme))
	utilruntime.Must(visibilityv1beta1.AddToScheme(scheme))
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Version: "v1"})
}

// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=prioritylevelconfigurations,verbs=list;watch
// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=flowschemas,verbs=list;watch
// +kubebuilder:rbac:groups=flowcontrol.apiserver.k8s.io,resources=flowschemas/status,verbs=patch

// CreateAndStartVisibilityServer creates a visibility server injecting KueueManager and starts it
func CreateAndStartVisibilityServer(ctx context.Context, kueueMgr *qcache.Manager, enableInternalCertManagement bool, kubeConfig *rest.Config, tlsOpts *tlsconfig.TLS) error {
	config := newVisibilityServerConfig(kubeConfig)
	if err := applyVisibilityServerOptions(config, enableInternalCertManagement, tlsOpts); err != nil {
		return fmt.Errorf("unable to apply VisibilityServerOptions: %w", err)
	}

	visibilityServer, err := config.Complete().New("visibility-server", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return fmt.Errorf("unable to create visibility server: %w", err)
	}

	if err := install(visibilityServer, kueueMgr); err != nil {
		return fmt.Errorf("unable to install visibility.kueue.x-k8s.io API: %w", err)
	}

	if err := visibilityServer.PrepareRun().RunWithContext(ctx); err != nil {
		return fmt.Errorf("running visibility server: %w", err)
	}

	return nil
}

func applyVisibilityServerOptions(config *genericapiserver.RecommendedConfig, enableInternalCertManagement bool, tlsOpts *tlsconfig.TLS) error {
	o := genericoptions.NewRecommendedOptions("", codecs.LegacyCodec(
		visibilityv1beta2.SchemeGroupVersion,
		visibilityv1beta1.SchemeGroupVersion,
	))
	o.Etcd = nil
	o.SecureServing.BindPort = 8082
	if enableInternalCertManagement {
		// The directory where TLS certs will be created
		o.SecureServing.ServerCert.CertDirectory = certDir
	} else {
		o.SecureServing.ServerCert.CertKey.CertFile = certDir + "/tls.crt"
		o.SecureServing.ServerCert.CertKey.KeyFile = certDir + "/tls.key"
	}
	o.Admission.DisablePlugins = disabledPlugins
	if err := o.SecureServing.MaybeDefaultWithSelfSignedCerts("localhost", nil, []net.IP{net.ParseIP("127.0.0.1")}); err != nil {
		return fmt.Errorf("error creating self-signed certificates: %v", err)
	}

	if err := o.ApplyTo(config); err != nil {
		return err
	}

	// Only apply TLS options if the feature gate is enabled
	if tlsOpts != nil && config.SecureServing != nil {
		config.SecureServing.MinTLSVersion = tlsOpts.MinVersion
		config.SecureServing.CipherSuites = tlsOpts.CipherSuites
	}

	return nil
}

func newVisibilityServerConfig(kubeConfig *rest.Config) *genericapiserver.RecommendedConfig {
	c := genericapiserver.NewRecommendedConfig(codecs)
	versionInfo := version.Get()
	version := strings.Split(versionInfo.String(), "-")[0]
	// enable OpenAPI schemas
	c.EffectiveVersion = compatibility.NewEffectiveVersionFromString(version, "", "")
	c.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(generatedopenapi.GetOpenAPIDefinitions, openapinamer.NewDefinitionNamer(scheme))
	c.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(generatedopenapi.GetOpenAPIDefinitions, openapinamer.NewDefinitionNamer(scheme))
	c.OpenAPIConfig.Info.Title = "Kueue visibility-server"
	c.OpenAPIV3Config.Info.Title = "Kueue visibility-server"
	c.OpenAPIConfig.Info.Version = version
	c.OpenAPIV3Config.Info.Version = version

	c.EnableMetrics = true
	c.ClientConfig = rest.CopyConfig(kubeConfig)

	return c
}

// install installs API scheme and registers storages
func install(server *genericapiserver.GenericAPIServer, kueueMgr *qcache.Manager) error {
	apiGroupInfo := genericapiserver.NewDefaultAPIGroupInfo(visibilityv1beta2.GroupVersion.Group, scheme, parameterCodec, codecs)
	storage := storage.NewStorage(kueueMgr)
	apiGroupInfo.VersionedResourcesStorageMap[visibilityv1beta2.GroupVersion.Version] = storage
	apiGroupInfo.VersionedResourcesStorageMap[visibilityv1beta1.GroupVersion.Version] = storage
	apiGroupInfo.PrioritizedVersions = []schema.GroupVersion{visibilityv1beta2.GroupVersion, visibilityv1beta1.GroupVersion}
	return server.InstallAPIGroups(&apiGroupInfo)
}
