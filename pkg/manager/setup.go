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
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics/filters"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
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
	"sigs.k8s.io/kueue/pkg/webhooks"
)

// getConfig returns the Kubernetes REST config. Defined as a var to allow tests to override.
var getConfig = ctrl.GetConfigOrDie

// ManagerSetup holds all the components needed to run the Kueue manager
type ManagerSetup struct {
	Mgr                  ctrl.Manager
	CCache               *schdcache.Cache
	Queues               *qcache.Manager
	CertsReady           chan struct{}
	ServerVersionFetcher *kubeversion.ServerVersionFetcher
	Config               *Config
}

// SetupManager initializes and configures all components needed for the Kueue manager.
// This function extracts the setup logic from main() to make it more testable.
func SetupManager(ctx context.Context, configFile string, featureGates string) (*ManagerSetup, error) {
	managerConfig := NewConfig()

	err := managerConfig.Apply(configFile)
	if err != nil {
		return nil, fmt.Errorf("unable to load the configuration: %w", err)
	}

	if err := config.ValidateFeatureGates(featureGates, managerConfig.Apiconf.FeatureGates); err != nil {
		return nil, fmt.Errorf("conflicting feature gates detected: %w", err)
	}

	if featureGates != "" {
		if err := utilfeature.DefaultMutableFeatureGate.Set(featureGates); err != nil {
			return nil, fmt.Errorf("unable to set flag gates for known features: %w", err)
		}
		managerConfig.SetupLog.V(2).Info("Feature gates configured from command line", "gates", featureGates)
	} else if len(managerConfig.Apiconf.FeatureGates) > 0 {
		if err := utilfeature.DefaultMutableFeatureGate.SetFromMap(managerConfig.Apiconf.FeatureGates); err != nil {
			return nil, fmt.Errorf("unable to set flag gates for known features: %w", err)
		}
		managerConfig.SetupLog.V(2).Info("Feature gates configured from config file", "gates", managerConfig.Apiconf.FeatureGates)
	}

	// Setup metrics server options
	metricsServerOptions := metricsserver.Options{
		BindAddress:    managerConfig.Apiconf.Metrics.BindAddress,
		SecureServing:  true,
		FilterProvider: filters.WithAuthenticationAndAuthorization,
	}

	if managerConfig.Apiconf.InternalCertManagement == nil || !*managerConfig.Apiconf.InternalCertManagement.Enable {
		metricsCertPath := "/etc/kueue/metrics/certs"
		managerConfig.SetupLog.Info("Initializing metrics certificate watcher using provided certificates",
			"metrics-cert-path", metricsCertPath)

		var err error
		metricsCertWatcher, err := certwatcher.New(
			filepath.Join(metricsCertPath, "tls.crt"),
			filepath.Join(metricsCertPath, "tls.key"),
		)
		if err != nil {
			return nil, fmt.Errorf("unable to initialize metrics certificate watcher: %w", err)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}
	managerConfig.Options.Metrics = metricsServerOptions

	metrics.Register()

	kubeConfig := getConfig()
	if kubeConfig.UserAgent == "" {
		kubeConfig.UserAgent = useragent.Default()
	}

	// Set the RateLimiter here, otherwise the controller-runtime's typedClient will use a different RateLimiter
	// for each API type.
	// When the controller-runtime > 0.21, the client-side ratelimiting will be disabled by default.
	// The following QPS negative value check allows us to disable the client-side ratelimiting.
	if *managerConfig.Apiconf.ClientConnection.QPS >= 0.0 {
		kubeConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(*managerConfig.Apiconf.ClientConnection.QPS, int(*managerConfig.Apiconf.ClientConnection.Burst))
	}
	managerConfig.SetupLog.V(2).Info("K8S Client", "qps", *managerConfig.Apiconf.ClientConnection.QPS, "burst", *managerConfig.Apiconf.ClientConnection.Burst)

	mgr, err := ctrl.NewManager(kubeConfig, managerConfig.Options)
	if err != nil {
		return nil, fmt.Errorf("unable to start manager: %w", err)
	}

	certsReady := make(chan struct{})
	if managerConfig.Apiconf.InternalCertManagement != nil && *managerConfig.Apiconf.InternalCertManagement.Enable {
		managerConfig.SetupLog.Info("Using internal certificate management")
		if err = cert.ManageCerts(mgr, managerConfig.Apiconf, certsReady); err != nil {
			return nil, fmt.Errorf("unable to set up cert rotation: %w", err)
		}
	} else {
		managerConfig.SetupLog.Info("Using external certificate management")
		close(certsReady)
	}

	// Setup cache and queue options
	cacheOptions := []schdcache.Option{schdcache.WithPodsReadyTracking(managerConfig.BlockForPodsReady())}
	queueOptions := []qcache.Option{qcache.WithPodsReadyRequeuingTimestamp(managerConfig.PodsReadyRequeuingTimestamp())}

	if managerConfig.Apiconf.Resources != nil && len(managerConfig.Apiconf.Resources.ExcludeResourcePrefixes) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithExcludedResourcePrefixes(managerConfig.Apiconf.Resources.ExcludeResourcePrefixes))
		queueOptions = append(queueOptions, qcache.WithExcludedResourcePrefixes(managerConfig.Apiconf.Resources.ExcludeResourcePrefixes))
	}

	if features.Enabled(features.ConfigurableResourceTransformations) && managerConfig.Apiconf.Resources != nil && len(managerConfig.Apiconf.Resources.Transformations) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithResourceTransformations(managerConfig.Apiconf.Resources.Transformations))
		queueOptions = append(queueOptions, qcache.WithResourceTransformations(managerConfig.Apiconf.Resources.Transformations))
	}

	if features.Enabled(features.DynamicResourceAllocation) && managerConfig.Apiconf.Resources != nil && len(managerConfig.Apiconf.Resources.DeviceClassMappings) > 0 {
		if err := dra.CreateMapperFromConfiguration(managerConfig.Apiconf.Resources.DeviceClassMappings); err != nil {
			return nil, fmt.Errorf("failed to initialize DRA mapper from configuration: %w", err)
		}
		managerConfig.SetupLog.Info("DRA mapper initialized from configuration")
	}

	if managerConfig.Apiconf.FairSharing != nil {
		cacheOptions = append(cacheOptions, schdcache.WithFairSharing(managerConfig.Apiconf.FairSharing.Enable))
	}

	if managerConfig.Apiconf.AdmissionFairSharing != nil {
		queueOptions = append(queueOptions, qcache.WithAdmissionFairSharing(managerConfig.Apiconf.AdmissionFairSharing))
		cacheOptions = append(cacheOptions, schdcache.WithAdmissionFairSharing(managerConfig.Apiconf.AdmissionFairSharing))
	}

	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)
	queues := qcache.NewManager(mgr.GetClient(), cCache, queueOptions...)
	managerConfig.SetupLog.V(2).Info("Scheduler cache and queue manager initialized")

	if err := managerConfig.SetupIndexes(ctx, mgr); err != nil {
		return nil, fmt.Errorf("unable to setup indexes: %w", err)
	}
	managerConfig.SetupLog.Info("Field indexes configured")

	debugger.NewDumper(cCache, queues).ListenForSignal(ctx)

	serverVersionFetcher, err := managerConfig.SetupServerVersionFetcher(mgr, kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("unable to setup server version fetcher: %w", err)
	}
	managerConfig.SetupLog.Info("Server version fetched", "version", serverVersionFetcher.GetServerVersion())

	if err := managerConfig.SetupProbeEndpoints(mgr, certsReady); err != nil {
		return nil, fmt.Errorf("unable to setup probe endpoints: %w", err)
	}

	if err := managerConfig.SetupScheduler(mgr, cCache, queues); err != nil {
		return nil, fmt.Errorf("could not setup scheduler: %w", err)
	}
	managerConfig.SetupLog.Info("Scheduler configured")

	managerConfig.SetupLog.Info("Manager setup completed successfully",
		"namespace", *managerConfig.Apiconf.Namespace,
		"leaderElection", managerConfig.Options.LeaderElection)

	return &ManagerSetup{
		Mgr:                  mgr,
		CCache:               cCache,
		Queues:               queues,
		CertsReady:           certsReady,
		ServerVersionFetcher: serverVersionFetcher,
		Config:               managerConfig,
	}, nil
}

