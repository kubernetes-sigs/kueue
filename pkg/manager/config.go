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

package manager

import (
	"github.com/go-logr/logr"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	// Ensure linking of the job controllers.
	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

// Config holds the configuration for setting up the Kueue manager
type Config struct {
	scheme   *runtime.Scheme
	SetupLog logr.Logger
	Apiconf  configapi.Configuration
	Options  ctrl.Options
}

// NewConfig creates a new Config instance with the specified config file
func NewConfig() *Config {
	config := &Config{
		scheme:   runtime.NewScheme(),
		SetupLog: ctrl.Log.WithName("setup"),
	}

	utilruntime.Must(clientgoscheme.AddToScheme(config.scheme))
	utilruntime.Must(schedulingv1.AddToScheme(config.scheme))

	utilruntime.Must(kueue.AddToScheme(config.scheme))
	utilruntime.Must(kueuealpha.AddToScheme(config.scheme))
	utilruntime.Must(configapi.AddToScheme(config.scheme))
	utilruntime.Must(autoscaling.AddToScheme(config.scheme))
	// Add any additional framework integration types.
	utilruntime.Must(
		jobframework.ForEachIntegration(func(_ string, cb jobframework.IntegrationCallbacks) error {
			if cb.AddToScheme != nil {
				return cb.AddToScheme(config.scheme)
			}
			return nil
		}),
	)

	return config
}

// Apply loads and applies the configuration from the config file
func (c *Config) Apply(configFile string) error {
	options, cfg, err := config.Load(c.scheme, configFile)
	if err != nil {
		return err
	}
	cfgStr, err := config.Encode(c.scheme, &cfg)
	if err != nil {
		return err
	}
	c.SetupLog.Info("Successfully loaded configuration", "config", cfgStr)
	c.Apiconf = cfg
	c.Options = options

	return nil
}

// BlockForPodsReady returns whether the admission should be blocked until pods are ready
func (c *Config) BlockForPodsReady() bool {
	return config.WaitForPodsReadyIsEnabled(&c.Apiconf) && c.Apiconf.WaitForPodsReady.BlockAdmission != nil && *c.Apiconf.WaitForPodsReady.BlockAdmission
}

// PodsReadyRequeuingTimestamp returns the timestamp strategy for requeuing pods
func (c *Config) PodsReadyRequeuingTimestamp() configapi.RequeuingTimestamp {
	if c.Apiconf.WaitForPodsReady != nil && c.Apiconf.WaitForPodsReady.RequeuingStrategy != nil &&
		c.Apiconf.WaitForPodsReady.RequeuingStrategy.Timestamp != nil {
		return *c.Apiconf.WaitForPodsReady.RequeuingStrategy.Timestamp
	}
	return configapi.EvictionTimestamp
}
