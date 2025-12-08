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
	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
)

// SetupOption configures the TAS controllers setup.
type SetupOption func(*setupOptions)

type setupOptions struct {
	roleTracker *roletracker.RoleTracker
}

// WithRoleTracker sets the roleTracker for TAS controllers.
func WithRoleTracker(tracker *roletracker.RoleTracker) SetupOption {
	return func(o *setupOptions) {
		o.roleTracker = tracker
	}
}

func SetupControllers(mgr ctrl.Manager, queues *qcache.Manager, cache *schdcache.Cache, cfg *configapi.Configuration, opts ...SetupOption) (string, error) {
	options := &setupOptions{}
	for _, opt := range opts {
		opt(options)
	}
	recorder := mgr.GetEventRecorderFor(TASResourceFlavorController)
	topologyRec := newTopologyReconciler(mgr.GetClient(), queues, cache, options.roleTracker)
	if ctrlName, err := topologyRec.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	rfRec := newRfReconciler(mgr.GetClient(), queues, cache, recorder, options.roleTracker)
	if ctrlName, err := rfRec.setupWithManager(mgr, cache, cfg); err != nil {
		return ctrlName, err
	}
	topologyUngater := newTopologyUngater(mgr.GetClient(), options.roleTracker)
	if ctrlName, err := topologyUngater.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	if features.Enabled(features.TASFailedNodeReplacement) {
		nodeFailureReconciler := newNodeFailureReconciler(mgr.GetClient(), recorder, options.roleTracker)
		if ctrlName, err := nodeFailureReconciler.SetupWithManager(mgr, cfg); err != nil {
			return ctrlName, err
		}
	}
	return "", nil
}
