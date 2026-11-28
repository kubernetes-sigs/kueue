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

package app

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	zaplog "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	resourceapi "k8s.io/api/resource/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	capacitybufferv1beta1 "k8s.io/autoscaler/cluster-autoscaler/apis/capacitybuffer/autoscaling.x-k8s.io/v1beta1"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1"
	"k8s.io/client-go/discovery"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	"k8s.io/utils/ptr"
	inventoryv1alpha2 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/certwatcher"
	clientconfig "sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/config"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/capacitybuffer"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue/externalframeworks"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/concurrentadmission"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/elasticjobs"
	"sigs.k8s.io/kueue/pkg/controller/failurerecovery"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/controller/unscheduledpods"
	"sigs.k8s.io/kueue/pkg/controller/workloaddispatcher"
	"sigs.k8s.io/kueue/pkg/debugger"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/util/cert"
	utildra "sigs.k8s.io/kueue/pkg/util/dra"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utillogging "sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/tlsconfig"
	"sigs.k8s.io/kueue/pkg/util/useragent"
	"sigs.k8s.io/kueue/pkg/util/waitforpodsready"
	"sigs.k8s.io/kueue/pkg/version"
	"sigs.k8s.io/kueue/pkg/visibility"
	"sigs.k8s.io/kueue/pkg/webhooks"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	// to ensure that exec-entrypoint and run can make use of them.
	_ "k8s.io/client-go/plugin/pkg/client/auth"
)

var (
	scheme              = runtime.NewScheme()
	setupLog            = ctrl.Log.WithName("setup")
	integrationManager  = jobs.NewIntegrationManager()
	registerMetricsOnce sync.Once
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(schedulingv1.AddToScheme(scheme))
	utilruntime.Must(resourceapi.AddToScheme(scheme))

	utilruntime.Must(kueue.AddToScheme(scheme))
	utilruntime.Must(kueuealpha.AddToScheme(scheme))
	utilruntime.Must(configapi.AddToScheme(scheme))
	utilruntime.Must(autoscaling.AddToScheme(scheme))
	utilruntime.Must(capacitybufferv1beta1.AddToScheme(scheme))
	utilruntime.Must(inventoryv1alpha2.AddToScheme(scheme))
	// Add any additional framework integration types.
	utilruntime.Must(
		integrationManager.ForEachIntegration(func(_ string, cb jobframework.IntegrationCallbacks) error {
			if cb.AddToScheme != nil {
				return cb.AddToScheme(scheme)
			}
			return nil
		}),
	)
}

// Run executes the Kueue controller manager startup and blocks until ctx is canceled.
func Run(ctx context.Context, args []string) error {
	return runWithOutput(ctx, args, os.Stderr)
}

