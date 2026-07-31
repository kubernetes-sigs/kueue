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

package dra

import (
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/pkg/controller/core"
	"sigs.k8s.io/kueue/pkg/features"
)

// checkResourceSliceAPIAvailable returns true when at least one of the ResourceSlice-dependent
// feature gates is enabled and the ResourceSlice API (resource.k8s.io/v1) is available
// on the cluster.
func CheckResourceSliceAPIAvailable(mgr ctrl.Manager) bool {
	if features.Enabled(features.KueueDRAIntegrationPartitionableDevices) || features.Enabled(features.KueueDRAIntegrationConsumableCapacity) {
		if err := core.ServerSupportsResourceSlice(mgr); err != nil {
			ctrl.Log.V(0).Info("ResourceSlice API not available, skipping DRA partitionable and consumable capacity features", "reason", err)
		} else {
			return true
		}
	}
	return false
}
