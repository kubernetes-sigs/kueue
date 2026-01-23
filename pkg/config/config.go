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

package config

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	"k8s.io/apimachinery/pkg/api/equality"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/client-go/rest"
	inventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/tlsconfig"
)

var (
	objectKeySecret         = new(corev1.Secret)
	objectKeyClusterProfile = new(inventoryv1alpha1.ClusterProfile)
)

// fromFile provides an alternative to the deprecated ctrl.ConfigFile().AtPath(path).OfKind(&cfg)
func fromFile(path string, scheme *runtime.Scheme, cfg *configapi.Configuration) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	codecs := serializer.NewCodecFactory(scheme, serializer.EnableStrict)

	// Regardless of if the bytes are of any external version,
	// it will be read successfully and converted into the internal version
	return runtime.DecodeInto(codecs.UniversalDecoder(), content, cfg)
}

// addTo provides an alternative to the deprecated o.AndFrom(&cfg)
func addTo(o *ctrl.Options, cfg *configapi.Configuration) {
	addLeaderElectionTo(o, cfg)
	addCacheByObjectTo(o, cfg)

	if o.Metrics.BindAddress == "" && cfg.Metrics.BindAddress != "" {
		o.Metrics.BindAddress = cfg.Metrics.BindAddress
	}

	if o.PprofBindAddress == "" && cfg.PprofBindAddress != "" {
		o.PprofBindAddress = cfg.PprofBindAddress
	}

	if o.HealthProbeBindAddress == "" && cfg.Health.HealthProbeBindAddress != "" {
		o.HealthProbeBindAddress = cfg.Health.HealthProbeBindAddress
	}

	if o.ReadinessEndpointName == "" && cfg.Health.ReadinessEndpointName != "" {
		o.ReadinessEndpointName = cfg.Health.ReadinessEndpointName
	}

	if o.LivenessEndpointName == "" && cfg.Health.LivenessEndpointName != "" {
		o.LivenessEndpointName = cfg.Health.LivenessEndpointName
	}

	if cfg.Controller != nil {
		if o.Controller.CacheSyncTimeout == 0 && cfg.Controller.CacheSyncTimeout != nil {
			o.Controller.CacheSyncTimeout = *cfg.Controller.CacheSyncTimeout
		}

		if len(o.Controller.GroupKindConcurrency) == 0 && len(cfg.Controller.GroupKindConcurrency) > 0 {
			o.Controller.GroupKindConcurrency = cfg.Controller.GroupKindConcurrency
		}
	}
}

func addCacheByObjectTo(o *ctrl.Options, cfg *configapi.Configuration) {
	if cfg.Namespace == nil {
		// Invalid source; noop. This should not be reached
		// due to prior defaulting/validation.
		return
	}

	if o.Cache.ByObject == nil {
		o.Cache.ByObject = make(map[ctrlclient.Object]ctrlcache.ByObject)
	}

	o.Cache.ByObject[objectKeySecret] = ctrlcache.ByObject{
		Namespaces: map[string]ctrlcache.Config{
			*cfg.Namespace: {},
		},
	}
}

func addLeaderElectionTo(o *ctrl.Options, cfg *configapi.Configuration) {
	if cfg.LeaderElection == nil {
		// The source does not have any configuration; noop
		return
	}

	if !o.LeaderElection && cfg.LeaderElection.LeaderElect != nil {
		o.LeaderElection = *cfg.LeaderElection.LeaderElect
	}

	if o.LeaderElectionResourceLock == "" && cfg.LeaderElection.ResourceLock != "" {
		o.LeaderElectionResourceLock = cfg.LeaderElection.ResourceLock
	}

	if o.LeaderElectionNamespace == "" && cfg.LeaderElection.ResourceNamespace != "" {
		o.LeaderElectionNamespace = cfg.LeaderElection.ResourceNamespace
	}

	if o.LeaderElectionID == "" && cfg.LeaderElection.ResourceName != "" {
		o.LeaderElectionID = cfg.LeaderElection.ResourceName
	}

	if o.LeaseDuration == nil && !equality.Semantic.DeepEqual(cfg.LeaderElection.LeaseDuration, metav1.Duration{}) {
		o.LeaseDuration = &cfg.LeaderElection.LeaseDuration.Duration
	}

	if o.RenewDeadline == nil && !equality.Semantic.DeepEqual(cfg.LeaderElection.RenewDeadline, metav1.Duration{}) {
		o.RenewDeadline = &cfg.LeaderElection.RenewDeadline.Duration
	}

	if o.RetryPeriod == nil && !equality.Semantic.DeepEqual(cfg.LeaderElection.RetryPeriod, metav1.Duration{}) {
		o.RetryPeriod = &cfg.LeaderElection.RetryPeriod.Duration
	}

	if o.LeaderElection {
		// When the manager is terminated, the leader manager voluntarily steps down
		// from the leader role as soon as possible.
		o.LeaderElectionReleaseOnCancel = true
	}
}