func runWithOutput(ctx context.Context, args []string, out io.Writer) error {
	configFile, featureGates, err := parseFlagsAndInitLogger(args, out)
	if err != nil {
		return err
	}

	options, cfg, err := loadAndValidateConfig(configFile, featureGates)
	if err != nil {
		return err
	}

	parsedTLSConfig, metricsCertWatcher, lqMetrics, customLabels, err := configureMetricsAndWebhooks(&options, &cfg)
	if err != nil {
		return err
	}

	kubeConfig, err := buildKubeConfig(&cfg)
	if err != nil {
		return err
	}

	if leaderElectionEnabled(&cfg) {
		// Keep the leader election lease client off the shared RateLimiter, so that a
		// burst of controller requests cannot delay lease renewals past renewDeadline.
		config.SetLeaderElectionConfig(&options, kubeConfig, &cfg)
	}

	// Use setupCtx for startup-only calls so setupLog does not leak into long-lived runtime components using ctx.
	setupCtx := ctrl.LoggerInto(ctx, setupLog)
	// Bootstrap certificates before creating the main manager
	// This ensures certs are ready and CA bundles are injected into conversion CRDs
	if cfg.InternalCertManagement != nil && *cfg.InternalCertManagement.Enable {
		if err := cert.BootstrapCerts(ctx, kubeConfig, cfg); err != nil {
			return fmt.Errorf("unable to bootstrap certificates: %w", err)
		}
	}

	var clusterProfileCRDPresent bool
	if features.Enabled(features.MultiKueueClusterProfile) {
		var err error
		if clusterProfileCRDPresent, err = config.ClusterProfileCRDPresent(setupCtx, kubeConfig); err != nil {
			return fmt.Errorf("unable to check ClusterProfile CRD: %w", err)
		}
		if clusterProfileCRDPresent {
			config.ConfigureClusterProfileCache(&options, cfg)
		} else {
			setupLog.V(1).Info("Skipping MultiKueue ClusterProfile setup as the ClusterProfile CRD is not installed")
		}
	}

	mgr, err := ctrl.NewManager(kubeConfig, options)
	if err != nil {
		return fmt.Errorf("unable to start manager: %w", err)
	}

	roleTracker := setupRoleTracker(ctx, mgr, &cfg)

	if cfg.InternalCertManagement != nil && *cfg.InternalCertManagement.Enable {
		if err = cert.ManageCerts(mgr, cfg); err != nil {
			return fmt.Errorf("unable to set up cert rotation: %w", err)
		}
		if err = cert.ManageVisibilityCerts(mgr, cfg); err != nil {
			return fmt.Errorf("unable to set up visibility cert rotation: %w", err)
		}
	}
	resourceFormatter := resources.NewResourceFormatter()
	cacheOptions := []schdcache.Option{
		schdcache.WithPodsReadyTracking(blockForPodsReady(&cfg)),
		schdcache.WithRoleTracker(roleTracker),
		schdcache.WithResourceMetrics(cfg.Metrics.EnableClusterQueueResources),
		schdcache.WithCustomLabels(customLabels),
		schdcache.WithLocalQueueMetrics(lqMetrics),
		schdcache.WithResourceFormatter(resourceFormatter),
	}
	queueOptions := []qcache.Option{
		qcache.WithPodsReadyRequeuingTimestamp(podsReadyRequeuingTimestamp(&cfg)),
		qcache.WithRoleTracker(roleTracker),
		qcache.WithCustomLabels(customLabels),
		qcache.WithLocalQueueMetrics(lqMetrics),
		qcache.WithResourceMetrics(cfg.Metrics.EnableClusterQueueResources),
		qcache.WithResourceFormatter(resourceFormatter),
	}
	if cfg.Resources != nil && len(cfg.Resources.ExcludeResourcePrefixes) > 0 {
		cacheOptions = append(
			cacheOptions,
			schdcache.WithExcludedResourcePrefixes(cfg.Resources.ExcludeResourcePrefixes),
		)
		queueOptions = append(queueOptions, qcache.WithExcludedResourcePrefixes(cfg.Resources.ExcludeResourcePrefixes))
	}
	if cfg.Resources != nil && len(cfg.Resources.Transformations) > 0 {
		cacheOptions = append(cacheOptions, schdcache.WithResourceTransformations(cfg.Resources.Transformations))
		queueOptions = append(queueOptions, qcache.WithResourceTransformations(cfg.Resources.Transformations))
	}
	var draMapper *dra.ResourceMapper
	var draBackedResources *dra.ExtendedResourceCache
	if features.Enabled(features.KueueDRAIntegration) {
		draMapper = dra.NewResourceMapper()
		draBackedResources = dra.NewExtendedResourceCache()
		if cfg.Resources != nil && len(cfg.Resources.DeviceClassMappings) > 0 {
			if err := draMapper.PopulateFromConfiguration(cfg.Resources.DeviceClassMappings); err != nil {
				return fmt.Errorf("failed to initialize DRA mapper from configuration: %w", err)
			}
			setupLog.V(1).Info("DRA mapper initialized from configuration")
			for _, resourceName := range draMapper.CounterBasedResourceNames() {
				resourceFormatter.RegisterBinaryFormattedResource(resourceName)
			}
			for _, resourceName := range draMapper.CapacityBasedResourceNames() {
				resourceFormatter.RegisterBinaryFormattedResource(resourceName)
			}
		}
		if features.Enabled(features.KueueDRAExcludeDeviceClasses) && cfg.Resources != nil {
			draMapper.SetExcludedDeviceClasses(cfg.Resources.ExcludeDeviceClassNames)
		}
	}
	if cfg.FairSharing != nil {
		cacheOptions = append(cacheOptions, schdcache.WithFairSharing(fairsharing.Enabled(cfg.FairSharing)))
	}
	if cfg.AdmissionFairSharing != nil {
		queueOptions = append(queueOptions, qcache.WithAdmissionFairSharing(cfg.AdmissionFairSharing))
		cacheOptions = append(cacheOptions, schdcache.WithAdmissionFairSharing(cfg.AdmissionFairSharing))
	}
	if features.Enabled(features.SchedulerLibraryIntegration) {
		simulatorFactory, err := was.NewWASSimulatorFactory(ctx, mgr.GetConfig())
		if err != nil {
			return fmt.Errorf("failed to initialize scheduling simulator: %w", err)
		}
		cacheOptions = append(cacheOptions, schdcache.WithSimulatorFactory(simulatorFactory))
	}
	if draBackedResources != nil {
		cacheOptions = append(cacheOptions, schdcache.WithDRABackedResources(draBackedResources))
	}
	if features.Enabled(features.KueueDRAIntegrationDeviceTaints) {
		// Only a discovery failure errors. Carrying on unregistered would let a scheduling
		// cycle block on the informer once discovery recovered.
		served, err := utildra.RegisterDeviceTaintRuleInformer(setupCtx, mgr)
		if err != nil {
			return fmt.Errorf("unable to watch DeviceTaintRules: %w", err)
		}
		cacheOptions = append(cacheOptions, schdcache.WithDeviceTaintRules(served))
	}
	cCache := schdcache.New(mgr.GetClient(), cacheOptions...)

	// setup inadmissible workload requeuer
	requeuer := qcache.NewRequeuer(qcache.WithInadmissibleWorkloadRequeuerConfig(cfg.InadmissibleWorkloadRequeuer))
	if err := mgr.Add(requeuer); err != nil {
		return fmt.Errorf("unable to add workloadRequeuer to manager: %w", err)
	}

	preemptionExpectations := preemptexpectations.New()
	queueOptions = append(queueOptions, qcache.WithPreemptionExpectations(preemptionExpectations))
	if draBackedResources != nil {
		queueOptions = append(queueOptions, qcache.WithDRABackedResources(draBackedResources))
	}
	queues := qcache.NewManager(mgr.GetClient(), cCache, requeuer, queueOptions...)

	var preemptionLimitTracker *limits.PreemptionLimitTracker
	if features.Enabled(features.ConfigurablePreemptionLimits) {
		preemptionLimitTracker = limits.NewPreemptionLimitTracker()
	}

	resourceSliceAPIAvailable := utildra.CheckResourceSliceAPIAvailable(mgr)

	if err := setupIndexes(setupCtx, mgr, &cfg, integrationManager, resourceSliceAPIAvailable); err != nil {
		return fmt.Errorf("unable to setup indexes: %w", err)
	}
	debugger.NewDumper(cCache, queues).ListenForSignal(ctx)

	serverVersionFetcher, err := setupServerVersionFetcher(mgr, kubeConfig)
	if err != nil {
		return fmt.Errorf("unable to setup server version fetcher: %w", err)
	}

	if metricsCertWatcher != nil {
		if err := mgr.Add(metricsCertWatcher); err != nil {
			return fmt.Errorf("unable to start metrics certificate watcher: %w", err)
		}
	}

	if err := setupProbeEndpoints(mgr); err != nil {
		return fmt.Errorf("unable to setup probe endpoints: %w", err)
	}

	if roleTracker != nil {
		roleTracker.OnElected(func() {
			metrics.ClearGaugeMetricsForRole(roletracker.RoleFollower)
			cCache.ResyncGaugeMetrics(setupLog)
			queues.ResyncGaugeMetrics()
		})
	}

	controllerOpts := core.SetupControllersOpts{
		RoleTracker:               roleTracker,
		PreemptionExpectations:    preemptionExpectations,
		PreemptionLimitTracker:    preemptionLimitTracker,
		CustomLabels:              customLabels,
		DRAMapper:                 draMapper,
		DRABackedResources:        draBackedResources,
		ResourceFormatter:         resourceFormatter,
		ResourceSliceAPIAvailable: resourceSliceAPIAvailable,
		ClusterProfileCRDPresent:  clusterProfileCRDPresent,
	}
	if err := setupControllers(ctx, mgr, cCache, queues, &cfg, serverVersionFetcher, integrationManager, controllerOpts); err != nil {
		return fmt.Errorf("unable to setup controllers: %w", err)
	}

	if failedWebhook, err := webhooks.Setup(mgr, roleTracker); err != nil {
		return fmt.Errorf("unable to create webhook %s: %w", failedWebhook, err)
	}

	go queues.CleanUpOnContext(ctx)
	go cCache.CleanUpOnContext(ctx)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	visibilityErrCh := make(chan error, 1)
	if features.Enabled(features.VisibilityOnDemand) {
		go func() {
			if err := visibility.CreateAndStartVisibilityServer(ctx, queues, &cfg, kubeConfig, parsedTLSConfig); err != nil {
				visibilityErrCh <- fmt.Errorf("unable to create and start visibility server: %w", err)
				cancel()
			}
		}()
	}

	if err := setupScheduler(mgr, cCache, queues, &cfg, roleTracker, preemptionExpectations, preemptionLimitTracker, customLabels, resourceFormatter); err != nil {
		return fmt.Errorf("could not setup scheduler: %w", err)
	}

	setupLog.V(1).Info("Starting manager")
	mgrErrCh := make(chan error, 1)
	go func() {
		mgrErrCh <- mgr.Start(ctx)
	}()

	select {
	case err := <-visibilityErrCh:
		cancel()
		return err
	case err := <-mgrErrCh:
		if err != nil {
			return fmt.Errorf("could not run manager: %w", err)
		}
		select {
		case vErr := <-visibilityErrCh:
			return vErr
		default:
		}
	}
	return nil
}

