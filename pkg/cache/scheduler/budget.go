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
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
)

type WallTimeFlavorGroup struct {
	Flavors []kueue.ResourceFlavorReference
	// The set of key labels from all flavors.
	// Those keys define the affinity terms of a workload
	// that can be matched against the flavors.
	LabelKeys sets.Set[string]
}

func (rg *WallTimeFlavorGroup) Clone() WallTimeFlavorGroup {
	return WallTimeFlavorGroup{
		Flavors:   rg.Flavors,
		LabelKeys: rg.LabelKeys.Clone(),
	}
}

type WallTimeResourceQuota struct {
	WallTimeAllocatedHours int32
}

func createWallTimeResourceQuota(kueueRgs []kueue.WallTimeFlavor) map[resources.FlavorWallTimeResource]WallTimeResourceQuota {
	frCount := len(kueueRgs)
	quotas := make(map[resources.FlavorWallTimeResource]WallTimeResourceQuota, frCount)
	for _, bq := range kueueRgs {
		quotas[resources.FlavorWallTimeResource{Flavor: bq.Name}] = WallTimeResourceQuota{bq.WallTimeAllocatedHours}
	}
	return quotas
}
