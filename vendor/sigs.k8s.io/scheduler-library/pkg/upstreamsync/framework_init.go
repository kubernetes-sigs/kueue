// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package upstreamsync

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	coreinformers "k8s.io/client-go/informers/core"
	corev1informers "k8s.io/client-go/informers/core/v1"
	clientset "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/dynamic-resource-allocation/deviceclass/extendedresourcecache"
	resourceslicetracker "k8s.io/dynamic-resource-allocation/resourceslice/tracker"
	"k8s.io/klog/v2"
	configv1 "k8s.io/kube-scheduler/config/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/apis/config/scheme"
	apidispatcher "k8s.io/kubernetes/pkg/scheduler/backend/api_dispatcher"
	internalcache "k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	apicalls "k8s.io/kubernetes/pkg/scheduler/framework/api_calls"
	"k8s.io/kubernetes/pkg/scheduler/framework/parallelize"
	frameworkplugins "k8s.io/kubernetes/pkg/scheduler/framework/plugins"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/noderesources"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/nodevolumelimits"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"k8s.io/kubernetes/pkg/scheduler/profile"
	"k8s.io/kubernetes/pkg/scheduler/util/assumecache"
)

/*

Extracted from kubernetes/kubernetes/pkg/scheduler/framework_init.go

Deviations from upstream framework_init.go are marked with the "UPSTREAM-DIFF:" prefix,
see pkg/upstreamsync/doc.go.

*/

// InitMetricsOnce constructs the kube-scheduler metric objects.
//
// UPSTREAM-DIFF: Package-level variables in k8s.io/kubernetes/pkg/scheduler/metrics stay nil
// until metrics.InitMetrics is called, which causes panics when frameworks record metrics.
var InitMetricsOnce = sync.OnceFunc(metrics.InitMetrics)

// FrameworkComponents holds the shared scheduler dependencies that span across
// all framework profiles, the scheduling queue, and event handlers.
//
// It decouples the initialization of shared infrastructure and DRA/extender
// managers from the per-profile framework wiring, allowing synthetic callers
// and simulation harnesses to construct profiles without redundant shared state.
type FrameworkComponents struct {
	extenders []fwk.Extender

	// APIDispatcher is non-nil when the SchedulerAsyncAPICalls feature gate is enabled.
	apiDispatcher   *apidispatcher.APIDispatcher
	metricsRecorder *metrics.MetricAsyncRecorder

	// DRA components required for registering event handlers. Nil unless
	// DynamicResourceAllocation feature gate is enabled.
	// UPSTREAM-DIFF: resourceClaimCache, resourceSliceTracker, and extendedResourceCache are never
	// mutated by the scheduler outside of PreBind, so they are created and registered on informers
	// once in FrameworkComponents. Each ProfileMap gets its own draManager (claimTracker) to isolate
	// inFlightAllocations across concurrent snapshots and states.
	resourceClaimCache    *assumecache.AssumeCache
	resourceSliceTracker  *resourceslicetracker.Tracker
	extendedResourceCache *extendedresourcecache.ExtendedResourceCache

	client          clientset.Interface
	informerFactory informers.SharedInformerFactory
	options         schedulerOptions

	// UPSTREAM-DIFF: registeredHandlers holds the informer event handler registrations created
	// by FrameworkComponents so callers can wait for their initial sync before scheduling.
	registeredHandlers []cache.ResourceEventHandlerRegistration
}

const (
	// syncedPollPeriod controls how often you look at the status of your sync funcs.
	syncedPollPeriod = 100 * time.Millisecond
)

// WaitForHandlersSync waits for EventHandlers registered in FrameworkComponents to sync.
//
// UPSTREAM-DIFF: adapted from Scheduler.WaitForHandlersSync in pkg/scheduler/eventhandlers.go
// so FrameworkComponents consumers can wait for shared DRA caches and trackers
// (resourceSliceTracker, resourceClaimCache, extendedResourceCache) to finish delivering
// initial informer events.
func (c *FrameworkComponents) WaitForHandlersSync(ctx context.Context) error {
	return wait.PollUntilContextCancel(ctx, syncedPollPeriod, true, func(ctx context.Context) (done bool, err error) {
		for _, handler := range c.registeredHandlers {
			if !handler.HasSynced() {
				return false, nil
			}
		}
		return true, nil
	})
}