func setupIndexes(
	ctx context.Context,
	mgr ctrl.Manager,
	cfg *configapi.Configuration,
	integrationManager *jobframework.IntegrationManager,
	resourceSliceAPIAvailable bool,
) error {
	var indexerOpts []indexer.Option
	if waitforpodsready.PodsScheduledTrackingEnabled(cfg.WaitForPodsReady) {
		indexerOpts = append(indexerOpts, indexer.WithPodWorkloadSliceNameIndex())
	}
	err := indexer.Setup(ctx, mgr.GetFieldIndexer(), indexerOpts...)
	if err != nil {
		return err
	}

	// setup provision admission check controller indexes
	if err := provisioning.ServerSupportsProvisioningRequest(mgr); err != nil {
		setupLog.Error(
			err,
			"Skipping admission check controller setup: Provisioning Requests not supported (Possible cause: missing or unsupported cluster-autoscaler)",
		)
	} else if err := provisioning.SetupIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("could not setup provisioning indexer: %w", err)
	}

	if features.Enabled(features.CapacityBufferACC) {
		if err := capacitybuffer.ServerSupportsCapacityBuffer(mgr); err == nil {
			if err := capacitybuffer.SetupIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
				return fmt.Errorf("could not setup capacity buffer indexer: %w", err)
			}
		}
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

	if resourceSliceAPIAvailable {
		if err := core.SetupResourceSliceIndexer(ctx, mgr.GetFieldIndexer()); err != nil {
			return fmt.Errorf("could not setup ResourceSlice indexer: %w", err)
		}
	}

	indexOpts := []jobframework.Option{
		jobframework.WithIntegrationManager(integrationManager),
		jobframework.WithEnabledFrameworks(cfg.Integrations.Frameworks),
	}
	return integrationManager.SetupIndexes(ctx, mgr.GetFieldIndexer(), indexOpts...)
}

