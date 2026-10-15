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

package workloadslicing

import (
	"context"
	"fmt"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// EvictionCleanupToken identifies the current eviction, including repeated
// evictions within the second precision of metav1.Time.
func EvictionCleanupToken(wl *kueue.Workload) string {
	condition := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadEvicted)
	if condition == nil || !workloadevict.IsEvicted(wl) {
		return ""
	}
	var count int64
	if wl.Status.SchedulingStats != nil {
		for _, eviction := range wl.Status.SchedulingStats.Evictions {
			count += int64(eviction.Count)
		}
	}
	return fmt.Sprintf("%s/%d", condition.LastTransitionTime.UTC().Format(time.RFC3339), count)
}

// EvictionCleanupPending deliberately includes Finished slices. Replacement is
// not proof that the remote RayCluster was deleted.
func EvictionCleanupPending(wl *kueue.Workload) bool {
	token := EvictionCleanupToken(wl)
	return token != "" && wl.Annotations[constants.MultiKueueEvictionCleanupAnnotation] != token
}

// FindPendingEviction includes every slice in the chain, including finished
// ancestors and the candidate itself. A direct reader avoids informer lag at
// the quota-reservation boundary.
func FindPendingEviction(ctx context.Context, reader client.Reader, wl *kueue.Workload) (*kueue.Workload, error) {
	list := &kueue.WorkloadList{}
	opts := []client.ListOption{client.InNamespace(wl.Namespace)}
	if uid := wl.Labels[constants.JobUIDLabel]; uid != "" {
		opts = append(opts, client.MatchingLabels{constants.JobUIDLabel: uid})
	}
	if err := reader.List(ctx, list, opts...); err != nil {
		return nil, err
	}
	for i := range list.Items {
		candidate := &list.Items[i]
		if SliceName(candidate) == SliceName(wl) && EvictionCleanupPending(candidate) {
			return candidate, nil
		}
	}
	return nil, nil
}