// UPSTREAM-DIFF: default parallelism from parallelize.DefaultParallelism instead of upstream-private defaultSchedulerOptions.parallelism.
var defaultComponentsOptions = schedulerOptions{
	parallelism:         int32(parallelize.DefaultParallelism),
	applyDefaultProfile: true,
}

// NewFrameworkComponents initializes the shared dependencies required across
// scheduler framework profiles.
//
// Callers must invoke NewFrameworkComponents before starting the shared
// informer factory so that any required informers are registered prior to Start().
func NewFrameworkComponents(ctx context.Context, client clientset.Interface, informerFactory informers.SharedInformerFactory, opts ...Option) (*FrameworkComponents, error) {
	options := defaultComponentsOptions
	for _, opt := range opts {
		opt(&options)
	}

	return newFrameworkComponents(ctx, client, informerFactory, options)
}

func newFrameworkComponents(ctx context.Context,
	client clientset.Interface,
	informerFactory informers.SharedInformerFactory,
	options schedulerOptions,
) (*FrameworkComponents, error) {
	// UPSTREAM-DIFF: ensure metrics are initialized before constructing components.
	InitMetricsOnce()
	logger := klog.FromContext(ctx)
	stopEverything := ctx.Done()

	if options.applyDefaultProfile {
		var versionedCfg configv1.KubeSchedulerConfiguration
		scheme.Scheme.Default(&versionedCfg)
		cfg := schedulerapi.KubeSchedulerConfiguration{}
		if err := scheme.Scheme.Convert(&versionedCfg, &cfg, nil); err != nil {
			return nil, err
		}
		options.profiles = cfg.Profiles
	}

	extenders, err := buildExtenders(logger, options.extenders, options.profiles)
	if err != nil {
		return nil, fmt.Errorf("couldn't build extenders: %w", err)
	}

	metricsRecorder := metrics.NewMetricsAsyncRecorder(1000, time.Second, stopEverything)

	var resourceClaimCache *assumecache.AssumeCache
	var resourceSliceTracker *resourceslicetracker.Tracker
	var extendedResourceCache *extendedresourcecache.ExtendedResourceCache
	var registeredHandlers []cache.ResourceEventHandlerRegistration
	if feature.DefaultFeatureGate.Enabled(features.DynamicResourceAllocation) {
		resourceClaimInformer := informerFactory.Resource().V1().ResourceClaims().Informer()
		resourceClaimCache = assumecache.NewAssumeCache(logger, resourceClaimInformer, "ResourceClaim", "", nil)
		resourceSliceTrackerOpts := resourceslicetracker.Options{
			EnableDeviceTaintRules:   feature.DefaultFeatureGate.Enabled(features.DRADeviceTaintRules),
			EnableConsumableCapacity: feature.DefaultFeatureGate.Enabled(features.DRAConsumableCapacity),
			SliceInformer:            informerFactory.Resource().V1().ResourceSlices(),
			KubeClient:               client,
		}
		// If device taint rules are disabled, the additional informers are not needed and
		// the tracker turns into a simple wrapper around the slice informer.
		if resourceSliceTrackerOpts.EnableDeviceTaintRules {
			resourceSliceTrackerOpts.TaintInformer = informerFactory.Resource().V1().DeviceTaintRules()
		}
		var err error
		resourceSliceTracker, err = resourceslicetracker.StartTracker(ctx, resourceSliceTrackerOpts)
		if err != nil {
			return nil, fmt.Errorf("couldn't start resource slice tracker: %w", err)
		}
		registeredHandlers = append(registeredHandlers, resourceSliceTracker, resourceClaimCache.AddEventHandler(cache.ResourceEventHandlerFuncs{}))
		if feature.DefaultFeatureGate.Enabled(features.DRAExtendedResource) {
			extendedResourceCache = extendedresourcecache.NewExtendedResourceCache(logger)
			reg, err := informerFactory.Resource().V1().DeviceClasses().Informer().AddEventHandler(extendedResourceCache)
			if err != nil {
				return nil, fmt.Errorf("couldn't add event handler for DeviceClass extendedResourceCache: %w", err)
			}
			registeredHandlers = append(registeredHandlers, reg)
		}
	}

	var apiDispatcher *apidispatcher.APIDispatcher
	if feature.DefaultFeatureGate.Enabled(features.SchedulerAsyncAPICalls) {
		apiDispatcher = apidispatcher.New(client, int(options.parallelism), apicalls.Relevances)
	}

	return &FrameworkComponents{
		extenders:             extenders,
		apiDispatcher:         apiDispatcher,
		metricsRecorder:       metricsRecorder,
		resourceClaimCache:    resourceClaimCache,
		resourceSliceTracker:  resourceSliceTracker,
		extendedResourceCache: extendedResourceCache,
		client:                client,
		informerFactory:       informerFactory,
		options:               options,
		registeredHandlers:    registeredHandlers,
	}, nil
}

