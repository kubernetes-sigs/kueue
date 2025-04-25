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
	"context"
	"fmt"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
)

func SetupControllers(ctx context.Context, mgr ctrl.Manager, queues *queue.Manager, cache *cache.Cache, cfg *configapi.Configuration) (string, error) {
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

	if err := waitForPopulatingTasCache(ctx, mgr, cache); err != nil {
		return "waitForPopulatingTasCache", err
	}

	return "", nil
}

func waitForPopulatingTasCache(ctx context.Context, mgr ctrl.Manager, cCache *cache.Cache) error {
	var flvs kueue.ResourceFlavorList
	if err := mgr.GetClient().List(ctx, &flvs); err != nil {
		return fmt.Errorf("Unable to list resource flavors %s", err)
	}
	for _, flv := range flvs.Items {
		if flv.Spec.TopologyName != nil {
			var isCachePopulated bool
			for !isCachePopulated {
				if cCache.TASCache().Get(kueue.ResourceFlavorReference(flv.Name)) == nil {
					time.Sleep(1 * time.Second)
				} else {
					isCachePopulated = true
				}
			}
		}
	}
	return nil
}
