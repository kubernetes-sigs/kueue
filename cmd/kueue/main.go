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
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue/externalframeworks"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	dispatcher "sigs.k8s.io/kueue/pkg/controller/workloaddispatcher"
	"sigs.k8s.io/kueue/pkg/debugger"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/pkg/util/cert"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/useragent"
	"sigs.k8s.io/kueue/pkg/version"
	"sigs.k8s.io/kueue/pkg/visibility"
	"sigs.k8s.io/kueue/pkg/webhooks"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	// Ensure linking of the job controllers.
	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1.AddToScheme(scheme))

	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(kueuealpha.AddToScheme(scheme))
	utilruntime.Must(configapi.AddToScheme(scheme))
	utilruntime.Must(autoscaling.AddToScheme(scheme))
	// Add any additional framework integration types.
	utilruntime.Must(
		jobframework.ForEachIntegration(func(_ string, cb jobframework.IntegrationCallbacks) error {
			if cb.AddToScheme != nil {
				return cb.AddToScheme(scheme)
			}
			return nil
		}),
	)
}

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

	options, cfg, err := apply(configFile)
	if err != nil {
		setupLog.Error(err, "Unable to load the configuration")
		os.Exit(1)
	}

	if err := config.ValidateFeatureGates(featureGates, cfg.FeatureGates); err != nil {
		setupLog.Error(err, "conflicting feature gates detected")
		os.Exit(1)
	}

	if featureGates != "" {
		if err := utilfeature.DefaultMutableFeatureGate.Set(featureGates); err != nil {
			setupLog.Error(err, "Unable to set flag gates for known features")
			os.Exit(1)
		}
	} else {
		if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(cfg.FeatureGates); err != nil {
			setupLog.Error(err, "Unable to set flag gates for known features")
			os.Exit(1)
		}
	}

	setupLog.Info("Initializing", "gitVersion", version.GitVersion, "gitCommit", version.GitCommit, "buildDate", version.BuildDate)

	features.LogFeatureGates(setupLog)

	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := metricsserver.Options{
		BindAddress:    cfg.Metrics.BindAddress,
		SecureServing:  true,
		FilterProvider: filters.WithAuthenticationAndAuthorization,
	}

	if cfg.InternalCertManagement == nil || !*cfg.InternalCertManagement.Enable {
		metricsCertPath := "/etc/kueue/metrics/certs"
		setupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath)

		var err error
		metricsCertWatcher, err := certwatcher.New(
			filepath.Join(metricsCertPath, "tls.crt"),
			filepath.Join(metricsCertPath, "tls.key"),
		)
		if err != nil {
			setupLog.Error(err, "Unable to initialize metrics certificate watcher")
			os.Exit(1)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}
	options.Metrics = metricsServerOptions

	metrics.Register()

	kubeConfig := ctrl.GetConfigOrDie()
	if kubeConfig.UserAgent == "" {
		kubeConfig.UserAgent = useragent.Default()
	}

	// Set the RateLimiter here, otherwise the controller-runtime's typedClient will use a different RateLimiter
	// for each API type.
	// When the controller-runtime > 0.21, the client-side ratelimiting will be disabled by default.
	// The following QPS negative value chack allows us to disable the client-side ratelimiting.
	if *cfg.ClientConnection.QPS >= 0.0 {
		kubeConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(*cfg.ClientConnection.QPS, int(*cfg.ClientConnection.Burst))
	}
	setupLog.V(2).Info("K8S Client", "qps", *cfg.ClientConnection.QPS, "burst", *cfg.ClientConnection.Burst)
	mgr, err := ctrl.NewManager(kubeConfig, options)
	if err != nil {
		setupLog.Error(err, "Unable to start manager")
		os.Exit(1)
	}

	certsReady := make(chan struct{})

	if cfg.InternalCertManagement != nil && *cfg.InternalCertManagement.Enable {
		if err = cert.ManageCerts(mgr, cfg, certsReady); err != nil {
			setupLog.Error(err, "Unable to set up cert rotation")
			os.Exit(1)
		}
	} else {
		close(certsReady)
	}
	cacheOptions := []schdcache.Option{schdcache.WithPodsReadyTracking(blockForPodsReady(&cfg))}
	queueOptions := []qcache.Option{qcache.WithPodsReadyRequeuingTimestamp(podsReadyRequeuingTimestamp(&cfg))}
	if cfg.Resources != nil && len(cfg.Resources.ExcludeResourcePrefixes) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithExcludedResourcePrefixes(cfg.Resources.ExcludeResourcePrefixes))
		queueOptions = append(queueOptions, qcache.WithExcludedResourcePrefixes(cfg.Resources.ExcludeResourcePrefixes))
	}
	if features.Enabled(features.ConfigurableResourceTransformations) && cfg.Resources != nil && len(cfg.Resources.Transformations) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithResourceTransformations(cfg.Resources.Transformations))
		queueOptions = append(queueOptions, qcache.WithResourceTransformations(cfg.Resources.Transformations))
	}
	if features.Enabled(features.DynamicResourceAllocation) && cfg.Resources != nil && len(cfg.Resources.DeviceClassMappings) > 0 {
		if err := dra.CreateMapperFromConfiguration(cfg.Resources.DeviceClassMappings); err != nil {
			setupLog.Error(err, "Failed to initialize DRA mapper from configuration")
			os.Exit(1)
		}
		setupLog.Info("DRA mapper initialized from configuration")
	}
	if cfg.FairSharing != nil {
		cacheOptions = append(cacheOptions, schdcache.WithFairSharing(cfg.FairSharing.Enable))
	}
	if cfg.AdmissionFairSharing != nil {
		queueOptions = append(queueOptions, qcache.WithAdmissionFairSharing(cfg.AdmissionFairSharing))
		cacheOptions = append(cacheOptions, schdcache.WithAdmissionFairSharing(cfg.AdmissionFairSharing))
	}
	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	queues := qcache.NewManager(mgr.GetClient(), cCache, queueOptions...)

	ctx := ctrl.SetupSignalHandler()
	if err := setupIndexes(ctx, mgr, &cfg); err != nil {
		setupLog.Error(err, "Unable to setup indexes")
		os.Exit(1)
	}
	debugger.NewDumper(cCache, queues).ListenForSignal(ctx)

	serverVersionFetcher, err := setupServerVersionFetcher(mgr, kubeConfig)
	if err != nil {
		setupLog.Error(err, "Unable to setup server version fetcher")
		os.Exit(1)
	}

	if err := setupProbeEndpoints(mgr, certsReady); err != nil {
		setupLog.Error(err, "Unable to setup probe endpoints")
		os.Exit(1)
	}

	// Cert won't be ready until manager starts, so start a goroutine here which
	// will block until the cert is ready before setting up the controllers.
	// Controllers who register after manager starts will start directly.
	go func() {
		if err := setupControllers(ctx, mgr, cCache, queues, certsReady, &cfg, serverVersionFetcher); err != nil {
			setupLog.Error(err, "Unable to setup controllers")
			os.Exit(1)
		}
	}()

	go queues.CleanUpOnContext(ctx)
	go cCache.CleanUpOnContext(ctx)

	if features.Enabled(features.VisibilityOnDemand) {
		go func() {
			if err := visibility.CreateAndStartVisibilityServer(ctx, queues, *cfg.InternalCertManagement.Enable); err != nil {
				setupLog.Error(err, "Unable to create and start visibility server")
				os.Exit(1)
			}
		}()
	}

	if err := setupScheduler(mgr, cCache, queues, &cfg); err != nil {
		setupLog.Error(err, "Could not setup scheduler")
		os.Exit(1)
	}

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Could not run manager")
		os.Exit(1)
	}
}