// UPSTREAM-DIFF: buildExtenders is copied from kubernetes/pkg/scheduler/scheduler.go
// because package scheduler is internal to k8s.io/kubernetes and buildExtenders is not exported.
func buildExtenders(logger klog.Logger, extenders []schedulerapi.Extender, profiles []schedulerapi.KubeSchedulerProfile) ([]fwk.Extender, error) {
	var fExtenders []fwk.Extender
	if len(extenders) == 0 {
		return nil, nil
	}

	var ignoredExtendedResources []string
	var ignorableExtenders []fwk.Extender
	for i := range extenders {
		logger.V(2).Info("Creating extender", "extender", extenders[i])
		extender, err := scheduler.NewHTTPExtender(&extenders[i])
		if err != nil {
			return nil, err
		}
		if !extender.IsIgnorable() {
			fExtenders = append(fExtenders, extender)
		} else {
			ignorableExtenders = append(ignorableExtenders, extender)
		}
		for _, r := range extenders[i].ManagedResources {
			if r.IgnoredByScheduler {
				ignoredExtendedResources = append(ignoredExtendedResources, r.Name)
			}
		}
	}
	// place ignorable extenders to the tail of extenders
	fExtenders = append(fExtenders, ignorableExtenders...)

	// If there are any extended resources found from the Extenders, append them to the pluginConfig for each profile.
	// This should only have an effect on ComponentConfig, where it is possible to configure Extenders and
	// plugin args (and in which case the extender ignored resources take precedence).
	if len(ignoredExtendedResources) == 0 {
		return fExtenders, nil
	}

	for i := range profiles {
		prof := &profiles[i]
		var found = false
		for k := range prof.PluginConfig {
			if prof.PluginConfig[k].Name == noderesources.Name {
				// Update the existing args
				pc := &prof.PluginConfig[k]
				args, ok := pc.Args.(*schedulerapi.NodeResourcesFitArgs)
				if !ok {
					return nil, fmt.Errorf("want args to be of type NodeResourcesFitArgs, got %T", pc.Args)
				}
				args.IgnoredResources = ignoredExtendedResources
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("can't find NodeResourcesFitArgs in plugin config")
		}
	}
	return fExtenders, nil
}

// ProfileMap holds one framework.Framework per scheduler name.
//
// UPSTREAM-DIFF: in upstream PR #140908, FrameworkForPod is a method on profile.Map
// (func (m Map) FrameworkForPod(pod *v1.Pod) (framework.Framework, error)).
// Because k8s.io/kubernetes v1.37.0 does not yet include that method on profile.Map,
// ProfileMap embeds profile.Map to attach FrameworkForPod and FrameworkForPodGroup.
type ProfileMap struct {
	profile.Map
}

// FrameworkForPod returns the framework registered for the pod's scheduler
// name. An empty scheduler name falls back to the default scheduler — this
// cannot happen for pods observed via the API (the field is defaulted there),
// but can for synthetic pods used in scheduling simulations.
func (p *ProfileMap) FrameworkForPod(pod *v1.Pod) (framework.Framework, error) {
	name := pod.Spec.SchedulerName
	if name == "" {
		name = v1.DefaultSchedulerName
	}
	f, ok := p.Map[name]
	if !ok {
		return nil, fmt.Errorf("profile not found for scheduler name %q", name)
	}
	return f, nil
}

// FrameworkForPodGroup returns the framework of the profile requested by the pod group,
// defaulting to the default scheduler name when its member pods do not name one.
//
// UPSTREAM-DIFF: adapted from Scheduler.frameworkForPodGroup in schedule_one_podgroup.go.
// It checks its podgroup members and delegates to FrameworkForPod which looks up the podgroup member profile.
func (p *ProfileMap) FrameworkForPodGroup(podGroupInfo *framework.PodGroupInfo) (framework.Framework, error) {
	pods := podGroupInfo.GetUnscheduledPods()
	if len(pods) == 0 {
		return nil, fmt.Errorf("no pods in pod group")
	}
	return p.FrameworkForPod(pods[0])
}

// NewFrameworkMap builds the map of scheduling framework profiles from the
// resolved configurations and shared components.
// It is not safe for concurrent calls on the same FrameworkComponents instance.
//
// snapshot is injected into every framework profile as its SharedLister and PodGroupManager.
//
// UPSTREAM-DIFF: returns *ProfileMap embedding profile.Map and omits unused upstream options
// (componentConfigVersion, kubeConfig, captureProfile, maxBatchAge).
func NewFrameworkMap(
	ctx context.Context,
	c *FrameworkComponents,
	recorderFactory profile.RecorderFactory,
	snapshot *internalcache.Snapshot,
) (*ProfileMap, error) {
	// UPSTREAM-DIFF: ensure metrics are initialized before constructing profiles.
	InitMetricsOnce()
	registry := frameworkplugins.NewInTreeRegistry()
	if err := registry.Merge(c.options.frameworkOutOfTreeRegistry); err != nil {
		return nil, err
	}
	csiManager := nodevolumelimits.NewCSIManager(
		c.informerFactory.Storage().V1().CSINodes().Lister())

	// UPSTREAM-DIFF: construct a per-ProfileMap draManager (to isolate claimTracker.inFlightAllocations
	// across concurrent snapshots and states) backed by the shared read-only resourceClaimCache,
	// resourceSliceTracker, and extendedResourceCache initialized in FrameworkComponents.
	var draManager fwk.SharedDRAManager
	if feature.DefaultFeatureGate.Enabled(features.DynamicResourceAllocation) {
		baseDRAManager := dynamicresources.NewDRAManager(ctx, c.resourceClaimCache, c.resourceSliceTracker, c.informerFactory)
		draManager = &sharedResolverDRAManager{
			DefaultDRAManager: baseDRAManager,
			resolver:          c.extendedResourceCache,
		}
	}

	opts := []frameworkruntime.Option{
		frameworkruntime.WithClientSet(c.client),
		// UPSTREAM-DIFF: wrap informerFactory so that plugins registering indexers on Pods().Informer()
		// (such as DynamicResources) skip already-registered indexers when NewFrameworkMap is called multiple times.
		frameworkruntime.WithInformerFactory(&dedupPodIndexerInformerFactory{SharedInformerFactory: c.informerFactory}),
		frameworkruntime.WithSharedDRAManager(draManager),
		frameworkruntime.WithSnapshotSharedLister(snapshot),
		frameworkruntime.WithMutableSnapshotLister(snapshot),
		frameworkruntime.WithParallelism(int(c.options.parallelism)),
		frameworkruntime.WithExtenders(c.extenders),
		frameworkruntime.WithMetricsRecorder(c.metricsRecorder),
		frameworkruntime.WithWaitingPods(frameworkruntime.NewWaitingPodsMap()),
		frameworkruntime.WithPodsInPreBind(frameworkruntime.NewPodsInPreBindMap()),
		frameworkruntime.WithAPIDispatcher(c.apiDispatcher),
		frameworkruntime.WithSharedCSIManager(csiManager),
		// UPSTREAM-DIFF: on the library side, PodGroupManager is constructed from snapshot
		// because at the moment of creating ProfileMap the snapshot is already created. On the
		// scheduler side, this should be configurable because the scheduler does not have a
		// snapshot created yet when building the profile map.
		frameworkruntime.WithPodGroupManager(snapshot),
	}

	profiles, err := profile.NewMap(ctx, c.options.profiles, registry, recorderFactory, opts...)
	if err != nil {
		return nil, fmt.Errorf("initializing profiles: %w", err)
	}
	if len(profiles) == 0 {
		return nil, errors.New("at least one profile is required")
	}

	return &ProfileMap{Map: profiles}, nil
}

// UPSTREAM-DIFF: sharedResolverDRAManager wraps DefaultDRAManager so that DeviceClassResolver
// returns the shared ExtendedResourceCache registered once in FrameworkComponents, while keeping
// the per-ProfileMap DefaultDRAManager (and its inFlightAllocations) isolated.
type sharedResolverDRAManager struct {
	*dynamicresources.DefaultDRAManager
	resolver *extendedresourcecache.ExtendedResourceCache
}

func (m *sharedResolverDRAManager) DeviceClassResolver() fwk.DeviceClassResolver {
	return m.resolver
}

// UPSTREAM-DIFF: dedupPodIndexerInformerFactory wraps SharedInformerFactory so that calling
// Pods().Informer().AddIndexers(...) across sequential NewFrameworkMap calls skips indexers
// that are already registered. Without this, plugins like DynamicResources that register a
// per-profile pod indexer during plugin initialization fail with an indexer conflict on subsequent
// profile map creations. Note that this deduplication is not concurrent-safe; NewFrameworkMap
// must not be called concurrently on the same FrameworkComponents.
type dedupPodIndexerInformerFactory struct {
	informers.SharedInformerFactory
}

func (f *dedupPodIndexerInformerFactory) Core() coreinformers.Interface {
	return &dedupPodIndexerCoreInformers{Interface: f.SharedInformerFactory.Core()}
}

type dedupPodIndexerCoreInformers struct {
	coreinformers.Interface
}

func (c *dedupPodIndexerCoreInformers) V1() corev1informers.Interface {
	return &dedupPodIndexerCoreV1Informers{Interface: c.Interface.V1()}
}

type dedupPodIndexerCoreV1Informers struct {
	corev1informers.Interface
}

func (v *dedupPodIndexerCoreV1Informers) Pods() corev1informers.TypedPodInformer {
	return &dedupPodIndexerPodInformer{TypedPodInformer: v.Interface.Pods()}
}

type dedupPodIndexerPodInformer struct {
	corev1informers.TypedPodInformer
}

func (p *dedupPodIndexerPodInformer) Informer() cache.SharedIndexInformer {
	return &dedupIndexersSharedIndexInformer{SharedIndexInformer: p.TypedPodInformer.Informer()}
}

type dedupIndexersSharedIndexInformer struct {
	cache.SharedIndexInformer
}

func (i *dedupIndexersSharedIndexInformer) AddIndexers(indexers cache.Indexers) error {
	existing := i.SharedIndexInformer.GetIndexer().GetIndexers()
	toAdd := cache.Indexers{}
	for name, fn := range indexers {
		if _, ok := existing[name]; !ok {
			toAdd[name] = fn
		}
	}
	if len(toAdd) == 0 {
		return nil
	}
	return i.SharedIndexInformer.AddIndexers(toAdd)
}
