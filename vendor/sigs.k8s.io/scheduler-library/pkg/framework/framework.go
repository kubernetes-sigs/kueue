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

var InitMetricsOnce = sync.OnceFunc(metrics.InitMetrics)

func NewProfileMap(ctx context.Context, client kubernetes.Interface, informerFactory informers.SharedInformerFactory, snap *cache.Snapshot, cfg *schedulerapi.KubeSchedulerConfiguration) (*upstreamsync.ProfileMap, error) {
	InitMetricsOnce()

	recorderFactory := func(name string) events.EventRecorderLogger {
		return &discardEventRecorder{}
	}

	if informerFactory == nil {
		informerFactory = scheduler.NewInformerFactory(client, 0)
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

type discardEventRecorder struct{}

var _ events.EventRecorderLogger = &discardEventRecorder{}

func (d *discardEventRecorder) Eventf(regarding runtime.Object, related runtime.Object, eventtype, reason, action, note string, args ...interface{}) {
}

func (d *discardEventRecorder) WithLogger(logger klog.Logger) events.EventRecorderLogger {
	return d
}

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

type noopPodActivator struct{}

var _ fwk.PodActivator = &noopPodActivator{}

func (a *noopPodActivator) Activate(logger klog.Logger, pods map[string]*v1.Pod) {}

type noopAPICacher struct{}

var _ fwk.APICacher = &noopAPICacher{}

func (c *noopAPICacher) PatchPodStatus(pod *v1.Pod, condition *v1.PodCondition, nominatingInfo *fwk.NominatingInfo) (<-chan error, error) {
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
