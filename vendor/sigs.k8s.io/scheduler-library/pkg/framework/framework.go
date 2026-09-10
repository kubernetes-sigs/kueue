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

package framework

import (
	"context"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/events"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler"
	schedulerapi "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	"k8s.io/kubernetes/pkg/scheduler/metrics"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"
)

// InitMetricsOnce constructs the kube-scheduler metric objects. They are package-level variables
// that stay nil until metrics.InitMetrics is called, and the framework and the plugins record into
// them unconditionally, so they have to be initialized before any framework is built or the first
// recording panics.
//
// The initialization is process-wide, while a process may create many ProfileMaps (e.g. one per
// SchedulingSimulator), each having to make sure the metrics exist without knowing about the
// others. sync.OnceFunc lets them all call it while the metric objects are only ever built once,
// so the values recorded so far are not thrown away by a later initialization.
var InitMetricsOnce = sync.OnceFunc(metrics.InitMetrics)

// NewProfileMap builds the scheduling profiles (one framework.Framework per scheduler name)
// used by the library to run the scheduling cycle in memory.
//
// It wires the upstream framework with the no-op implementations of the extension points that
// only make sense for a real, cluster-connected scheduler: events are discarded, pods are never
// nominated nor activated and no API calls are issued. This keeps the simulation purely in-memory
// while still running the very same plugins as kube-scheduler.
//
// The client is used by the plugins and the informers to read the cluster state; it must never be
// allowed to mutate it (see simulator.ReadonlyClient). The snap is shared with all the built
// frameworks, so mutating it in place is immediately visible to every plugin.
// A nil informerFactory makes the function create one, and a nil cfg selects the default profile.
func NewProfileMap(ctx context.Context, client kubernetes.Interface, informerFactory informers.SharedInformerFactory, snap *cache.Snapshot, cfg *schedulerapi.KubeSchedulerConfiguration) (*upstreamsync.ProfileMap, error) {
	InitMetricsOnce()

	recorderFactory := func(name string) events.EventRecorderLogger {
		return &discardEventRecorder{}
	}

	if informerFactory == nil {
		informerFactory = scheduler.NewInformerFactory(client, 0, nil)
	}

	opts := []upstreamsync.Option{}
	if cfg != nil {
		opts = append(opts, upstreamsync.WithProfiles(cfg.Profiles...))
	}

	return upstreamsync.NewProfileMap(
		ctx,
		client,
		informerFactory,
		recorderFactory,
		&noopPodNominator{},
		&noopPodActivator{},
		&noopAPICacher{},
		snap,
		opts...,
	)
}

// discardEventRecorder drops all the events emitted by the plugins.
// Simulations run against a read-only view of the cluster, so they must not
// report anything back to the API server.
type discardEventRecorder struct{}

var _ events.EventRecorderLogger = &discardEventRecorder{}

func (d *discardEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
}

func (d *discardEventRecorder) WithLogger(logger klog.Logger) events.EventRecorderLogger {
	return d
}

// noopPodNominator ignores all the nominations. Nomination is a kube-scheduler mechanism for
// reserving a node for a pod awaiting preemption across scheduling cycles. The library has no
// scheduling queue and exposes preemption explicitly so there is nothing to nominate to.
type noopPodNominator struct{}

var _ fwk.PodNominator = &noopPodNominator{}

func (n *noopPodNominator) AddNominatedPod(logger klog.Logger, pod fwk.PodInfo, nominatingInfo *fwk.NominatingInfo) {
}
func (n *noopPodNominator) DeleteNominatedPodIfExists(pod *v1.Pod) {}
func (n *noopPodNominator) UpdateNominatedPod(logger klog.Logger, oldPod *v1.Pod, newPodInfo fwk.PodInfo) {
}
func (n *noopPodNominator) NominatedPodsForNode(nodeName string) []fwk.PodInfo {
	return nil
}

// noopPodActivator ignores all the requests to move pods back to the active queue,
// as the library has no scheduling queue.
type noopPodActivator struct{}

var _ fwk.PodActivator = &noopPodActivator{}

func (a *noopPodActivator) Activate(logger klog.Logger, pods map[string]*v1.Pod) {}

// noopAPICacher pretends that every asynchronous API call the plugins request has already
// succeeded, without ever contacting the API server. Simulations must not mutate the cluster,
// so binding a pod or patching its status is reported as an immediately completed no-op.
type noopAPICacher struct{}

var _ fwk.APICacher = &noopAPICacher{}

func (c *noopAPICacher) PatchPodStatus(pod *v1.Pod, conditions []*v1.PodCondition, nominatingInfo *fwk.NominatingInfo) (<-chan error, error) {
	ch := make(chan error)
	close(ch)
	return ch, nil
}

func (c *noopAPICacher) BindPod(binding *v1.Binding) (<-chan error, error) {
	ch := make(chan error)
	close(ch)
	return ch, nil
}

func (c *noopAPICacher) WaitOnFinish(ctx context.Context, onFinish <-chan error) error {
	if onFinish == nil {
		return nil
	}
	select {
	case err := <-onFinish:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
