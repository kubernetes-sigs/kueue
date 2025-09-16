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

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
)

func SetupControllers(mgr ctrl.Manager, queues *qcache.Manager, cache *schdcache.Cache, cfg *configapi.Configuration) (string, error) {
	recorder := mgr.GetEventRecorderFor(TASResourceFlavorController)
	topologyRec := newTopologyReconciler(mgr.GetClient(), queues, cache)
	if ctrlName, err := topologyRec.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	rfRec := newRfReconciler(mgr.GetClient(), queues, cache, recorder)
	if ctrlName, err := rfRec.setupWithManager(mgr, cache, cfg); err != nil {
		return ctrlName, err
	}
	topologyUngater := newTopologyUngater(mgr.GetClient())
	if ctrlName, err := topologyUngater.setupWithManager(mgr, cfg); err != nil {
		return ctrlName, err
	}
	if features.Enabled(features.TASFailedNodeReplacement) {
		nodeFailureReconciler := newNodeFailureReconciler(mgr.GetClient(), recorder)
		if ctrlName, err := nodeFailureReconciler.SetupWithManager(mgr, cfg); err != nil {
			return ctrlName, err
		}
	}
	return "", nil
}
