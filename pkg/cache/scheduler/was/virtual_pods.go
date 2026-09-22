//go:build !exclude_scheduler_library

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

package was

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"maps"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/podset"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload/finish"
)

const (
	maxPodNameLength = 253
	hashLength       = 5
)

// virtualPodName returns a collision-free name for a virtual pod
// following the standard naming convention in Kueue
func virtualPodName(wlName, podSetName string, index int) string {
	indexStr := strconv.Itoa(index)
	hash := getVirtualPodHash(wlName, podSetName, indexStr)
	suffix := fmt.Sprintf("-%s-%s", indexStr, hash)
	maxPrefix := maxPodNameLength - len(suffix)

	prefix := fmt.Sprintf("virtual-%s-%s", wlName, podSetName)
	if len(prefix) > maxPrefix {
		prefix = prefix[:maxPrefix]
	}

	return prefix + suffix
}

func getVirtualPodHash(wlName, podSetName, indexStr string) string {
	h := sha1.New()
	h.Write([]byte(wlName))
	h.Write([]byte("\n"))
	h.Write([]byte(podSetName))
	h.Write([]byte("\n"))
	h.Write([]byte(indexStr))

	return hex.EncodeToString(h.Sum(nil))[:hashLength]
}

// VirtualPodsForWorkload generates virtual pods for an admitted or quota-reserved
// workload based on its PodSets and TopologyAssignments.
func VirtualPodsForWorkload(wl *kueue.Workload) (virtualPods []*corev1.Pod) {
	if wl == nil || wl.Status.Admission == nil || finish.IsFinished(wl) {
		return nil
	}

	for _, psa := range wl.Status.Admission.PodSetAssignments {
		if psa.TopologyAssignment == nil || len(psa.TopologyAssignment.Levels) == 0 || !utiltas.IsLowestLevelHostname(psa.TopologyAssignment.Levels) {
			continue
		}

		ps := podset.FindPodSetByName(wl.Spec.PodSets, psa.Name)
		if ps == nil {
			continue
		}

		levels := psa.TopologyAssignment.Levels
		replicaIdx := 0

		for domain := range utiltas.InternalSeqFrom(psa.TopologyAssignment) {
			nodeName, hasNode := utiltas.NodeNameFromDomainID(levels, utiltas.DomainID(domain.Values))
			if !hasNode {
				return nil
			}

			for range domain.Count {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:        virtualPodName(wl.Name, string(psa.Name), replicaIdx),
						Namespace:   wl.Namespace,
						UID:         types.UID(fmt.Sprintf("virtual-%s-%s-%d", wl.UID, psa.Name, replicaIdx)),
						Labels:      maps.Clone(ps.Template.Labels),
						Annotations: maps.Clone(ps.Template.Annotations),
					},
					Spec: *ps.Template.Spec.DeepCopy(),
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
					},
				}

				if pod.Labels == nil {
					pod.Labels = make(map[string]string)
				}
				pod.Labels[constants.PodSetLabel] = string(psa.Name)

				if pod.Annotations == nil {
					pod.Annotations = make(map[string]string)
				}
				pod.Annotations[kueue.WorkloadAnnotation] = wl.Name

				pod.Spec.NodeName = nodeName

				virtualPods = append(virtualPods, pod)
				replicaIdx++
			}
		}
	}

	return virtualPods
}