// AddWebhookSettingsTo is used to add settings to the webhook
// This is separated to a exported function as we need to call this function after the feature gates are parsed.
func AddWebhookSettingsTo(o *ctrl.Options, cfg *configapi.Configuration) {
	if o.WebhookServer == nil && cfg.Webhook.Port != nil {
		wo := webhook.Options{}
		if cfg.Webhook.Port != nil {
			wo.Port = *cfg.Webhook.Port
		}
		if cfg.Webhook.Host != "" {
			wo.Host = cfg.Webhook.Host
		}

		if cfg.Webhook.CertDir != "" {
			wo.CertDir = cfg.Webhook.CertDir
		}

		// Apply TLS configuration if provided
		if features.Enabled(features.TLSOptions) {
			if cfg.TLS != nil {
				tlsOpts, err := tlsconfig.ParseTLSOptions(cfg.TLS)
				if err == nil {
					tlsOpts := tlsconfig.BuildTLSOptions(tlsOpts)
					wo.TLSOpts = append(wo.TLSOpts, tlsOpts...)
				}
			}
		}
		o.WebhookServer = webhook.NewServer(wo)
	}
}

func Encode(scheme *runtime.Scheme, cfg *configapi.Configuration) (string, error) {
	codecs := serializer.NewCodecFactory(scheme)
	const mediaType = runtime.ContentTypeYAML
	info, ok := runtime.SerializerInfoForMediaType(codecs.SupportedMediaTypes(), mediaType)
	if !ok {
		return "", fmt.Errorf("unable to locate encoder -- %q is not a supported media type", mediaType)
	}

	encoder := codecs.EncoderForVersion(info.Serializer, configapi.GroupVersion)
	buf := new(bytes.Buffer)
	if err := encoder.Encode(cfg, buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// Load returns a set of controller options and configuration from the given file, if the config file path is empty
// it used the default configapi values.
func Load(scheme *runtime.Scheme, configFile string) (ctrl.Options, configapi.Configuration, error) {
	var err error
	options := ctrl.Options{
		Scheme: scheme,
	}

	cfg := configapi.Configuration{}
	if configFile == "" {
		scheme.Default(&cfg)
	} else {
		err := fromFile(configFile, scheme, &cfg)
		if err != nil {
			return options, cfg, err
		}
	}
	if err := validate(&cfg, scheme).ToAggregate(); err != nil {
		return options, cfg, err
	}
	addTo(&options, &cfg)
	return options, cfg, err
}

// ConfigureClusterProfileCache creates the CRD client from kubeConfig and delegates
// to ConfigureClusterProfileCacheWithClient. Keeping this wrapper preserves the
// original API while enabling dependency injection via the WithClient function.
func ConfigureClusterProfileCache(ctx context.Context, log logr.Logger, options *ctrl.Options, kubeConfig *rest.Config, cfg configapi.Configuration) error {
	crdClient, err := apiextensionsclient.NewForConfig(kubeConfig)
	if err != nil {
		return fmt.Errorf("failed creating the CRD client: %w", err)
	}
	return configureClusterProfileCacheWithClient(ctx, log, options, crdClient, cfg)
}

func configureClusterProfileCacheWithClient(ctx context.Context, log logr.Logger, options *ctrl.Options, crdClient apiextensionsclient.Interface, cfg configapi.Configuration) error {
	if _, err := crdClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, "clusterprofiles.multicluster.x-k8s.io", metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("Skipping MultiKueue ClusterProfile setup as the ClusterProfile CRD is not installed")
			return nil
		}
		return fmt.Errorf("failed loading the ClusterProfile CRD: %w", err)
	}
	if options.Cache.ByObject == nil {
		options.Cache.ByObject = make(map[ctrlclient.Object]ctrlcache.ByObject)
	}
	options.Cache.ByObject[objectKeyClusterProfile] = ctrlcache.ByObject{
		Namespaces: map[string]ctrlcache.Config{
			*cfg.Namespace: {},
		},
	}
	return nil
}
