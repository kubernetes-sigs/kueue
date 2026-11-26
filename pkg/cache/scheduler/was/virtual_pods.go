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
	utilstrings "sigs.k8s.io/kueue/pkg/util/strings"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload/finish"
)

const (
	maxPodNameLength = 253
	hashLength       = 5
)

var (
	errWorkloadOrPodSetIsNil = errors.New("workload and podset must not be nil")
	errMergeResourceFlavor   = errors.New("failed to merge ResourceFlavor")
	errMergePodSetUpdate     = errors.New("failed to merge PodSetUpdate")
)

// VirtualPods holds the generated virtual pods for a Workload along with
// the Workload's ResourceVersion at the time of generation.
type VirtualPods struct {
	// workloadResourceVersion of the parent Workload. When the Workload's ResourceVersion
	// changes (e.g. status or admission changes), the cached pods are invalidated.
	workloadResourceVersion string
	pods                    []*corev1.Pod
}

// Pods returns the underlying virtual pods slice.
func (v VirtualPods) Pods() []*corev1.Pod {
	return v.pods
}

// virtualPodName returns a collision-free name for a virtual pod
// following the standard naming convention in Kueue
func virtualPodName(wlName, podSetName string, index int) string {
	indexStr := strconv.Itoa(index)
	hash := getVirtualPodHash(wlName, podSetName, indexStr)
	suffix := fmt.Sprintf("-%s-%s", indexStr, hash)
	maxPrefix := maxPodNameLength - len(suffix)

	prefix := fmt.Sprintf("virtual-%s-%s", wlName, podSetName)
	return utilstrings.TruncateNamePrefix(prefix, maxPrefix) + suffix
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
func VirtualPodsForWorkload(wl *kueue.Workload) VirtualPods {
	if wl == nil || wl.Status.Admission == nil || finish.IsFinished(wl) {
		return VirtualPods{}
	}

	var virtualPods []*corev1.Pod

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
				return VirtualPods{}
			}

			for range domain.Count {
				pod := newVirtualPod(wl, string(psa.Name), replicaIdx, ps.Template.Labels, ps.Template.Annotations, &ps.Template.Spec, corev1.PodRunning)
				pod.Spec.NodeName = nodeName
				virtualPods = append(virtualPods, pod)
				replicaIdx++
			}
		}
	}

	return VirtualPods{
		workloadResourceVersion: wl.ResourceVersion,
		pods:                    virtualPods,
	}
}

// CandidatePodOptions holds the options for creating a candidate pod.
type CandidatePodOptions struct {
	PodSpec           *corev1.PodSpec
	FlavorNodeLabels  map[string]string
	FlavorTolerations []corev1.Toleration
	PodSetUpdates     []kueue.PodSetUpdate
	LeafPGForPod      func(replicaIdx int) string
	// PodIndexOffset is added to the replica index when setting the pod index label, so that pods
	// of different PodSets sharing a leaf PodGroup get distinct indexes.
	PodIndexOffset int
}

// CandidateVirtualPodsForPodSet returns candidate virtual pods for the specified count
// of replicas for a PodSet, merging PodSet templates, assigned flavor details, and admission check updates.
func CandidateVirtualPodsForPodSet(wl *kueue.Workload, ps *kueue.PodSet, count int32, opts CandidatePodOptions) (VirtualPods, error) {
	if wl == nil || ps == nil {
		return VirtualPods{}, errWorkloadOrPodSetIsNil
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
			return VirtualPods{}, fmt.Errorf("%w for PodSet %s: %w", errMergeResourceFlavor, ps.Name, err)
		}
	}

	for _, u := range opts.PodSetUpdates {
		if err := info.Merge(podset.FromUpdate(&u)); err != nil {
			return VirtualPods{}, fmt.Errorf("%w for PodSet %s: %w", errMergePodSetUpdate, ps.Name, err)
		}
	}

	spec := baseSpec.DeepCopy()
	spec.NodeSelector = info.NodeSelector
	spec.Tolerations = info.Tolerations
	spec.SchedulingGroup = &corev1.PodSchedulingGroup{
		PodGroupName: new(VirtualPodGroupName(wl.Name)),
	}

	pods := make([]*corev1.Pod, 0, count)
	for replicaIdx := range int(count) {
		pod := newVirtualPod(wl, string(ps.Name), replicaIdx, info.Labels, info.Annotations, spec, corev1.PodPending)
		if ps.TopologyRequest != nil && ps.TopologyRequest.PodIndexLabel != nil && *ps.TopologyRequest.PodIndexLabel != "" {
			pod.Labels[*ps.TopologyRequest.PodIndexLabel] = strconv.Itoa(replicaIdx + opts.PodIndexOffset)
		}
		if opts.LeafPGForPod != nil {
			if leafPG := opts.LeafPGForPod(replicaIdx); leafPG != "" {
				pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{
					PodGroupName: new(leafPG),
				}
			}
		}
		pods = append(pods, pod)
	}
	return VirtualPods{
		workloadResourceVersion: wl.ResourceVersion,
		pods:                    pods,
	}, nil
}