// SetupIndexes sets up all the necessary field indexes for the manager
func (c *Config) SetupIndexes(ctx context.Context, mgr ctrl.Manager) error {
	err := indexer.Setup(ctx, mgr.GetFieldIndexer())
	if err != nil {
		return err
	}

	// setup provision admission check controller indexes
	if err := provisioning.ServerSupportsProvisioningRequest(mgr); err != nil {
		c.SetupLog.Error(err, "Skipping admission check controller setup: Provisioning Requests not supported (Possible cause: missing or unsupported cluster-autoscaler)")
	} else if err := provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("could not setup provisioning indexer: %w", err)
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if err := tasindexer.SetupIndexes(ctx, mgr.GetFieldIndexer()); err != nil {
			return fmt.Errorf("could not setup TAS indexer: %w", err)
		}
	}

	if features.Enabled(features.MultiKueue) {
		if err := multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), *c.Apiconf.Namespace); err != nil {
			return fmt.Errorf("could not setup multikueue indexer: %w", err)
		}
	}

	opts := []jobframework.Option{
		jobframework.WithEnabledFrameworks(c.Apiconf.Integrations.Frameworks),
	}
	return jobframework.SetupIndexes(ctx, mgr.GetFieldIndexer(), opts...)
}

// SetupControllers sets up all the controllers for the manager
func (c *Config) SetupControllers(ctx context.Context, mgr ctrl.Manager, cCache *schdcache.Cache, queues *qcache.Manager, certsReady chan struct{}, serverVersionFetcher *kubeversion.ServerVersionFetcher) error {
	// The controllers won't work until the webhooks are operating, and the webhook won't work until the
	// certs are all in place.
	cert.WaitForCertsReady(c.SetupLog, certsReady)

	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache, &c.Apiconf); err != nil {
		return fmt.Errorf("unable to create controller %s: %w", failedCtrl, err)
	}

	// setup provision admission check controller
	if err := provisioning.ServerSupportsProvisioningRequest(mgr); err != nil {
		c.SetupLog.Info("Skipping provisioning controller setup: Provisioning Requests not supported (Possible cause: missing or unsupported cluster-autoscaler)")
	} else {
		ctrlr, err := provisioning.NewController(mgr.GetClient(), mgr.GetEventRecorderFor("kueue-provisioning-request-controller"))
		if err != nil {
			return fmt.Errorf("could not create the provisioning controller: %w", err)
		}
		if err := ctrlr.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("could not setup provisioning controller: %w", err)
		}
	}

	if features.Enabled(features.MultiKueue) {
		adapters, err := jobframework.GetMultiKueueAdapters(sets.New(c.Apiconf.Integrations.Frameworks...))
		if err != nil {
			return fmt.Errorf("could not get the enabled multikueue adapters: %w", err)
		}
		if err := multikueue.SetupControllers(mgr, *c.Apiconf.Namespace,
			multikueue.WithGCInterval(c.Apiconf.MultiKueue.GCInterval.Duration),
			multikueue.WithOrigin(ptr.Deref(c.Apiconf.MultiKueue.Origin, configapi.DefaultMultiKueueOrigin)),
			multikueue.WithWorkerLostTimeout(c.Apiconf.MultiKueue.WorkerLostTimeout.Duration),
			multikueue.WithAdapters(adapters),
			multikueue.WithDispatcherName(ptr.Deref(c.Apiconf.MultiKueue.DispatcherName, configapi.MultiKueueDispatcherModeAllAtOnce)),
		); err != nil {
			return fmt.Errorf("could not setup MultiKueue controller: %w", err)
		}
		if failedDispatcher, err := dispatcher.SetupControllers(mgr, &c.Apiconf, ptr.Deref(c.Apiconf.MultiKueue.DispatcherName, configapi.MultiKueueDispatcherModeAllAtOnce)); err != nil {
			return fmt.Errorf("could not setup Dispatcher controller %q for MultiKueue: %w", failedDispatcher, err)
		}
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if failedCtrl, err := tas.SetupControllers(mgr, queues, cCache, &c.Apiconf); err != nil {
			return fmt.Errorf("could not setup TAS controller %s: %w", failedCtrl, err)
		}
	}

	if failedWebhook, err := webhooks.Setup(mgr); err != nil {
		return fmt.Errorf("unable to create webhook %s: %w", failedWebhook, err)
	}

	opts := []jobframework.Option{
		jobframework.WithManageJobsWithoutQueueName(c.Apiconf.ManageJobsWithoutQueueName),
		jobframework.WithWaitForPodsReady(c.Apiconf.WaitForPodsReady),
		jobframework.WithKubeServerVersion(serverVersionFetcher),
		jobframework.WithEnabledFrameworks(c.Apiconf.Integrations.Frameworks),
		jobframework.WithEnabledExternalFrameworks(c.Apiconf.Integrations.ExternalFrameworks),
		jobframework.WithManagerName(constants.KueueName),
		jobframework.WithLabelKeysToCopy(c.Apiconf.Integrations.LabelKeysToCopy),
		jobframework.WithCache(cCache),
		jobframework.WithQueues(queues),
		jobframework.WithObjectRetentionPolicies(c.Apiconf.ObjectRetentionPolicies),
	}
	if c.Apiconf.Integrations.PodOptions != nil {
		opts = append(opts, jobframework.WithIntegrationOptions(corev1.SchemeGroupVersion.WithKind("Pod").String(), c.Apiconf.Integrations.PodOptions))
	}
	nsSelector, err := metav1.LabelSelectorAsSelector(c.Apiconf.ManagedJobsNamespaceSelector)
	if err != nil {
		return fmt.Errorf("failed to parse managedJobsNamespaceSelector: %w", err)
	}
	opts = append(opts, jobframework.WithManagedJobsNamespaceSelector(nsSelector))

	if err := jobframework.SetupControllers(ctx, mgr, c.SetupLog, opts...); err != nil {
		return fmt.Errorf("unable to create controller or webhook for kubernetesVersion %v: %w", serverVersionFetcher.GetServerVersion(), err)
	}
	return nil
}

