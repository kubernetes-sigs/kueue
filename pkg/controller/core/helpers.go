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

package core

import (
	"k8s.io/apimachinery/pkg/api/equality"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilmath "sigs.k8s.io/kueue/pkg/util/math"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
)

// clusterQueueSpecOrQuotaUpdated reports whether a ClusterQueue update changed
// its spec or its effective resource groups, as opposed to only its status.
func clusterQueueSpecOrQuotaUpdated(oldCQ, newCQ *kueue.ClusterQueue) bool {
	return !equality.Semantic.DeepEqual(oldCQ.Spec, newCQ.Spec) ||
		!equality.Semantic.DeepEqual(resourcegroups.EffectiveResourceGroups(oldCQ), resourcegroups.EffectiveResourceGroups(newCQ))
}

// WeightedShare rounds a share up to the int64 that FairSharingStatus reports,
// math.MaxInt64 for +Inf and for any share above the int64 range.
func WeightedShare(f float64) int64 {
	return utilmath.SaturatingCeil(f)
}
