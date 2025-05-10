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

func SetupControllers(mgr ctrl.Manager, queues *queue.Manager, cache *cache.Cache, cfg *configapi.Configuration) (string, error) {
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

	// Set up the TAS flavor cache in the background. This is because mgr is configured to list from a cache
	// and the cache is not populated until the controller is started.
	go func() {
		err := syncTasCache(mgr, cache)
		if err != nil {
			panic(fmt.Sprintf("failed to populate TAS cache: %v", err))
		}
	}()

	return "", nil
}

func syncTasCache(mgr ctrl.Manager, cCache *cache.Cache) error {
	// Wait for the cache to be synced before listing resources. Else it will fail.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if ok := mgr.GetCache().WaitForCacheSync(ctx); !ok {
		return fmt.Errorf("failed to wait for caches to sync")
	}

	var flvs kueue.ResourceFlavorList
	// Wait for the flavors to be populated in the cache. We suppose that we are down when we are
	// in sync with the catched list fetched above. We also suppose that the flavors are not
	// created/recreated in the meantime.
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for flavors to be populated in the cache: %w", ctx.Err())
		default:
		}
		if err := mgr.GetClient().List(ctx, &flvs); err != nil {
			return fmt.Errorf("unable to list resource flavors: %w", err)
		}
		allFound := true
		for _, flv := range flvs.Items {
			if flv.Spec.TopologyName != nil {
				for {
					if cCache.TASCache().Get(kueue.ResourceFlavorReference(flv.Name)) != nil {
						break
					}
					allFound = false
					time.Sleep(1 * time.Second)
					break
				}
			}
		}
		if allFound {
			cCache.TASCache().SetSyncedFlavors(true)
			return nil
		}
	}
}