// SetupProbeEndpoints registers the health endpoints
func (c *Config) SetupProbeEndpoints(mgr ctrl.Manager, certsReady <-chan struct{}) error {
	defer c.SetupLog.Info("Probe endpoints are configured on healthz and readyz")

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

// SetupScheduler sets up the scheduler for the manager
func (c *Config) SetupScheduler(mgr ctrl.Manager, cCache *schdcache.Cache, queues *qcache.Manager) error {
	sched := scheduler.New(
		queues,
		cCache,
		mgr.GetClient(),
		mgr.GetEventRecorderFor(constants.AdmissionName),
		scheduler.WithPodsReadyRequeuingTimestamp(c.PodsReadyRequeuingTimestamp()),
		scheduler.WithFairSharing(c.Apiconf.FairSharing),
		scheduler.WithAdmissionFairSharing(c.Apiconf.AdmissionFairSharing),
	)
	if err := mgr.Add(sched); err != nil {
		return fmt.Errorf("unable to add scheduler to manager: %w", err)
	}
	return nil
}

// SetupServerVersionFetcher sets up the server version fetcher for the manager
func (c *Config) SetupServerVersionFetcher(mgr ctrl.Manager, kubeConfig *rest.Config) (*kubeversion.ServerVersionFetcher, error) {
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
