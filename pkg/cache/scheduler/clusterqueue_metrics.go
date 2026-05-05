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

package scheduler

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
)

func (c *Cache) RecordClusterQueueResourceMetrics(log logr.Logger, cqName kueue.ClusterQueueReference) {
	log.V(4).Info("Recording resource metrics for ClusterQueue")

	c.RLock()
	defer c.RUnlock()

	cq := c.hm.ClusterQueue(cqName)
	if cq == nil {
		return
	}

	cq.reportResourceMetrics(c.fairSharingEnabled)
}

func (c *Cache) ClearClusterQueueOldResourceMetrics(log logr.Logger, oldCq *kueue.ClusterQueue) {
	c.RLock()
	defer c.RUnlock()

	cqName := kueue.ClusterQueueReference(oldCq.Name)
	log.V(4).Info("Clearing old resource metrics for ClusterQueue")

	newCq := c.hm.ClusterQueue(cqName)
	if newCq == nil {
		return
	}

	for rgi := range oldCq.Spec.ResourceGroups {
		oldRg := &oldCq.Spec.ResourceGroups[rgi]
		newFlavorSet := sets.New[kueue.ResourceFlavorReference]()
		if rgi < len(newCq.ResourceGroups) && len(newCq.ResourceGroups[rgi].Flavors) > 0 {
			newFlavorSet.Insert(newCq.ResourceGroups[rgi].Flavors...)
		}

		for fi := range oldRg.Flavors {
			flavor := &oldRg.Flavors[fi]
			if !newFlavorSet.Has(flavor.Name) {
				metrics.ClearClusterQueueResourceQuotas(oldCq.Name, string(flavor.Name), "")
				metrics.ClearClusterQueueResourceReservations(oldCq.Name, string(flavor.Name), "")
				metrics.ClearClusterQueueResourceUsage(oldCq.Name, string(flavor.Name), "")
			} else {
				for ri := range flavor.Resources {
					fr := resources.FlavorResource{Flavor: flavor.Name, Resource: flavor.Resources[ri].Name}
					if _, found := newCq.resourceNode.Quotas[fr]; !found {
						metrics.ClearClusterQueueResourceQuotas(oldCq.Name, string(fr.Flavor), string(fr.Resource))
						metrics.ClearClusterQueueResourceReservations(oldCq.Name, string(fr.Flavor), string(fr.Resource))
						metrics.ClearClusterQueueResourceUsage(oldCq.Name, string(fr.Flavor), string(fr.Resource))
					}
				}
			}
		}
	}
}