func setupIndexes(ctx context.Context, mgr ctrl.Manager, cfg *configapi.Configuration) error {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	if err != nil {
		return err
	}

	// setup provision admission check controller indexes
	if err := provisioning.ServerSupportsProvisioningRequest(mgr); err != nil {
		setupLog.Error(err, "Skipping admission check controller setup: Provisioning Requests not supported (Possible cause: missing or unsupported cluster-autoscaler)")
	} else if err := provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("could not setup provisioning indexer: %w", err)
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if err := tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
			return fmt.Errorf("could not setup TAX indexer: %w", err)
		}
	}

	if features.Enabled(features.MultiKueue) {
		if err := multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), *cfg.Namespace); err != nil {
			return fmt.Errorf("could not setup multikueue indexer: %w", err)
		}
	}

	opts := []jobframework.Option{
		jobframework.WithEnabledFrameworks(cfg.Integrations.Frameworks),
	}
	return jobframework.SetupIndexes(ctx, mgr.GetFieldIndexer(), opts...)
}

func setupControllers(ctx context.Context, mgr ctrl.Manager, cCache *schdcache.Cache, queues *qcache.Manager, certsReady chan struct{}, cfg *configapi.Configuration, serverVersionFetcher *kubeversion.ServerVersionFetcher) error {
	// The controllers won't work until the webhooks are operating, and the webhook won't work until the
	// certs are all in place.
	cert.WaitForCertsReady(setupLog, certsReady)

	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache, cfg); err != nil {
		return fmt.Errorf("unable to create controller %s: %w", failedCtrl, err)
	}

	// setup provision admission check controller
	if err := provisioning.ServerSupportsProvisioningRequest(mgr); err != nil {
		setupLog.Info("Skipping provisioning controller setup: Provisioning Requests not supported (Possible cause: missing or unsupported cluster-autoscaler)")
	} else {
		ctrl, err := provisioning.NewController(mgr.GetClient(), mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
		if err != nil {
			return fmt.Errorf("could not create the provisioning controller: %w", err)
		}

		if err := ctrl.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("could not setup provisioning controller: %w", err)
		}
	}

	if features.Enabled(features.MultiKueue) {
		adapters, err := jobframework.GetMultiKueueAdapters(sets.New(cfg.Integrations.Frameworks...))
		if err != nil {
			return fmt.Errorf("could not get the enabled multikueue adapters: %w", err)
		}

		if features.Enabled(features.MultiKueueAdaptersForCustomJobs) && cfg.MultiKueue != nil && len(cfg.MultiKueue.ExternalFrameworks) > 0 {
			externalAdapters, err := externalframeworks.NewAdapters(cfg.MultiKueue.ExternalFrameworks)
			if err != nil {
				return fmt.Errorf("could not create external framework adapters: %w", err)
			}

			// Add external framework adapters to the adapters map
			for _, adapter := range externalAdapters {
				gvk := adapter.GVK().String()
				setupLog.Info("Creating external framework MultiKueue adapter", "gvk", gvk)
				adapters[gvk] = adapter
			}
		}

		if err := multikueue.SetupControllers(mgr, *cfg.Namespace,
			multikueue.WithGCInterval(cfg.MultiKueue.GCInterval.Duration),
			multikueue.WithOrigin(ptr.Deref(cfg.MultiKueue.Origin, configapi.DefaultMultiKueueOrigin)),
			multikueue.WithWorkerLostTimeout(cfg.MultiKueue.WorkerLostTimeout.Duration),
			multikueue.WithAdapters(adapters),
			multikueue.WithDispatcherName(ptr.Deref(cfg.MultiKueue.DispatcherName, configapi.MultiKueueDispatcherModeAllAtOnce)),
		); err != nil {
			return fmt.Errorf("could not setup MultiKueue controller: %w", err)
		}

		if failedDispatcher, err := dispatcher.SetupControllers(mgr, cfg, ptr.Deref(cfg.MultiKueue.DispatcherName, configapi.MultiKueueDispatcherModeAllAtOnce)); err != nil {
			return fmt.Errorf("could not setup Dispatcher controller %q for MultiKueue: %w", failedDispatcher, err)
		}
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if failedCtrl, err := tas.SetupControllers(mgr, queues, cCache, cfg); err != nil {
			return fmt.Errorf("could not setup TAS controller %s: %w", failedCtrl, err)
		}
	}

	if failedWebhook, err := webhooks.Setup(mgr, ptr.Deref(cfg.MultiKueue.DispatcherName, configapi.MultiKueueDispatcherModeAllAtOnce)); err != nil {
		return fmt.Errorf("unable to create webhook %s: %w", failedWebhook, err)
	}

	opts := []jobframework.Option{
		jobframework.WithManageJobsWithoutQueueName(cfg.ManageJobsWithoutQueueName),
		jobframework.WithWaitForPodsReady(cfg.WaitForPodsReady),
		jobframework.WithKubeServerVersion(serverVersionFetcher),
		jobframework.WithEnabledFrameworks(cfg.Integrations.Frameworks),
		jobframework.WithEnabledExternalFrameworks(cfg.Integrations.ExternalFrameworks),
		jobframework.WithManagerName(constants.KueueName),
		jobframework.WithLabelKeysToCopy(cfg.Integrations.LabelKeysToCopy),
		jobframework.WithCache(cCache),
		jobframework.WithQueues(queues),
		jobframework.WithObjectRetentionPolicies(cfg.ObjectRetentionPolicies),
	}
	if cfg.Integrations.PodOptions != nil {
		opts = append(opts, jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), cfg.Integrations.PodOptions))
	}
	nsSelector, err := metav1.LabelSelectorAsSelector(cfg.ManagedJobsNamespaceSelector)
	if err != nil {
		return fmt.Errorf("failed to parse managedJobsNamespaceSelector: %w", err)
	}
	opts = append(opts, jobframework.WithManagedJobsNamespaceSelector(nsSelector))

	if err := jobframework.SetupControllers(ctx, mgr, setupLog, opts...); err != nil {
		return fmt.Errorf("unable to create controller or webhook for kubernetesVersion %v: %w", serverVersionFetcher.GetServerVersion(), err)
	}

	return nil
}

