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

package cache

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

func (c *Cache) IsRequired(w *kueue.Workload) bool {
	c.RLock()
	defer c.RUnlock()
	// if _, ok := c.assumedWorkloads[workload.Key(w)]; ok {
	// 	return false
	// }
	for i := range w.Status.AdmissionChecks {
		if ac, ok := c.admissionChecks[w.Status.AdmissionChecks[i].Name]; ok &&
			ac.Controller == kueue.ProvisioningRequestControllerName {
			return true
		}
	}
	return false
}

func (c *Cache) IsReady(w *kueue.Workload) bool {
	if !isRequiredPreChecks(w) {
		return false
	}
	return workload.HasQuotaReservation(w) &&
		workload.HasAllChecksReady(w) &&
		c.IsRequired(w)
}

func isRequiredPreChecks(w *kueue.Workload) bool {
	return features.Enabled(features.TopologyAwareScheduling) &&
		len(w.Status.AdmissionChecks) > 0 &&
		!workload.IsUsingTAS(w)
}
