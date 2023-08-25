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
	"fmt"
	"os"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.

	_ "k8s.io/client-go/plugin/pkg/client/auth"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/noop"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/scheduler"
	"sigs.k8s.io/kueue/pkg/util/cert"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/useragent"
	"sigs.k8s.io/kueue/pkg/version"
	"sigs.k8s.io/kueue/pkg/webhooks"

	// Ensure linking of the job controllers.
	_ "sigs.k8s.io/kueue/pkg/controller/jobs"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1.AddToScheme(scheme))

	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(configapi.AddToScheme(scheme))
	// Add any additional framework integration types.
	utilruntime.Must(
		jobframework.ForEachIntegration(func(_ string, cb jobframework.IntegrationCallbacks) error {
			if cb.AddToScheme != nil {
				return cb.AddToScheme(scheme)
			}
			return nil
		}),
	)
	// +kubebuilder:scaffold:scheme
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
	kubeConfig.QPS = *cfg.ClientConnection.QPS
	kubeConfig.Burst = int(*cfg.ClientConnection.Burst)
	setupLog.V(2).Info("K8S Client", "qps", kubeConfig.QPS, "burst", kubeConfig.Burst)
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

	cCache := cache.New(mgr.GetClient(), cache.WithPodsReadyTracking(blockForPodsReady(&cfg)))
	queues := setupQueueManager(mgr, cCache, &cfg)

	ctx := ctrl.SetupSignalHandler()
	if err := setupIndexes(ctx, mgr, &cfg); err != nil {
		setupLog.Error(err, "Unable to setup indexes")
		os.Exit(1)
	}

	serverVersionFetcher := setupServerVersionFetcher(mgr, kubeConfig)

	setupProbeEndpoints(mgr)
	// Cert won't be ready until manager starts, so start a goroutine here which
	// will block until the cert is ready before setting up the controllers.
	// Controllers who register after manager starts will start directly.
	go setupControllers(mgr, cCache, queues, certsReady, &cfg, serverVersionFetcher)

	go func() {
		queues.CleanUpOnContext(ctx)
	}()
	go func() {
		cCache.CleanUpOnContext(ctx)
	}()

	setupScheduler(mgr, cCache, queues)

	setupLog.Info("Starting manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Could not run manager")
		os.Exit(1)
	}
}

func setupQueueManager(mgr ctrl.Manager, cCache *cache.Cache, cfg *configapi.Configuration) *queue.Manager {
	queues := queue.NewManager(
		mgr.GetClient(),
		cCache,
		queue.WithQueueVisibilityUpdateInterval(cfg.QueueVisibility.UpdateIntervalSeconds),
		queue.WithQueueVisibilityClusterQueuesMaxCount(cfg.QueueVisibility.ClusterQueues.MaxCount),
	)
	if cfg.QueueVisibility.ClusterQueues.MaxCount != 0 {
		if err := mgr.Add(queues); err != nil {
			setupLog.Error(err, "Unable to add queue manager to manager")
			os.Exit(1)
		}
	}
	return queues
}

func setupIndexes(ctx context.Context, mgr ctrl.Manager, cfg *configapi.Configuration) error {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	if err != nil {
		return err
	}

	err = jobframework.ForEachIntegration(func(name string, cb jobframework.IntegrationCallbacks) error {
		if isFrameworkEnabled(cfg, name) {
			if err := cb.SetupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
				return fmt.Errorf("integration %s: %w", name, err)
			}
		}
		return nil
	})
	return err
}