// setupProbeEndpoints registers the health endpoints
func setupProbeEndpoints(mgr ctrl.Manager, certsReady <-chan struct{}) error {
	defer setupLog.Info("Probe endpoints are configured on healthz and readyz")

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}

	// Wait for the webhook server to be listening before advertising the
	// Kueue replica as ready. This allows users to wait with sending the first
	// requests, requiring webhooks, until the Kueue deployment is available, so
	// that the early requests are not rejected during the Kueue's startup.
	// We wrap the call to GetWebhookServer in a closure to delay calling
	// the function, otherwise a not fully-initialized webhook server (without
	// ready certs) fails the start of the manager.
	if err := mgr.AddReadyzCheck("readyz", func(req *http.Request) error {
		select {
		case <-certsReady:
			return mgr.GetWebhookServer().StartedChecker()(req)
		default:
			return errors.New("certificates are not ready")
		}
	}); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	return nil
}

func setupScheduler(mgr ctrl.Manager, cCache *schdcache.Cache, queues *qcache.Manager, cfg *configapi.Configuration) error {
	sched := scheduler.New(
		queues,
		cCache,
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.AdmissionName),
		scheduler.WithPodsReadyRequeuingTimestamp(podsReadyRequeuingTimestamp(cfg)),
		scheduler.WithFairSharing(cfg.FairSharing),
		scheduler.WithAdmissionFairSharing(cfg.AdmissionFairSharing),
	)
	if err := mgr.Add(sched); err != nil {
		return fmt.Errorf("unable to add scheduler to manager: %w", err)
	}
	return nil
}

