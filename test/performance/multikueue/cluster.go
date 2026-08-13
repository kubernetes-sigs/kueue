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
	"errors"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/flowcontrol"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	crconfig "sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue"
	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/scheduler"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
)

const (
	apiQPS   = configapi.DefaultClientConnectionQPS
	apiBurst = int(configapi.DefaultClientConnectionBurst)

	benchmarkDispatcherName    = configapi.MultiKueueDispatcherModeAllAtOnce
	benchmarkGCInterval        = configapi.DefaultMultiKueueGCInterval
	benchmarkWorkerLostTimeout = configapi.DefaultMultiKueueWorkerLostTimeout
	benchmarkEventsBatchPeriod = constants.UpdatesBatchPeriod
)

// workloadConcurrency matches the MultiKueue e2e configuration
// (test/e2e/config/multikueue/baseline/controller_manager_config.yaml) and the documented MultiKueue
// setup. It has to be set explicitly: controller-runtime resolves a controller's concurrency from the
// manager's GroupKindConcurrency, and falls back to one reconcile at a time when the Workload kind is
// absent from it.
const workloadConcurrency = 10

// benchmarkGroupKindConcurrency omits Job and Pod because no job framework reconciler runs here.
func benchmarkGroupKindConcurrency() map[string]int {
	return map[string]int{
		kueue.SchemeGroupVersion.WithKind("Workload").GroupKind().String():       workloadConcurrency,
		kueue.SchemeGroupVersion.WithKind("LocalQueue").GroupKind().String():     5,
		kueue.SchemeGroupVersion.WithKind("ClusterQueue").GroupKind().String():   5,
		kueue.SchemeGroupVersion.WithKind("Cohort").GroupKind().String():         1,
		kueue.SchemeGroupVersion.WithKind("ResourceFlavor").GroupKind().String(): 1,
	}
}

// workerName has to agree between the runner and the summary, which reports results per worker.
func workerName(index int) string {
	return fmt.Sprintf("worker-%d", index+1)
}

type benchmarkCluster struct {
	name   string
	env    *envtest.Environment
	config *rest.Config
	client client.WithWatch
	cancel context.CancelFunc
	done   <-chan error
}

type managerSetup func(context.Context, manager.Manager) error

func startBenchmarkCluster(ctx context.Context, name, crdPath string, setup managerSetup) (*benchmarkCluster, error) {
	scheme, err := benchmarkScheme()
	if err != nil {
		return nil, err
	}

	testEnv := benchmarkEnvironment(crdPath, scheme)
	testEnv.ControlPlane.GetAPIServer().Configure().
		Append("max-requests-inflight", "5000").
		Append("max-mutating-requests-inflight", "2500")

	restConfig, err := testEnv.Start()
	if err != nil {
		return nil, fmt.Errorf("start %s control plane: %w", name, err)
	}
	restConfig.QPS = apiQPS
	restConfig.Burst = apiBurst

	directClient, err := client.NewWithWatch(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		_ = testEnv.Stop()
		return nil, fmt.Errorf("create %s client: %w", name, err)
	}
	// Production shares one token bucket across the manager's typed clients. Install it only
	// after creating the direct benchmark client so workload generation has its own limiter.
	restConfig.RateLimiter = flowcontrol.NewTokenBucketRateLimiter(apiQPS, apiBurst)

	mgr, err := ctrl.NewManager(restConfig, manager.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
		Controller: crconfig.Controller{
			SkipNameValidation:   new(true),
			GroupKindConcurrency: benchmarkGroupKindConcurrency(),
		},
	})
	if err != nil {
		_ = testEnv.Stop()
		return nil, fmt.Errorf("create %s manager: %w", name, err)
	}

	managerCtx, cancel := context.WithCancel(ctx)
	if err := setup(managerCtx, mgr); err != nil {
		cancel()
		_ = testEnv.Stop()
		return nil, fmt.Errorf("setup %s manager: %w", name, err)
	}

	done := make(chan error, 1)
	go func() {
		done <- mgr.Start(managerCtx)
		close(done)
	}()

	cacheCtx, cacheCancel := context.WithTimeout(ctx, time.Minute)
	defer cacheCancel()
	if err := waitForManagerReady(name, mgr.GetCache().WaitForCacheSync, done, cacheCtx); err != nil {
		cancel()
		<-done
		_ = testEnv.Stop()
		return nil, err
	}

	return &benchmarkCluster{
		name:   name,
		env:    testEnv,
		config: restConfig,
		client: directClient,
		cancel: cancel,
		done:   done,
	}, nil
}