func setupControllers(
	ctx context.Context,
	mgr ctrl.Manager,
	cCache *schdcache.Cache,
	queues *qcache.Manager,
	cfg *configapi.Configuration,
	serverVersionFetcher *kubeversion.ServerVersionFetcher,
	integrationManager *jobframework.IntegrationManager,
	opts core.SetupControllersOpts,
) error {
	if failedCtrl, err := core.SetupControllers(mgr, queues, cCache, cfg, opts); err != nil {
		return fmt.Errorf("unable to create controller %s: %w", failedCtrl, err)
	}
	if features.Enabled(features.FailureRecoveryPolicy) {
		if failedCtrlName, err := failurerecovery.SetupControllers(mgr, cfg, opts.RoleTracker); err != nil {
			return fmt.Errorf("could not setup FailureRecovery controller %s: %w", failedCtrlName, err)
		}
	}

	// setup provision admission check controller
	if err := provisioning.ServerSupportsProvisioningRequest(mgr); err != nil {
		setupLog.Info(
			"Skipping provisioning controller setup: Provisioning Requests not supported (Possible cause: missing or unsupported cluster-autoscaler)",
		)
	} else {
		ctrl, err := provisioning.NewController(mgr.GetClient(), mgr.GetEventRecorder("kueue-provisioning-request-controller"), opts.RoleTracker)
		if err != nil {
			return fmt.Errorf("could not create the provisioning controller: %w", err)
		}

		if err := ctrl.SetupWithManager(mgr); err != nil {
			return fmt.Errorf("could not setup provisioning controller: %w", err)
		}
	}

	if features.Enabled(features.CapacityBufferACC) {
		if err := setupCapacityBuffers(mgr, opts); err != nil {
			return err
		}
	}

	if features.Enabled(features.MultiKueue) {
		adapters, err := integrationManager.GetMultiKueueAdapters(sets.New(cfg.Integrations.Frameworks...))
		if err != nil {
			return fmt.Errorf("could not get the enabled multikueue adapters: %w", err)
		}

		if features.Enabled(features.MultiKueueAdaptersForCustomJobs) && cfg.MultiKueue != nil &&
			len(cfg.MultiKueue.ExternalFrameworks) > 0 {
			externalAdapters, err := externalframeworks.NewAdapters(cfg.MultiKueue.ExternalFrameworks)
			if err != nil {
				return fmt.Errorf("could not create external framework adapters: %w", err)
			}

			// Add external framework adapters to the adapters map
			for _, adapter := range externalAdapters {
				gvk := adapter.GVK().String()
				setupLog.V(1).Info("Creating external framework MultiKueue adapter", "gvk", gvk)
				adapters[gvk] = adapter
			}
		}

		if err := multikueue.SetupControllers(mgr, *cfg.Namespace,
			multikueue.WithGCInterval(cfg.MultiKueue.GCInterval.Duration),
			multikueue.WithOrigin(ptr.Deref(cfg.MultiKueue.Origin, configapi.DefaultMultiKueueOrigin)),
			multikueue.WithWorkerLostTimeout(cfg.MultiKueue.WorkerLostTimeout.Duration),
			multikueue.WithAdapters(adapters),
			multikueue.WithDispatcherName(ptr.Deref(cfg.MultiKueue.DispatcherName, configapi.MultiKueueDispatcherModeAllAtOnce)),
			multikueue.WithClusterProfiles(cfg.MultiKueue.ClusterProfile),
			multikueue.WithClusterProfileCRDPresent(opts.ClusterProfileCRDPresent),
			multikueue.WithRoleTracker(opts.RoleTracker),
			multikueue.WithClientConnection(cfg.ClientConnection),
		); err != nil {
			return fmt.Errorf("could not setup MultiKueue controller: %w", err)
		}

		if failedDispatcher, err := workloaddispatcher.SetupControllers(mgr, cfg, opts.RoleTracker); err != nil {
			return fmt.Errorf("could not setup Dispatcher controller %q for MultiKueue: %w", failedDispatcher, err)
		}
	}

	if features.Enabled(features.TopologyAwareScheduling) {
		if failedCtrl, err := tas.SetupControllers(mgr, queues, cCache, cfg, opts.RoleTracker, tas.WithCustomLabels(opts.CustomLabels)); err != nil {
			return fmt.Errorf("could not setup TAS controller %s: %w", failedCtrl, err)
		}
	}

	if features.Enabled(features.ElasticJobsViaWorkloadSlices) {
		if failedCtrl, err := elasticjobs.SetupWithManager(mgr, cfg, opts.RoleTracker, opts.CustomLabels); err != nil {
			return fmt.Errorf("could not setup %s controller: %w", failedCtrl, err)
		}
	}

	if waitforpodsready.PodsScheduledTrackingEnabled(cfg.WaitForPodsReady) {
		tracker := unscheduledpods.NewTracker(mgr.GetClient(), opts.RoleTracker, cfg.WaitForPodsReady)
		if failedCtrl, err := tracker.SetupWithManager(mgr, cfg); err != nil {
			return fmt.Errorf("could not setup %s controller: %w", failedCtrl, err)
		}
	}

	if features.Enabled(features.ConcurrentAdmission) {
		if failedCtrl, err := concurrentadmission.SetupControllers(mgr, queues, cfg, opts.RoleTracker); err != nil {
			return fmt.Errorf("could not setup ConcurrentAdmission controller %s: %w", failedCtrl, err)
		}
	}

	labelKeysToCopy, annotationsToCopy := jobframework.KeysToCopy(setupLog, cfg)
	jfOpts := []jobframework.Option{
		jobframework.WithIntegrationManager(integrationManager),
		jobframework.WithManageJobsWithoutQueueName(cfg.ManageJobsWithoutQueueName),
		jobframework.WithWaitForPodsReady(cfg.WaitForPodsReady),
		jobframework.WithKubeServerVersion(serverVersionFetcher),
		jobframework.WithEnabledFrameworks(cfg.Integrations.Frameworks),
		jobframework.WithEnabledExternalFrameworks(cfg.Integrations.ExternalFrameworks),
		jobframework.WithManagerName(constants.KueueName),
		jobframework.WithLabelKeysToCopy(labelKeysToCopy),
		jobframework.WithAnnotationsToCopy(annotationsToCopy),
		jobframework.WithCache(cCache),
		jobframework.WithQueues(queues),
		jobframework.WithObjectRetentionPolicies(cfg.ObjectRetentionPolicies),
		jobframework.WithRoleTracker(opts.RoleTracker),
		jobframework.WithCustomLabels(opts.CustomLabels),
	}

	nsSelector, err := metav1.LabelSelectorAsSelector(cfg.ManagedJobsNamespaceSelector)
	if err != nil {
		return fmt.Errorf("failed to parse managedJobsNamespaceSelector: %w", err)
	}
	jfOpts = append(jfOpts, jobframework.WithManagedJobsNamespaceSelector(nsSelector))

	if err := integrationManager.SetupControllers(ctx, mgr, setupLog, jfOpts...); err != nil {
		return fmt.Errorf(
			"unable to create controller or webhook for kubernetesVersion %v: %w",
			serverVersionFetcher.GetServerVersion(),
			err,
		)
	}

	return nil
}