func setupServerVersionFetcher(mgr ctrl.Manager, kubeConfig *rest.Config) (*kubeversion.ServerVersionFetcher, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to create the discovery client: %w", err)
	}

	serverVersionFetcher := kubeversion.NewServerVersionFetcher(discoveryClient)

	if err := mgr.Add(serverVersionFetcher); err != nil {
		return nil, fmt.Errorf("unable to add server version fetcher to manager: %w", err)
	}

	if err := serverVersionFetcher.FetchServerVersion(); err != nil {
		return nil, fmt.Errorf("failed to fetch kubernetes server version: %w", err)
	}

	return serverVersionFetcher, nil
}

func blockForPodsReady(cfg *configapi.Configuration) bool {
	return config.WaitForPodsReadyIsEnabled(cfg) && cfg.WaitForPodsReady.BlockAdmission != nil && *cfg.WaitForPodsReady.BlockAdmission
}

func podsReadyRequeuingTimestamp(cfg *configapi.Configuration) configapi.RequeuingTimestamp {
	if cfg.WaitForPodsReady != nil && cfg.WaitForPodsReady.RequeuingStrategy != nil &&
		cfg.WaitForPodsReady.RequeuingStrategy.Timestamp != nil {
		return *cfg.WaitForPodsReady.RequeuingStrategy.Timestamp
	}
	return configapi.EvictionTimestamp
}

func apply(configFile string) (ctrl.Options, configapi.Configuration, error) {
	options, cfg, err := config.Load(scheme, configFile)
	if err != nil {
		return options, cfg, err
	}
	cfgStr, err := config.Encode(scheme, &cfg)
	if err != nil {
		return options, cfg, err
	}
	setupLog.Info("Successfully loaded configuration", "config", cfgStr)
	return options, cfg, nil
}
