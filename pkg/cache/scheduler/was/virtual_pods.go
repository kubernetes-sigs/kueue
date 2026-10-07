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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/podset"
	utilpodset "sigs.k8s.io/kueue/pkg/util/podset"
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

func newVirtualPod(wl *kueue.Workload, psName string, replicaIdx int, labels, annotations map[string]string, spec *corev1.PodSpec, phase corev1.PodPhase) *corev1.Pod {
	pod := &corev1.Pod{
		Name:        virtualPodName(wl.Name, psName, replicaIdx),
		Namespace:   wl.Namespace,
		UID:         types.UID(fmt.Sprintf("virtual-%s-%s-%d", wl.UID, psName, replicaIdx)),
		Labels:      maps.Clone(labels),
		Annotations: maps.Clone(annotations),
		Spec:        *spec.DeepCopy(),
		Status: corev1.PodStatus{
			Phase: phase,
		},
	}
	// Add PodSet label
	if pod.Labels == nil {
		pod.Labels = make(map[string]string)
	}
	pod.Labels[constants.PodSetLabel] = psName

	// Add Workload annotation
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[kueue.WorkloadAnnotation] = wl.Name

	return pod
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

		ps := utilpodset.FindPodSetByName(wl.Spec.PodSets, psa.Name)
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
				pod := newVirtualPod(wl, string(psa.Name), replicaIdx, ps.Template.Labels, ps.Template.Annotations, &ps.Template.Spec, corev1.PodRunning)
				pod.Spec.NodeName = nodeName
				virtualPods = append(virtualPods, pod)
				replicaIdx++
			}
		}
	}

	return virtualPods
}

// CandidatePodOptions holds the options for creating a candidate pod.
type CandidatePodOptions struct {
	PodSpec           *corev1.PodSpec
	FlavorNodeLabels  map[string]string
	FlavorTolerations []corev1.Toleration
	PodSetUpdates     []kueue.PodSetUpdate
}

// CandidateVirtualPodsForPodSet returns candidate virtual pods for the specified count
// of replicas for a PodSet, merging PodSet templates, assigned flavor details, and admission check updates.
func CandidateVirtualPodsForPodSet(wl *kueue.Workload, ps *kueue.PodSet, count int32, opts CandidatePodOptions) ([]*corev1.Pod, error) {
	if wl == nil || ps == nil {
		return nil, errors.New("workload and podset must be non-nil")
	}

	info := podset.FromPodSet(ps)
	baseSpec := &ps.Template.Spec
	if opts.PodSpec != nil {
		baseSpec = opts.PodSpec
		info.NodeSelector = maps.Clone(opts.PodSpec.NodeSelector)
		info.Tolerations = slices.Clone(opts.PodSpec.Tolerations)
	}

	if len(opts.FlavorNodeLabels) > 0 || len(opts.FlavorTolerations) > 0 {
		flavorInfo := podset.PodSetInfo{
			NodeSelector: opts.FlavorNodeLabels,
			Tolerations:  opts.FlavorTolerations,
		}
		if err := info.Merge(flavorInfo); err != nil {
			return nil, fmt.Errorf("failed to merge ResourceFlavor for PodSet %s: %w", ps.Name, err)
		}
	}

	for _, u := range opts.PodSetUpdates {
		if err := info.Merge(podset.FromUpdate(&u)); err != nil {
			return nil, fmt.Errorf("failed to merge PodSetUpdate for PodSet %s: %w", ps.Name, err)
		}
	}

	spec := baseSpec.DeepCopy()
	spec.NodeSelector = info.NodeSelector
	spec.Tolerations = info.Tolerations

	pods := make([]*corev1.Pod, 0, count)
	for replicaIdx := range int(count) {
		pod := newVirtualPod(wl, string(ps.Name), replicaIdx, info.Labels, info.Annotations, spec, corev1.PodPending)
		pods = append(pods, pod)
	}
	return pods, nil
}