func setupCapacityBuffers(mgr ctrl.Manager, opts core.SetupControllersOpts) error {
	if err := capacitybuffer.ServerSupportsCapacityBuffer(mgr); err != nil {
		setupLog.Info("Skipping capacity buffer controller setup: CapacityBuffers not supported "+
			"(Possible cause: missing or unsupported autoscaler, or Kueue alpha APIs not installed)", "error", err)
		// Pods gated while the API was served would otherwise stay gated.
		if err := capacitybuffer.NewUngater(mgr.GetClient(), opts.RoleTracker).SetupWithManager(mgr); err != nil {
			return fmt.Errorf("could not setup capacity buffer ungater: %w", err)
		}
		return nil
	}
	ctrl, err := capacitybuffer.NewController(mgr.GetClient(), mgr.GetEventRecorder("kueue-capacity-buffer-controller"), opts.RoleTracker)
	if err != nil {
		return fmt.Errorf("could not create the capacity buffer controller: %w", err)
	}
	if err := ctrl.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("could not setup capacity buffer controller: %w", err)
	}
	return nil
}

// setupProbeEndpoints registers the health endpoints
func setupProbeEndpoints(mgr ctrl.Manager) error {
	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		return fmt.Errorf("unable to set up health check: %w", err)
	}

	// Wait for the webhook server to be listening before advertising the
	// Kueue replica as ready. This allows users to wait with sending the first
	// requests, requiring webhooks, until the Kueue deployment is available, so
	// that the early requests are not rejected during the Kueue's startup.
	if err := mgr.AddReadyzCheck("readyz", mgr.GetWebhookServer().StartedChecker()); err != nil {
		return fmt.Errorf("unable to set up ready check: %w", err)
	}

	setupLog.V(1).Info("Probe endpoints are configured on healthz and readyz")
	return nil
}