func benchmarkEnvironment(crdPath string, scheme *runtime.Scheme) *envtest.Environment {
	return &envtest.Environment{
		CRDDirectoryPaths:       []string{crdPath},
		ErrorIfCRDPathMissing:   true,
		ControlPlaneStopTimeout: 90 * time.Second,
		UseExistingCluster:      new(false),
		Scheme:                  scheme,
	}
}

func benchmarkScheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		clientgoscheme.AddToScheme,
		configapi.AddToScheme,
		kueue.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			return nil, fmt.Errorf("add API to scheme: %w", err)
		}
	}
	return scheme, nil
}

func setupCoreControllers(ctx context.Context, mgr manager.Manager) error {
	if err := indexer.Setup(ctx, mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("setup core indexers: %w", err)
	}

	schedulerCache := schdcache.New(mgr.GetClient())
	requeuer := qcache.NewRequeuer()
	if err := mgr.Add(requeuer); err != nil {
		return fmt.Errorf("add workload requeuer: %w", err)
	}

	preemptionExpectations := preemptexpectations.New()
	queues := qcache.NewManager(
		mgr.GetClient(),
		schedulerCache,
		requeuer,
		qcache.WithPreemptionExpectations(preemptionExpectations),
	)
	go queues.CleanUpOnContext(ctx)
	go schedulerCache.CleanUpOnContext(ctx)

	configuration := &configapi.Configuration{}
	mgr.GetScheme().Default(configuration)
	if failedController, err := core.SetupControllers(
		mgr,
		queues,
		schedulerCache,
		configuration,
		core.SetupControllersOpts{PreemptionExpectations: preemptionExpectations},
	); err != nil {
		return fmt.Errorf("setup core controller %s: %w", failedController, err)
	}

	sched := scheduler.New(
		queues,
		schedulerCache,
		mgr.GetClient(),
		mgr.GetEventRecorder(constants.AdmissionName),
		scheduler.WithPreemptionExpectations(preemptionExpectations),
	)
	if err := mgr.Add(sched); err != nil {
		return fmt.Errorf("add scheduler: %w", err)
	}
	return nil
}

func setupManagerControllers(configNamespace string) managerSetup {
	return func(ctx context.Context, mgr manager.Manager) error {
		if err := setupCoreControllers(ctx, mgr); err != nil {
			return err
		}
		if err := multikueue.SetupIndexer(ctx, mgr.GetFieldIndexer(), configNamespace); err != nil {
			return fmt.Errorf("setup MultiKueue indexers: %w", err)
		}

		integrationManager := jobframework.NewIntegrationManager()
		if err := workloadjob.RegisterIntegration(integrationManager); err != nil {
			return fmt.Errorf("register Job integration: %w", err)
		}
		adapters, err := integrationManager.GetMultiKueueAdapters(
			sets.New(workloadjob.FrameworkName),
		)
		if err != nil {
			return fmt.Errorf("get MultiKueue adapters: %w", err)
		}
		if err := multikueue.SetupControllers(
			mgr,
			configNamespace,
			multikueue.WithGCInterval(benchmarkGCInterval),
			multikueue.WithWorkerLostTimeout(benchmarkWorkerLostTimeout),
			multikueue.WithEventsBatchPeriod(benchmarkEventsBatchPeriod),
			multikueue.WithAdapters(adapters),
			multikueue.WithDispatcherName(benchmarkDispatcherName),
		); err != nil {
			return fmt.Errorf("setup MultiKueue controllers: %w", err)
		}
		return nil
	}
}

func (c *benchmarkCluster) stop() error {
	c.cancel()
	var managerErr error
	select {
	case managerErr = <-c.done:
	case <-time.After(time.Minute):
		managerErr = fmt.Errorf("%s manager did not stop within one minute", c.name)
	}
	return stopErrors(c.name, managerErr, c.env.Stop())
}

func waitForManagerReady(name string, cacheSynced func(context.Context) bool, done <-chan error, ctx context.Context) error {
	synced := make(chan bool, 1)
	go func() {
		synced <- cacheSynced(ctx)
	}()
	select {
	case err := <-done:
		return managerStartError(name, err)
	case ok := <-synced:
		select {
		case err := <-done:
			return managerStartError(name, err)
		default:
		}
		if !ok {
			return fmt.Errorf("wait for %s manager cache sync", name)
		}
		return nil
	}
}

func managerStartError(name string, err error) error {
	if err != nil {
		return fmt.Errorf("start %s manager: %w", name, err)
	}
	return fmt.Errorf("%s manager exited before cache sync", name)
}

func stopErrors(name string, managerErr, envErr error) error {
	if managerErr != nil {
		managerErr = fmt.Errorf("stop %s manager: %w", name, managerErr)
	}
	if envErr != nil {
		envErr = fmt.Errorf("stop %s control plane: %w", name, envErr)
	}
	return errors.Join(managerErr, envErr)
}
