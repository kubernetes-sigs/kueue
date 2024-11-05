/*
Copyright 2021 The Kubernetes Authors.

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
	"errors"
	"flag"
	"net/http"
	"os"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	corev1 "k8s.io/api/core/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/debugger"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
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

	if err := utilfeature.DefaultMutableFeatureGate.Set(featureGates); err != nil {
		setupLog.Error(err, "Unable to set flag gates for known features")
		os.Exit(1)
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	setupLog.Info("Initializing", "gitVersion", version.GitVersion, "gitCommit", version.GitCommit)

	features.LogFeatureGates(setupLog)

	options, cfg, err := apply(configFile)
	if err != nil {
		setupLog.Error(err, "Unable to load the configuration")
		os.Exit(1)
	}

	metrics.Register()

	kubeConfig := ctrl.GetConfigOrDie()
	if kubeConfig.UserAgent == "" {
		kubeConfig.UserAgent = useragent.Default()
	}

	// Set the RateLimiter here, otherwise the controller-runtime's typedClient will use a different RateLimiter
	// for each API type.
	kubeConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(*cfg.ClientConnection.QPS, int(*cfg.ClientConnection.Burst))
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
	cacheOptions := []cache.Option{cache.WithPodsReadyTracking(blockForPodsReady(&cfg))}
	queueOptions := []queue.Option{queue.WithPodsReadyRequeuingTimestamp(podsReadyRequeuingTimestamp(&cfg))}
	if cfg.Resources != nil && len(cfg.Resources.ExcludeResourcePrefixes) > 0 {
		cacheOptions = append(cacheOptions, cache.WithExcludedResourcePrefixes(cfg.Resources.ExcludeResourcePrefixes))
		queueOptions = append(queueOptions, queue.WithExcludedResourcePrefixes(cfg.Resources.ExcludeResourcePrefixes))
	}
	if features.Enabled(features.ConfigurableResourceTransformations) && cfg.Resources != nil && len(cfg.Resources.Transformations) > 0 {
		cacheOptions = append(cacheOptions, cache.WithResourceTransformations(cfg.Resources.Transformations))
		queueOptions = append(queueOptions, queue.WithResourceTransformations(cfg.Resources.Transformations))
	}
	if cfg.FairSharing != nil {
		cacheOptions = append(cacheOptions, cache.WithFairSharing(cfg.FairSharing.Enable))
	}
	cCache := cache.New(mgr.GetClient(), cacheOptions...)
	queues := queue.NewManager(mgr.GetClient(), cCache, queueOptions...)

	ctx := ctrl.SetupSignalHandler()
	if err := setupIndexes(ctx, mgr, &cfg); err != nil {
		setupLog.Error(err, "Unable to setup indexes")
		os.Exit(1)
	}
	debugger.NewDumper(cCache, queues).ListenForSignal(ctx)

	serverVersionFetcher := setupServerVersionFetcher(mgr, kubeConfig)

	setupProbeEndpoints(mgr, certsReady)
	// Cert won't be ready until manager starts, so start a goroutine here which
	// will block until the cert is ready before setting up the controllers.
	// Controllers who register after manager starts will start directly.
	go setupControllers(ctx, mgr, cCache, queues, certsReady, &cfg, serverVersionFetcher)

	go queues.CleanUpOnContext(ctx)
	go cCache.CleanUpOnContext(ctx)

	if features.Enabled(features.VisibilityOnDemand) {
		go visibility.CreateAndStartVisibilityServer(ctx, queues)
	}

	setupScheduler(mgr, cCache, queues, &cfg)

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
	if features.Enabled(features.ProvisioningACC) {
		if !provisioning.ServerSupportsProvisioningRequest(mgr) {
			setupLog.Error(nil, "Provisioning Requests are not supported, skipped admission check controller setup")
		} else if err := provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
			setupLog.Error(err, "Could not setup provisioning indexer")
			os.Exit(1)
		}
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if err := tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
			setupLog.Error(err, "Could not setup TAS indexer")
			os.Exit(1)
		}
	}

	if features.Enabled(features.MultiKueue) {
		if err := multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), *cfg.Namespace); err != nil {
			setupLog.Error(err, "Could not setup multikueue indexer")
			os.Exit(1)
		}
	}

	opts := []jobframework.Option{
		jobframework.WithEnabledFrameworks(cfg.Integrations.Frameworks),
	}
	return jobframework.SetupIndexes(ctx, mgr.GetFieldIndexer(), opts...)
}

func setupControllers(ctx context.Context, mgr ctrl.Manager, cCache *cache.Cache, queues *queue.Manager, certsReady chan struct{}, cfg *configapi.Configuration, serverVersionFetcher *kubeversion.ServerVersionFetcher) {
	// The controllers won't work until the webhooks are operating, and the webhook won't work until the
	// certs are all in place.
	cert.WaitForCertsReady(setupLog, certsReady)

	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache, cfg); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", failedCtrl)
		os.Exit(1)
	}

	// setup provision admission check controller
	if features.Enabled(features.ProvisioningACC) && provisioning.ServerSupportsProvisioningRequest(mgr) {
		// A info message is added in setupIndexes if autoscaling is not supported by the cluster
		ctrl, err := provisioning.NewController(mgr.GetClient(), mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
		if err != nil {
			setupLog.Error(err, "Could not create the provisioning controller")
			os.Exit(1)
		}

		if err := ctrl.SetupWithManager(mgr); err != nil {
			setupLog.Error(err, "Could not setup provisioning controller")
			os.Exit(1)
		}
	}

	if features.Enabled(features.MultiKueue) {
		if err := multikueue.SetupControllers(mgr, *cfg.Namespace,
			multikueue.WithGCInterval(cfg.MultiKueue.GCInterval.Duration),
			multikueue.WithOrigin(ptr.Deref(cfg.MultiKueue.Origin, configapi.DefaultMultiKueueOrigin)),
			multikueue.WithWorkerLostTimeout(cfg.MultiKueue.WorkerLostTimeout.Duration),
		); err != nil {
			setupLog.Error(err, "Could not setup MultiKueue controller")
			os.Exit(1)
		}
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if failedCtrl, err := tas.SetupControllers(mgr, queues, cCache, cfg); err != nil {
			setupLog.Error(err, "Could not setup TAS controller", "controller", failedCtrl)
			os.Exit(1)
		}
	}

	if failedWebhook, err := webhooks.Setup(mgr); err != nil {
		setupLog.Error(err, "Unable to create webhook", "webhook", failedWebhook)
		os.Exit(1)
	}

	opts := []jobframework.Option{
		jobframework.WithManageJobsWithoutQueueName(cfg.ManageJobsWithoutQueueName),
		jobframework.WithWaitForPodsReady(cfg.WaitForPodsReady),
		jobframework.WithKubeServerVersion(serverVersionFetcher),
		jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), cfg.Integrations.PodOptions),
		jobframework.WithEnabledFrameworks(cfg.Integrations.Frameworks),
		jobframework.WithEnabledExternalFrameworks(cfg.Integrations.ExternalFrameworks),
		jobframework.WithManagerName(constants.KueueName),
		jobframework.WithLabelKeysToCopy(cfg.Integrations.LabelKeysToCopy),
		jobframework.WithCache(cCache),
		jobframework.WithQueues(queues),
	}
	if err := jobframework.SetupControllers(ctx, mgr, setupLog, opts...); err != nil {
		setupLog.Error(err, "Unable to create controller or webhook", "kubernetesVersion", serverVersionFetcher.GetServerVersion())
		os.Exit(1)
	}
}

// setupProbeEndpoints registers the health endpoints
func setupProbeEndpoints(mgr ctrl.Manager, certsReady <-chan struct{}) {
	defer setupLog.Info("Probe endpoints are configured on healthz and readyz")

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
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
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}

func setupScheduler(mgr ctrl.Manager, cCache *cache.Cache, queues *queue.Manager, cfg *configapi.Configuration) {
	sched := scheduler.New(
		queues,
		cCache,
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.AdmissionName),
		scheduler.WithPodsReadyRequeuingTimestamp(podsReadyRequeuingTimestamp(cfg)),
		scheduler.WithFairSharing(cfg.FairSharing),
	)
	if err := mgr.Add(sched); err != nil {
		setupLog.Error(err, "Unable to add scheduler to manager")
		os.Exit(1)
	}
}

func setupServerVersionFetcher(mgr ctrl.Manager, kubeConfig *rest.Config) *kubeversion.ServerVersionFetcher {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(kubeConfig)
	if err != nil {
		setupLog.Error(err, "Unable to create the discovery client")
		os.Exit(1)
	}

	serverVersionFetcher := kubeversion.NewServerVersionFetcher(discoveryClient)

	if err := mgr.Add(serverVersionFetcher); err != nil {
		setupLog.Error(err, "Unable to add server version fetcher to manager")
		os.Exit(1)
	}

	if err := serverVersionFetcher.FetchServerVersion(); err != nil {
		setupLog.Error(err, "failed to fetch kubernetes server version")
		os.Exit(1)
	}

	return serverVersionFetcher
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