func setupScheduler(
	mgr ctrl.Manager,
	cCache *schdcache.Cache,
	queues *qcache.Manager,
	cfg *configapi.Configuration,
	roleTracker *roletracker.RoleTracker,
	preemptionExpectations *expectations.Store,
	preemptionLimitTracker *limits.PreemptionLimitTracker,
	customLabels *metrics.CustomLabels,
	resourceFormatter *resources.ResourceFormatter,
) error {
	sched := scheduler.New(
		queues,
		cCache,
		mgr.GetClient(),
		mgr.GetEventRecorder(constants.AdmissionName),
		scheduler.WithPodsReadyRequeuingTimestamp(podsReadyRequeuingTimestamp(cfg)),
		scheduler.WithFairSharing(cfg.FairSharing),
		scheduler.WithAdmissionFairSharing(cfg.AdmissionFairSharing),
		scheduler.WithQuotaCheckStrategy(quotaCheckStrategy(cfg)),
		scheduler.WithRoleTracker(roleTracker),
		scheduler.WithPreemptionExpectations(preemptionExpectations),
		scheduler.WithPreemptionLimitTracker(preemptionLimitTracker),
		scheduler.WithCustomLabels(customLabels),
		scheduler.WithResourceFormatter(resourceFormatter),
		scheduler.WithFailedTASReplacementRecoveryTimeout(waitforpodsready.RecoveryTimeout(cfg.WaitForPodsReady)),
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

func leaderElectionEnabled(cfg *configapi.Configuration) bool {
	return cfg.LeaderElection != nil && ptr.Deref(cfg.LeaderElection.LeaderElect, false)
}

func setupRoleTracker(ctx context.Context, mgr ctrl.Manager, cfg *configapi.Configuration) *roletracker.RoleTracker {
	if leaderElectionEnabled(cfg) {
		tracker := roletracker.NewRoleTracker(mgr.Elected())
		go tracker.Start(ctx, setupLog)
		setupLog.V(1).Info("RoleTracker: leader election enabled")
		return tracker
	}
	setupLog.V(1).Info("RoleTracker: running in standalone mode")
	return nil
}

func blockForPodsReady(cfg *configapi.Configuration) bool {
	return waitforpodsready.Enabled(cfg.WaitForPodsReady) &&
		cfg.WaitForPodsReady.BlockAdmission != nil &&
		*cfg.WaitForPodsReady.BlockAdmission
}

func podsReadyRequeuingTimestamp(cfg *configapi.Configuration) configapi.RequeuingTimestamp {
	if cfg.WaitForPodsReady != nil && cfg.WaitForPodsReady.RequeuingStrategy != nil &&
		cfg.WaitForPodsReady.RequeuingStrategy.Timestamp != nil {
		return *cfg.WaitForPodsReady.RequeuingStrategy.Timestamp
	}
	return configapi.EvictionTimestamp
}

func quotaCheckStrategy(cfg *configapi.Configuration) configapi.QuotaCheckStrategy {
	if features.Enabled(features.QuotaCheckStrategy) && cfg.Resources != nil &&
		cfg.Resources.QuotaCheckStrategy != nil {
		return *cfg.Resources.QuotaCheckStrategy
	}
	return configapi.QuotaCheckBlockUndeclared
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
	setupLog.V(1).Info("Configuration loaded", "config", cfgStr)
	return options, cfg, nil
}

func parseFlagsAndInitLogger(args []string, out io.Writer) (string, string, error) {
	fs := flag.NewFlagSet("kueue", flag.ContinueOnError)
	fs.SetOutput(out)
	clientconfig.RegisterFlags(fs)

	var configFile string
	fs.StringVar(&configFile, "config", "",
		"The controller will load its initial configuration from this file. "+
			"Omit this flag to use the default configuration values. ")

	var featureGates string
	fs.StringVar(
		&featureGates,
		"feature-gates",
		"",
		"A set of key=value pairs that describe feature gates for alpha/experimental features.",
	)

	customLogProcessor := zaplog.WrapCore(utillogging.NewCustomLogProcessor)

	zapOptions := zap.Options{
		TimeEncoder: zapcore.RFC3339NanoTimeEncoder,
		ZapOpts:     []zaplog.Option{zaplog.AddCaller(), customLogProcessor},
	}
	zapOptions.BindFlags(fs)
	if err := fs.Parse(args); err != nil {
		return "", "", fmt.Errorf("unable to parse flags: %w", err)
	}

	logger := zap.New(zap.UseFlagOptions(&zapOptions))
	ctrl.SetLogger(logger)
	return configFile, featureGates, nil
}

func loadAndValidateConfig(configFile, featureGates string) (ctrl.Options, configapi.Configuration, error) {
	options, cfg, err := apply(configFile)
	if err != nil {
		return options, cfg, fmt.Errorf("unable to load the configuration: %w", err)
	}

	if err := config.LoadAndValidateFeatureGates(featureGates, cfg.FeatureGates).ToAggregate(); err != nil {
		return options, cfg, fmt.Errorf("conflicting feature gates detected: %w", err)
	}

	// Apply feature-gated defaults after the configured gates have been loaded.
	scheme.Default(&cfg)

	// Validates the configuration after it has been loaded and feature gates have been set.
	if err := config.Validate(&cfg, scheme, integrationManager).ToAggregate(); err != nil {
		return options, cfg, fmt.Errorf("unable to validate the configuration: %w", err)
	}

	setupLog.V(1).Info(
		"Initializing",
		"gitVersion",
		version.GitVersion,
		"gitCommit",
		version.GitCommit,
		"buildDate",
		version.BuildDate,
	)

	features.LogFeatureGates(setupLog)

	if labels := jobframework.NonInheritableLabelsIn(cfg.Integrations.LabelKeysToCopy); len(labels) > 0 {
		setupLog.V(1).Info("integrations.labelKeysToCopy names Kueue's internal labels, which are never copied to a Workload", "labels", labels)
	}

	return options, cfg, nil
}

func configureMetricsAndWebhooks(options *ctrl.Options, cfg *configapi.Configuration) (*tlsconfig.TLS, *certwatcher.CertWatcher, *metrics.LocalQueueMetricsConfig, *metrics.CustomLabels, error) {
	// Metrics endpoint is enabled in 'config/default/kustomization.yaml'. The Metrics options configure the server.
	// More info:
	// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.1/pkg/metrics/server
	// - https://book.kubebuilder.io/reference/metrics.html
	metricsServerOptions := config.MetricsServerOptions(setupLog, cfg.Metrics)

	parsedTLSConfig := &tlsconfig.TLS{}
	if features.Enabled(features.TLSOptions) {
		var err error
		parsedTLSConfig, err = tlsconfig.ParseTLSOptions(cfg.TLS)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("unable to parse TLS options from configuration: %w", err)
		}
	}

	// Apply TLS configuration from config
	if parsedTLSConfig != nil {
		tlsOpts := tlsconfig.BuildTLSOptions(parsedTLSConfig)
		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, tlsOpts...)
	}

	if err := config.AddWebhookSettingsTo(options, cfg); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to configure webhook server: %w", err)
	}

	var metricsCertWatcher *certwatcher.CertWatcher
	if cfg.InternalCertManagement == nil || !*cfg.InternalCertManagement.Enable {
		metricsCertPath := "/etc/kueue/metrics/certs"
		setupLog.V(1).Info("Initializing metrics certificate watcher using provided certificates",
			"metricsCertPath", metricsCertPath)

		var err error
		metricsCertWatcher, err = certwatcher.New(
			filepath.Join(metricsCertPath, "tls.crt"),
			filepath.Join(metricsCertPath, "tls.key"),
		)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("unable to initialize metrics certificate watcher: %w", err)
		}

		metricsServerOptions.TLSOpts = append(metricsServerOptions.TLSOpts, func(config *tls.Config) {
			config.GetCertificate = metricsCertWatcher.GetCertificate
		})
	}
	options.Metrics = metricsServerOptions

	lqMetrics, err := metrics.NewLocalQueueMetricsConfig(cfg.Metrics.LocalQueueMetrics)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("unable to configure LocalQueue metrics: %w", err)
	}

	var customLabels *metrics.CustomLabels
	if features.Enabled(features.CustomMetricLabels) {
		customLabels = metrics.NewCustomLabels(cfg.Metrics.CustomLabels)
	} else if len(cfg.Metrics.CustomLabels) > 0 {
		setupLog.V(1).Info("metrics.customLabels is configured but CustomMetricLabels feature gate is disabled; custom labels will have no effect")
	}
	registerMetricsOnce.Do(metrics.Register)

	return parsedTLSConfig, metricsCertWatcher, lqMetrics, customLabels, nil
}

func buildKubeConfig(cfg *configapi.Configuration) (*rest.Config, error) {
	kubeConfig, err := ctrl.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("unable to get kubeconfig: %w", err)
	}
	if kubeConfig.UserAgent == "" {
		kubeConfig.UserAgent = useragent.Default()
	}

	// Set the RateLimiter here, otherwise the controller-runtime's typedClient will use a different RateLimiter
	// for each API type.
	// When the controller-runtime > 0.21, the client-side ratelimiting will be disabled by default.
	// The following QPS negative value check allows us to disable the client-side ratelimiting.
	if *cfg.ClientConnection.QPS >= 0.0 {
		kubeConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(
			*cfg.ClientConnection.QPS,
			int(*cfg.ClientConnection.Burst),
		)
	}
	setupLog.V(1).Info("K8S Client", "qps", *cfg.ClientConnection.QPS, "burst", *cfg.ClientConnection.Burst)
	return kubeConfig, nil
}
