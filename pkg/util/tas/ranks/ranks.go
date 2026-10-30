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

// Package ranks provides helpers for the rank-based ordering of pods, where a
// pod's rank is read from its index label (e.g. the Job completion index).
//
// It is a sub-package of util/tas rather than part of it because it reads pod
// labels via util/pod, which util/tas cannot import: the testing wrappers used
// by util/pod's tests already depend on util/tas.
package ranks

import (
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
)

// ErrInvalidSubGroupCount reports a PodSet whose SubGroupIndexLabel is set but
// whose SubGroupCount is missing or not positive.
var ErrInvalidSubGroupCount = errors.New("invalid subgroup count for podset with subgroup index label")

// PodSetRankOffsets returns, per PodSet, the index of its first pod (rankOffsets),
// derived from how the assignments are grouped by PodSetGroupName.
func PodSetRankOffsets(
	psas []kueue.PodSetAssignment,
	psNameToTopologyRequest map[kueue.PodSetReference]*kueue.PodSetTopologyRequest,
) map[kueue.PodSetReference]int32 {
	groupedPodSetAssignments := make(map[string][]*kueue.PodSetAssignment)
	for i := range psas {
		psa := &psas[i]
		groupName := strconv.Itoa(i)
		if tr := psNameToTopologyRequest[psa.Name]; tr != nil && tr.PodSetGroupName != nil {
			groupName = *tr.PodSetGroupName
		}
		groupedPodSetAssignments[groupName] = append(groupedPodSetAssignments[groupName], psa)
	}

	rankOffsets := make(map[kueue.PodSetReference]int32)
	for _, group := range groupedPodSetAssignments {
		if len(group) > 1 {
			// In case of LeaderWorkerSet, in each Workload there will be
			// 1 leader and N workers. Leader will get rank 0 and workers
			// 1, 2, ..., N. To detect the leader we are selecting PodSet
			// which is smaller.
			smallerPsa := group[0]
			largerPsa := group[1]
			if *smallerPsa.Count > *largerPsa.Count {
				smallerPsa = group[1]
				largerPsa = group[0]
			}
			rankOffsets[smallerPsa.Name] = 0
			rankOffsets[largerPsa.Name] = *smallerPsa.Count
		} else {
			rankOffsets[group[0].Name] = 0
		}
	}
	return rankOffsets
}

// ReadRanksForLabels does not check the ranks against the PodSet count or the
// number of pods the topology assignment covers; the caller does that, because
// whether a rank without a domain is allowed depends on the workload.
func ReadRanksForLabels(
	psa *kueue.PodSetAssignment,
	pods []*corev1.Pod,
	psReq *kueue.PodSetTopologyRequest,
	offset int32) (map[int]*corev1.Pod, error) {
	result := make(map[int]*corev1.Pod)
	podSetSize := int(*psa.Count)
	singleJobSize := podSetSize
	if psReq.SubGroupIndexLabel != nil {
		if psReq.SubGroupCount == nil || *psReq.SubGroupCount <= 0 {
			return nil, fmt.Errorf("%w %q: must be set and greater than 0", ErrInvalidSubGroupCount, *psReq.SubGroupIndexLabel)
		}
		singleJobSize = podSetSize / int(*psReq.SubGroupCount)
	}

	for _, pod := range pods {
		podIndex, err := utilpod.ReadUIntFromLabel(pod, *psReq.PodIndexLabel)
		if err != nil {
			// the Pod has no rank information - ranks cannot be used
			return nil, err
		}
		rank := *podIndex - int(offset)
		if psReq.SubGroupIndexLabel != nil {
			jobIndex, err := utilpod.ReadUIntFromLabelBelowBound(pod, *psReq.SubGroupIndexLabel, int(*psReq.SubGroupCount))
			if err != nil {
				// the Pod has no Job index information - ranks cannot be used
				return nil, err
			}
			if *podIndex >= singleJobSize {
				// the pod index exceeds size, this scenario is not
				// supported by the rank-based ordering of pods.
				return nil, fmt.Errorf("pod index %v of Pod %q exceeds the single Job size: %v", *podIndex, klog.KObj(pod), singleJobSize)
			}
			rank = *podIndex + *jobIndex*singleJobSize - int(offset)
		}
		if rank < 0 {
			return nil, fmt.Errorf("rank %v of Pod %q is below 0", rank, klog.KObj(pod))
		}
		if _, found := result[rank]; found {
			// there is a conflict in ranks, they cannot be used
			return nil, fmt.Errorf("conflicting rank %v found for pod %q", rank, klog.KObj(pod))
		}
		result[rank] = pod
	}
	return result, nil
}
