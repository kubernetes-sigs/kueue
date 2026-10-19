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

package workload

import (
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/resources"
)

// IsPartiallyPreempted reports whether partial preemption shrank, or is
// shrinking, the workload.
func IsPartiallyPreempted(wl *kueue.Workload) bool {
	_, found := wl.Annotations[constants.ReclaimTargetCountsAnnotation]
	return found
}

// ReclaimTargetCounts returns the reclaim target counts set by partial preemption.
// Malformed entries are ignored.
func ReclaimTargetCounts(wl *kueue.Workload) PodSetsCounts {
	value := wl.Annotations[constants.ReclaimTargetCountsAnnotation]
	if value == "" {
		return nil
	}
	targets := make(PodSetsCounts)
	for entry := range strings.SplitSeq(value, ",") {
		name, count, found := strings.Cut(entry, "=")
		n, err := strconv.ParseInt(count, 10, 32)
		if found && err == nil && n >= 0 {
			targets[kueue.PodSetReference(name)] = int32(n)
		}
	}
	return targets
}

// SetReclaimTargetCounts sets the reclaim target counts of partial preemption.
func SetReclaimTargetCounts(wl *kueue.Workload, targets PodSetsCounts) {
	entries := make([]string, 0, len(targets))
	for _, name := range slices.Sorted(maps.Keys(targets)) {
		entries = append(entries, fmt.Sprintf("%s=%d", name, targets[name]))
	}
	metav1.SetMetaDataAnnotation(&wl.ObjectMeta, constants.ReclaimTargetCountsAnnotation, strings.Join(entries, ","))
}

// PendingReclaimTargetCounts returns the reclaim target counts of the PodSets
// that are still admitted above them, so their quota is not released yet.
func PendingReclaimTargetCounts(wl *kueue.Workload) PodSetsCounts {
	if wl.Status.Admission == nil {
		return nil
	}
	targets := ReclaimTargetCounts(wl)
	var pending PodSetsCounts
	counts := ExtractPodSetCountsFromWorkload(wl)
	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if target, found := targets[psa.Name]; found && target < ptr.Deref(psa.Count, counts[psa.Name]) {
			if pending == nil {
				pending = make(PodSetsCounts)
			}
			pending[psa.Name] = target
		}
	}
	return pending
}

// ReleaseReclaimedQuota lowers the admitted counts, with their resource usage,
// to the pending reclaim targets. It returns whether any count changed.
func ReleaseReclaimedQuota(wl *kueue.Workload) bool {
	targets := PendingReclaimTargetCounts(wl)
	counts := ExtractPodSetCountsFromWorkload(wl)
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		target, found := targets[psa.Name]
		if !found {
			continue
		}
		admitted := PodSetResources{
			Requests: resources.NewRequestsFromResourceList(psa.ResourceUsage),
			Count:    ptr.Deref(psa.Count, counts[psa.Name]),
		}
		psa.ResourceUsage = admitted.ScaledTo(target).Requests.ToResourceList(resources.NewResourceFormatter())
		psa.Count = new(target)
	}
	return len(targets) > 0
}
