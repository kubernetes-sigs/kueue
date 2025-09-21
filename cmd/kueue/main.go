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

package main

import (
	"crypto/tls"
	"flag"
	"os"
	"path/filepath"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/debugger"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/manager"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/cert"
	"sigs.k8s.io/kueue/pkg/util/useragent"
	"sigs.k8s.io/kueue/pkg/version"
	"sigs.k8s.io/kueue/pkg/visibility"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	// Ensure linking of the job controllers.
	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

func main() {
	var configFile string
	flag.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. ")

	var featureGates string
	flag.StringVar(&featureGates, "feature-gates", "", "A set of key=value pairs that describe feature gates for alpha/experimental features.")

	opts := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller()},
	}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	manager := manager.NewConfig()

	err := manager.Apply(configFile)
	if err != nil {
		manager.SetupLog.Error(err, "Unable to load the configuration")
		os.Exit(1)
	}

	if err := config.ValidateFeatureGates(featureGates, manager.Apiconf.FeatureGates); err != nil {
		manager.SetupLog.Error(err, "conflicting feature gates detected")
		os.Exit(1)
	}

	if featureGates != "" {
		if err := utilfeature.DefaultMutableFeatureGate.Set(featureGates); err != nil {
			manager.SetupLog.Error(err, "Unable to set flag gates for known features")
			os.Exit(1)
		}
	} else {
		if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(manager.Apiconf.FeatureGates); err != nil {
			manager.SetupLog.Error(err, "Unable to set flag gates for known features")
			os.Exit(1)
		}
	}

	manager.SetupLog.Info("Initializing", "gitVersion", version.GitVersion, "gitCommit", version.GitCommit, "buildDate", version.BuildDate)
	features.LogFeatureGates(manager.SetupLog)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:    manager.Apiconf.Metrics.BindAddress,
		SecureServing:  true,
		FilterProvider: filters.WithAuthenticationAndAuthorization,
	}

	if manager.Apiconf.InternalCertManagement == nil || !*manager.Apiconf.InternalCertManagement.Enable {
		metricsCertPath := "/etc/kueue/metrics/certs"
		manager.SetupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath)

		var err error
		metricsCertWatcher, err := certwatcher.New(
			filepath.Join(metricsCertPath, "tls.crt"),
			filepath.Join(metricsCertPath, "tls.key"),
		)
		if err != nil {
			manager.SetupLog.Error(err, "Unable to initialize metrics certificate watcher")
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}
	manager.Options.Metrics = metricsServerOptions

	metrics.Register()

	kubeConfig := ctrl.GetConfigOrDie()
	if kubeConfig.UserAgent == "" {
		kubeConfig.UserAgent = useragent.Default()
	}

	// Set the RateLimiter here, otherwise the controller-runtime's typedClient will use a different RateLimiter
	// for each API type.
	// When the controller-runtime > 0.21, the client-side ratelimiting will be disabled by default.
	// The following QPS negative value chack allows us to disable the client-side ratelimiting.
	if *manager.Apiconf.ClientConnection.QPS >= 0.0 {
		kubeConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(*manager.Apiconf.ClientConnection.QPS, int(*manager.Apiconf.ClientConnection.Burst))
	}
	manager.SetupLog.V(2).Info("K8S Client", "qps", *manager.Apiconf.ClientConnection.QPS, "burst", *manager.Apiconf.ClientConnection.Burst)
	mgr, err := ctrl.NewManager(kubeConfig, manager.Options)
	if err != nil {
		manager.SetupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	certsReady := make(chan struct{})
	if manager.Apiconf.InternalCertManagement != nil && *manager.Apiconf.InternalCertManagement.Enable {
		if err = cert.ManageCerts(mgr, manager.Apiconf, certsReady); err != nil {
			manager.SetupLog.Error(err, "Unable to set up cert rotation")
			os.Exit(1)
		}
	} else {
		close(certsReady)
	}
	cacheOptions := []schdcache.Option{schdcache.WithPodsReadyTracking(manager.BlockForPodsReady())}
	queueOptions := []qcache.Option{qcache.WithPodsReadyRequeuingTimestamp(manager.PodsReadyRequeuingTimestamp())}
	if manager.Apiconf.Resources != nil && len(manager.Apiconf.Resources.ExcludeResourcePrefixes) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithExcludedResourcePrefixes(manager.Apiconf.Resources.ExcludeResourcePrefixes))
		queueOptions = append(queueOptions, qcache.WithExcludedResourcePrefixes(manager.Apiconf.Resources.ExcludeResourcePrefixes))
	}
	if features.Enabled(features.ConfigurableResourceTransformations) && manager.Apiconf.Resources != nil && len(manager.Apiconf.Resources.Transformations) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithResourceTransformations(manager.Apiconf.Resources.Transformations))
		queueOptions = append(queueOptions, qcache.WithResourceTransformations(manager.Apiconf.Resources.Transformations))
	}
	if features.Enabled(features.DynamicResourceAllocation) && manager.Apiconf.Resources != nil && len(manager.Apiconf.Resources.DeviceClassMappings) > 0 {
		if err := dra.CreateMapperFromConfiguration(manager.Apiconf.Resources.DeviceClassMappings); err != nil {
			manager.SetupLog.Error(err, "Failed to initialize DRA mapper from configuration")
			os.Exit(1)
		}
		manager.SetupLog.Info("DRA mapper initialized from configuration")
	}
	if manager.Apiconf.FairSharing != nil {
		cacheOptions = append(cacheOptions, schdcache.WithFairSharing(manager.Apiconf.FairSharing.Enable))
	}
	if manager.Apiconf.AdmissionFairSharing != nil {
		queueOptions = append(queueOptions, qcache.WithAdmissionFairSharing(manager.Apiconf.AdmissionFairSharing))
		cacheOptions = append(cacheOptions, schdcache.WithAdmissionFairSharing(manager.Apiconf.AdmissionFairSharing))
	}
	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	queues := qcache.NewManager(mgr.GetClient(), cCache, queueOptions...)

	ctx := ctrl.SetupSignalHandler()
	if err := manager.SetupIndexes(ctx, mgr); err != nil {
		manager.SetupLog.Error(err, "Unable to setup indexes")
		os.Exit(1)
	}
	debugger.NewDumper(cCache, queues).ListenForSignal(ctx)

	serverVersionFetcher, err := manager.SetupServerVersionFetcher(mgr, kubeConfig)
	if err != nil {
		manager.SetupLog.Error(err, "Unable to setup server version fetcher")
		os.Exit(1)
	}

	if err := manager.SetupProbeEndpoints(mgr, certsReady); err != nil {
		manager.SetupLog.Error(err, "Unable to setup probe endpoints")
		os.Exit(1)
	}

	// Cert won't be ready until manager starts, so start a goroutine here which
	// will block until the cert is ready before setting up the controllers.
	// Controllers who register after manager starts will start directly.
	go func() {
		if err := manager.SetupControllers(ctx, mgr, cCache, queues, certsReady, serverVersionFetcher); err != nil {
			manager.SetupLog.Error(err, "Unable to setup controllers")
			os.Exit(1)
		}
	}()

	go queues.CleanUpOnContext(ctx)
	go cCache.CleanUpOnContext(ctx)

	if features.Enabled(features.VisibilityOnDemand) {
		go func() {
			internalCertEnabled := manager.Apiconf.InternalCertManagement != nil && *manager.Apiconf.InternalCertManagement.Enable
			if err := visibility.CreateAndStartVisibilityServer(ctx, queues, internalCertEnabled, kubeConfig); err != nil {
				manager.SetupLog.Error(err, "Unable to create and start visibility server")
				os.Exit(1)
			}
		}()
	}

	if err := manager.SetupScheduler(mgr, cCache, queues); err != nil {
		manager.SetupLog.Error(err, "Could not setup scheduler")
		os.Exit(1)
	}

	manager.SetupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		manager.SetupLog.Error(err, "Could not run manager")
		os.Exit(1)
	}
}
