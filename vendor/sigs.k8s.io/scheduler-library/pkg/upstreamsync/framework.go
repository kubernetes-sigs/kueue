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
	"fmt"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/internalversion/scheme"
	"k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	clientset "k8s.io/client-go/kubernetes"
	resourceslicetracker "k8s.io/dynamic-resource-allocation/resourceslice/tracker"
	"k8s.io/klog/v2"
	configv1 "k8s.io/kube-scheduler/config/v1"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/features"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
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

This is a proposal for using common framework initialization logic for both the scheduler and the library.
Some minor adjustments may be necessary to share certain objects with the caller, as e.g., the same snapshot instance needs to be passed to both the Scheduler and the Framework instances.

Common initialization logic would prevent accidental divergence between the library and the scheduler.
The library would need to ensure the provided clientset intercepts and prevents resource mutations.


Extracted from kubernetes/kubernetes/pkg/upstreamsync/scheduler/scheduler.go

*/

type FrameworkCapturer = scheduler.FrameworkCapturer

type frameworkOptions struct {
	profiles                   []schedulerapi.KubeSchedulerProfile
	frameworkOutOfTreeRegistry frameworkruntime.Registry
	applyDefaultProfile        bool
	extenders                  []schedulerapi.Extender
	frameworkCapturer          FrameworkCapturer
	parallelism                int32
}

type Option = func(*frameworkOptions)

type ProfileMap struct {
	profile.Map
}

func (p *ProfileMap) FrameworkForPod(pod *v1.Pod) (framework.Framework, error) {
	schedulerName := pod.Spec.SchedulerName
	// This won't happen if we're dealing with real pods, but may happen when running simulations
	if schedulerName == "" {
		schedulerName = v1.DefaultSchedulerName
	}
	fwk, ok := p.Map[schedulerName]
	if !ok {
		return nil, fmt.Errorf("profile not found for scheduler name %q", pod.Spec.SchedulerName)
	}
	return fwk, nil
}

func WithProfiles(p ...schedulerapi.KubeSchedulerProfile) Option {
	return func(o *frameworkOptions) {
		o.profiles = p
		o.applyDefaultProfile = false
	}
}

func NewProfileMap(ctx context.Context,
	client clientset.Interface,
	informerFactory informers.SharedInformerFactory,
	recorderFactory profile.RecorderFactory,
	podNominator fwk.PodNominator,
	podActivator fwk.PodActivator,
	apiCacher fwk.APICacher,
	snapshot *internalcache.Snapshot,
	opts ...Option) (*ProfileMap, error) {
	logger := klog.FromContext(ctx)
	stopEverything := ctx.Done()

	options := frameworkOptions{
		parallelism:         int32(parallelize.DefaultParallelism),
		applyDefaultProfile: true,
	}
	for _, opt := range opts {
		opt(&options)
	}

	if options.applyDefaultProfile {
		var versionedCfg configv1.KubeSchedulerConfiguration
		scheme.Scheme.Default(&versionedCfg)
		cfg := schedulerapi.KubeSchedulerConfiguration{}
		if err := scheme.Scheme.Convert(&versionedCfg, &cfg, nil); err != nil {
			return nil, err
		}
		options.profiles = cfg.Profiles
	}

	registry := frameworkplugins.NewInTreeRegistry()
	if err := registry.Merge(options.frameworkOutOfTreeRegistry); err != nil {
		return nil, err
	}

	extenders, err := buildExtenders(logger, options.extenders, options.profiles)
	if err != nil {
		return nil, fmt.Errorf("couldn't build extenders: %w", err)
	}

	metricsRecorder := metrics.NewMetricsAsyncRecorder(1000, time.Second, stopEverything)
	// waitingPods holds all the pods that are in the scheduler and waiting in the permit stage
	waitingPods := frameworkruntime.NewWaitingPodsMap()

	// podsInPreBind holds all the pods that are in the scheduler in the preBind phase
	podsInPreBind := frameworkruntime.NewPodsInPreBindMap()

	var resourceClaimCache *assumecache.AssumeCache
	var resourceSliceTracker *resourceslicetracker.Tracker
	var draManager fwk.SharedDRAManager
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
			resourceSliceTrackerOpts.TaintInformer = informerFactory.Resource().V1beta2().DeviceTaintRules()
			resourceSliceTrackerOpts.ClassInformer = informerFactory.Resource().V1().DeviceClasses()
		}
		resourceSliceTracker, err = resourceslicetracker.StartTracker(ctx, resourceSliceTrackerOpts)
		if err != nil {
			return nil, fmt.Errorf("couldn't start resource slice tracker: %w", err)
		}
		draManager = dynamicresources.NewDRAManager(ctx, resourceClaimCache, resourceSliceTracker, informerFactory)
	}
	sharedCSIManager := nodevolumelimits.NewCSIManager(informerFactory.Storage().V1().CSINodes().Lister())

	var apiDispatcher *apidispatcher.APIDispatcher
	if feature.DefaultFeatureGate.Enabled(features.SchedulerAsyncAPICalls) {
		apiDispatcher = apidispatcher.New(client, int(options.parallelism), apicalls.Relevances)
	}

	schedulerCache := internalcache.New(ctx, apiDispatcher, feature.DefaultFeatureGate.Enabled(features.GenericWorkload))

	profileMap, err := profile.NewMap(ctx, options.profiles, registry, recorderFactory,
		frameworkruntime.WithClientSet(client),
		frameworkruntime.WithInformerFactory(informerFactory),
		frameworkruntime.WithPodNominator(podNominator),
		frameworkruntime.WithPodActivator(podActivator),
		frameworkruntime.WithSharedDRAManager(draManager),
		frameworkruntime.WithSnapshotSharedLister(snapshot),
		frameworkruntime.WithCaptureProfile(frameworkruntime.CaptureProfile(options.frameworkCapturer)),
		frameworkruntime.WithParallelism(int(options.parallelism)),
		frameworkruntime.WithExtenders(extenders),
		frameworkruntime.WithMetricsRecorder(metricsRecorder),
		frameworkruntime.WithWaitingPods(waitingPods),
		frameworkruntime.WithPodsInPreBind(podsInPreBind),
		frameworkruntime.WithAPIDispatcher(apiDispatcher),
		frameworkruntime.WithSharedCSIManager(sharedCSIManager),
		frameworkruntime.WithPodGroupManager(schedulerCache),
	)
	if err != nil {
		return nil, err
	}
	for _, fwk := range profileMap {
		fwk.SetAPICacher(apiCacher)
	}
	return &ProfileMap{
		Map: profileMap,
	}, nil
}

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
