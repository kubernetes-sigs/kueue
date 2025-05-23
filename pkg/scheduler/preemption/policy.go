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

package preemption

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
)

// CanAlwaysReclaim indicates that the CQ is guaranteed to
// be able to reclaim the capacity of workloads borrowing
// its capacity.
func CanAlwaysReclaim(cq *cache.ClusterQueueSnapshot) bool {
	return cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyAny
}
