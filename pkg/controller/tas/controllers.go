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

package tas

import (
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// SetupControllersOption configures TAS controller setup.
type SetupControllersOption func(*setupControllersOptions)

type setupControllersOptions struct {
	podUsageOpts []podUsageOption
}

// WithRequeueBatchInterval overrides the interval at which freed non-TAS
// capacity triggers requeue of inadmissible workloads. Defaults to 10s.
func WithRequeueBatchInterval(d time.Duration) SetupControllersOption {
	return func(o *setupControllersOptions) {
		o.podUsageOpts = append(o.podUsageOpts, withRequeueBatchInterval(d))
	}
}

func SetupControllers(
	mgr ctrl.Manager,
	queues *qcache.Manager,
	cache *schdcache.Cache,
	cfg *configapi.Configuration,
	roleTracker *roletracker.RoleTracker,
	opts ...SetupControllersOption,
) (string, error) {
	var options setupControllersOptions
	for _, opt := range opts {
		opt(&options)
	}

	recorder := mgr.GetEventRecorder(TASResourceFlavorController)
	topologyRec := newTopologyReconciler(mgr.GetClient(), queues, cache, roleTracker)
	if ctrlName, err := topologyRec.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	rfRec := newRfReconciler(mgr.GetClient(), queues, cache, recorder, roleTracker)
	if ctrlName, err := rfRec.setupWithManager(mgr, cache, cfg); err != nil {
		return ctrlName, err
	}
	topologyUngater := newTopologyUngater(mgr.GetClient(), roleTracker)
	if ctrlName, err := topologyUngater.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	nodeRec := newNodeReconciler(mgr.GetClient(), recorder, cache, roleTracker, WithWatchers(rfRec))
	if ctrlName, err := nodeRec.SetupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	podUsageController := newPodUsageReconciler(mgr.GetClient(), queues, cache, roleTracker, options.podUsageOpts...)
	if ctrlName, err := podUsageController.SetupWithManager(mgr); err != nil {
		return ctrlName, err
	}
	if err := mgr.Add(podUsageController); err != nil {
		return TASPodUsageController, fmt.Errorf(
			"unable to add pod usage requeue drainer: %w",
			err,
		)
	}
	return "", nil
}