func setupControllers(
	mgr ctrl.Manager,
	cCache *cache.Cache,
	queues *queue.Manager,
	certsReady chan struct{},
	cfg *configapi.Configuration,
	serverVersionFetcher *kubeversion.ServerVersionFetcher) {
	// The controllers won't work until the webhooks are operating, and the webhook won't work until the
	// certs are all in place.
	setupLog.Info("Waiting for certificate generation to complete")
	<-certsReady
	setupLog.Info("Certs ready")

	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache, cfg); err != nil {
		setupLog.Error(err, "Unable to create controller", "controller", failedCtrl)
		os.Exit(1)
	}
	manageJobsWithoutQueueName := cfg.ManageJobsWithoutQueueName

	if failedWebhook, err := webhooks.Setup(mgr); err != nil {
		setupLog.Error(err, "Unable to create webhook", "webhook", failedWebhook)
		os.Exit(1)
	}

	opts := []jobframework.Option{
		jobframework.WithManageJobsWithoutQueueName(manageJobsWithoutQueueName),
		jobframework.WithWaitForPodsReady(waitForPodsReady(cfg)),
		jobframework.WithKubeServerVersion(serverVersionFetcher),
	}
	err := jobframework.ForEachIntegration(func(name string, cb jobframework.IntegrationCallbacks) error {
		log := setupLog.WithValues("jobFrameworkName", name)

		if isFrameworkEnabled(cfg, name) {
			gvk, err := apiutil.GVKForObject(cb.JobType, mgr.GetScheme())
			if err != nil {
				return err
			}
			if _, err = mgr.GetRESTMapper().RESTMapping(gvk.GroupKind(), gvk.Version); err != nil {
				// TODO: If the below PR is released, we need to change a way to check if the GVK is registered.
				// REF: https://github.com/kubernetes-sigs/controller-runtime/pull/2425
				// if !meta.IsNoMatchError(err) {
				//   return err
				// }
				var NoMatchingErr *discovery.ErrGroupDiscoveryFailed
				if !meta.IsNoMatchError(err) && !errors.As(err, &NoMatchingErr) {
					return err
				}
				log.Info("No matching API server for job framework, skip to create controller and webhook")
			} else {
				if err = cb.NewReconciler(
					mgr.GetClient(),
					mgr.GetEventRecorderFor(fmt.Sprintf("%s-%s-controller", name, constants.KueueName)),
					opts...,
				).SetupWithManager(mgr); err != nil {
					log.Error(err, "Unable to create controller")
					return err
				}
				if err = cb.SetupWebhook(mgr, opts...); err != nil {
					log.Error(err, "Unable to create webhook")
					return err
				}
				return nil
			}
		}
		if err := noop.SetupWebhook(mgr, cb.JobType); err != nil {
			log.Error(err, "Unable to create noop webhook")
			return err
		}
		return nil
	})
	if err != nil {
		os.Exit(1)
	}
	// +kubebuilder:scaffold:builder
}

// setupProbeEndpoints registers the health endpoints
func setupProbeEndpoints(mgr ctrl.Manager) {
	defer setupLog.Info("Probe endpoints are configured on healthz and readyz")

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}
}

func setupScheduler(mgr ctrl.Manager, cCache *cache.Cache, queues *queue.Manager) {
	sched := scheduler.New(
		queues,
		cCache,
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.AdmissionName),
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

	return serverVersionFetcher
}

func blockForPodsReady(cfg *configapi.Configuration) bool {
	return waitForPodsReady(cfg) && cfg.WaitForPodsReady.BlockAdmission != nil && *cfg.WaitForPodsReady.BlockAdmission
}

func waitForPodsReady(cfg *configapi.Configuration) bool {
	return cfg.WaitForPodsReady != nil && cfg.WaitForPodsReady.Enable
}

func apply(configFile string) (ctrl.Options, configapi.Configuration, error) {
	options, cfg, err := config.Load(scheme, configFile)
	if err != nil {
		return options, cfg, err
	}

	if cfg.Integrations != nil {
		var errorlist field.ErrorList
		availableFrameworks := jobframework.GetIntegrationsList()
		path := field.NewPath("integrations", "frameworks")
		for _, framework := range cfg.Integrations.Frameworks {
			if _, found := jobframework.GetIntegration(framework); !found {
				errorlist = append(errorlist, field.NotSupported(path, framework, availableFrameworks))
			}
		}
		if len(errorlist) > 0 {
			err := errorlist.ToAggregate()
			return options, cfg, err
		}
	}

	cfgStr, err := config.Encode(scheme, &cfg)
	if err != nil {
		return options, cfg, err
	}
	setupLog.Info("Successfully loaded configuration", "config", cfgStr)

	return options, cfg, nil
}

func isFrameworkEnabled(cfg *configapi.Configuration, name string) bool {
	for _, framework := range cfg.Integrations.Frameworks {
		if framework == name {
			return true
		}
	}
	return false
}
