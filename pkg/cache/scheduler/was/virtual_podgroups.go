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
	"math"
	"slices"
	"strings"

	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// VirtualPodGroupName returns a collision-free name for a virtual pod group
// representing the workload.
func VirtualPodGroupName(wlName string) string {
	h := sha1.New()
	h.Write([]byte(wlName))
	hash := hex.EncodeToString(h.Sum(nil))[:hashLength]
	suffix := fmt.Sprintf("-%s", hash)
	prefix := fmt.Sprintf("virtual-pg-%s", wlName)
	maxPrefix := maxPodNameLength - len(suffix)
	if len(prefix) > maxPrefix {
		prefix = strings.TrimRight(prefix[:maxPrefix], ".-")
	}

	return prefix + suffix
}

// BuildVirtualPodGroup creates one single gang PodGroup for the entire Workload.
func BuildVirtualPodGroup(wl *kueue.Workload) (*schedulingv1beta1.PodGroup, error) {
	if wl == nil {
		return nil, errors.New("workload must not be nil")
	}

	var totalPods int64
	for _, ps := range wl.Spec.PodSets {
		totalPods += int64(ps.Count)
	}
	if totalPods <= 0 {
		return nil, fmt.Errorf("workload %q has non-positive total pods count %d", wl.Name, totalPods)
	}
	if totalPods > math.MaxInt32 {
		return nil, fmt.Errorf("workload %q total pods count %d exceeds max int32", wl.Name, totalPods)
	}

	return &schedulingv1beta1.PodGroup{
		Name:      VirtualPodGroupName(wl.Name),
		Namespace: wl.Namespace,
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{
					MinCount: int32(totalPods),
				},
			},
		},
	}, nil
}

const (
	maxWASHierarchyDepth = 4
)

type UnsupportableReason string

const (
	ReasonPreferredTopologyNotSupported UnsupportableReason = "PreferredTopologyNotSupported"
	ReasonHierarchyDepthExceeded        UnsupportableReason = "HierarchyDepthExceeded"
)

type ErrUnsupportableWorkload struct {
	WorkloadName string
	Reason       UnsupportableReason
	Message      string
}

func (e *ErrUnsupportableWorkload) Error() string {
	return fmt.Sprintf("workload %q is unsupportable by deep WAS: %s (%s)", e.WorkloadName, e.Reason, e.Message)
}

// WorkloadVirtualPodGroups holds the generated PodGroups, CompositePodGroups,
// and a function mapping each pod to its leaf PodGroup
type WorkloadVirtualPodGroups struct {
	PodGroups          []*schedulingv1beta1.PodGroup
	CompositePodGroups []*schedulingv1alpha3.CompositePodGroup
	LeafPGForPod       func(podSetName string, replicaIdx int) string
}

// virtualGroupName generates a unique group name, truncating wlName if needed while keeping groupID and the hash suffix
func virtualGroupName(prefix, wlName, groupID string) string {
	h := sha1.New()
	if groupID != "" {
		h.Write([]byte(wlName + "\n" + groupID))
	} else {
		h.Write([]byte(wlName))
	}
	hash := hex.EncodeToString(h.Sum(nil))[:hashLength]

	var suffix string
	if groupID != "" {
		suffix = fmt.Sprintf("-%s-%s", groupID, hash)
	} else {
		suffix = fmt.Sprintf("-%s", hash)
	}
	basePrefix := fmt.Sprintf("virtual-%s-", prefix)
	maxWlLen := maxPodNameLength - len(basePrefix) - len(suffix)
	if len(wlName) > maxWlLen {
		wlName = strings.TrimRight(wlName[:maxWlLen], ".-")
	}
	return fmt.Sprintf("%s%s%s", basePrefix, wlName, suffix)
}

// newVPG creates a leaf virtual PodGroup with gang count and optional topology constraint
func newVPG(name, namespace, topology string, count int32, parent string) *schedulingv1beta1.PodGroup {
	pg := &schedulingv1beta1.PodGroup{
		Name:      name,
		Namespace: namespace,
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: count},
			},
		},
	}
	if parent != "" {
		pg.Spec.ParentCompositePodGroupName = new(parent)
	}
	if topology != "" {
		pg.Spec.SchedulingConstraints = &schedulingv1beta1.PodGroupSchedulingConstraints{
			Topology: []schedulingv1beta1.TopologyConstraint{{Key: topology}},
		}
	}
	return pg
}

// newVCPG creates a CompositePodGroup with gang group count and optional topology constraint
func newVCPG(name, namespace, topology string, groupCount int32, parent string) *schedulingv1alpha3.CompositePodGroup {
	cpg := &schedulingv1alpha3.CompositePodGroup{
		Name:      name,
		Namespace: namespace,
		Spec: schedulingv1alpha3.CompositePodGroupSpec{
			SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: groupCount},
			},
		},
	}
	if parent != "" {
		cpg.Spec.ParentCompositePodGroupName = new(parent)
	}
	if topology != "" {
		cpg.Spec.SchedulingConstraints = &schedulingv1alpha3.CompositePodGroupSchedulingConstraints{
			Topology: []schedulingv1alpha3.TopologyConstraint{{Key: topology}},
		}
	}
	return cpg
}

// BuildVirtualPodGroups creates the virtual PodGroup hierarchy for a workload
func BuildVirtualPodGroups(wl *kueue.Workload) (*WorkloadVirtualPodGroups, error) {
	if wl == nil {
		return nil, errors.New("workload must not be nil")
	}

	if err := validateWorkloadPods(wl); err != nil {
		return nil, err
	}

	groups, psgOrder, err := groupPodSets(wl)
	if err != nil {
		return nil, err
	}

	f := newVirtualPodGroupFactory(wl)
	return f.build(groups, psgOrder)
}

// validateWorkloadPods checks that total pods > 0
func validateWorkloadPods(wl *kueue.Workload) error {
	var totalPods int64
	for i := range wl.Spec.PodSets {
		totalPods += int64(wl.Spec.PodSets[i].Count)
	}
	if totalPods <= 0 {
		return fmt.Errorf("workload %q has non-positive total pods count %d", wl.Name, totalPods)
	}
	return nil
}

// groupPodSets groups non-empty PodSets by group name and rejects preferred topology
func groupPodSets(wl *kueue.Workload) (map[string][]*kueue.PodSet, []string, error) {
	groups := make(map[string][]*kueue.PodSet)
	var psgOrder []string

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if tr := ps.TopologyRequest; tr != nil && tr.Preferred != nil {
			return nil, nil, &ErrUnsupportableWorkload{
				WorkloadName: wl.Name,
				Reason:       ReasonPreferredTopologyNotSupported,
				Message:      fmt.Sprintf("podset %q specified preferred topology, which is not supported with SchedulerLibraryDeepIntegration", ps.Name),
			}
		}
		if ps.Count == 0 {
			continue
		}

		psgName := string(utiltas.GroupKeyForPodSet(ps))
		if _, ok := groups[psgName]; !ok {
			psgOrder = append(psgOrder, psgName)
		}
		groups[psgName] = append(groups[psgName], ps)
	}
	return groups, psgOrder, nil
}

type podSetLeafMapping struct {
	sliceSize int32
	leafPGs   []string
}

// virtualPodGroupFactory builds and tracks the virtual PodGroup hierarchy for a workload
type virtualPodGroupFactory struct {
	wl          *kueue.Workload
	podToLeafPG map[string]podSetLeafMapping
	vpgMap      map[string]*schedulingv1beta1.PodGroup
	vcpgMap     map[string]*schedulingv1alpha3.CompositePodGroup
	cpgHeights  map[string]int
}

func newVirtualPodGroupFactory(wl *kueue.Workload) *virtualPodGroupFactory {
	return &virtualPodGroupFactory{
		wl:          wl,
		podToLeafPG: make(map[string]podSetLeafMapping),
		vpgMap:      make(map[string]*schedulingv1beta1.PodGroup),
		vcpgMap:     make(map[string]*schedulingv1alpha3.CompositePodGroup),
		cpgHeights:  make(map[string]int),
	}
}

// build creates subtrees for all groups, adds a root CompositePodGroup if needed, and checks WAS limits
func (f *virtualPodGroupFactory) build(groups map[string][]*kueue.PodSet, psgOrder []string) (*WorkloadVirtualPodGroups, error) {
	var topLevelGroupNames []string

	for _, psgName := range psgOrder {
		roots, err := f.buildSubtree(psgName, groups[psgName])
		if err != nil {
			return nil, err
		}
		topLevelGroupNames = append(topLevelGroupNames, roots...)
	}

	f.buildRootCPG(topLevelGroupNames)

	for _, h := range f.cpgHeights {
		if h > maxWASHierarchyDepth {
			return nil, &ErrUnsupportableWorkload{
				WorkloadName: f.wl.Name,
				Reason:       ReasonHierarchyDepthExceeded,
				Message:      fmt.Sprintf("depth %d exceeds maximum of %d", h, maxWASHierarchyDepth),
			}
		}
	}

	allPGs := slices.Collect(maps.Values(f.vpgMap))
	allCPGs := slices.Collect(maps.Values(f.vcpgMap))

	return &WorkloadVirtualPodGroups{
		PodGroups:          allPGs,
		CompositePodGroups: allCPGs,
		LeafPGForPod: func(podSetName string, replicaIdx int) string {
			mapping, ok := f.podToLeafPG[podSetName]
			if !ok || len(mapping.leafPGs) == 0 || replicaIdx < 0 {
				return ""
			}
			if mapping.sliceSize <= 0 {
				return mapping.leafPGs[0]
			}
			idx := replicaIdx / int(mapping.sliceSize)
			if idx >= len(mapping.leafPGs) {
				return ""
			}
			return mapping.leafPGs[idx]
		},
	}, nil
}

// buildSubtree builds the PodGroups for a group of PodSets, wrapping them in a CompositePodGroup if needed
func (f *virtualPodGroupFactory) buildSubtree(psgName string, podSets []*kueue.PodSet) ([]string, error) {
	var roots []string
	hasSlices := false

	// Build groups for each PodSet
	for _, ps := range podSets {
		if len(utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)) > 0 {
			hasSlices = true
		}
		psRoots, err := f.buildTreesForPodSet(ps)
		if err != nil {
			return nil, err
		}
		roots = append(roots, psRoots...)
	}

	// Extract group-level required topology constraint
	var groupTopology string
	for _, ps := range podSets {
		if ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil {
			groupTopology = *ps.TopologyRequest.Required
			break
		}
	}

	// Wrap in an enclosing CompositePodGroup if needed
	needsEnclosingCPG := (hasSlices && groupTopology != "") || (!hasSlices && len(roots) > 1)
	if !needsEnclosingCPG {
		return roots, nil
	}

	cpgName := virtualGroupName("cpg", f.wl.Name, "g."+strings.ReplaceAll(psgName, "/", "."))
	for _, child := range roots {
		f.setParent(child, cpgName)
	}
	f.vcpgMap[cpgName] = newVCPG(cpgName, f.wl.Namespace, groupTopology, int32(len(roots)), "")
	return []string{cpgName}, nil
}

// buildTreesForPodSet creates the PodGroups for one PodSet (either sliced or a single leaf)
func (f *virtualPodGroupFactory) buildTreesForPodSet(ps *kueue.PodSet) ([]string, error) {
	podSetName := string(ps.Name)
	sliceConstraints := utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)

	if len(sliceConstraints) > 0 {
		return f.buildSlicedTrees(ps, sliceConstraints)
	}

	topology := ""
	if ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil {
		topology = *ps.TopologyRequest.Required
	}
	pgName := virtualGroupName("pg", f.wl.Name, "p."+podSetName)
	f.podToLeafPG[podSetName] = podSetLeafMapping{
		leafPGs: []string{pgName},
	}
	f.vpgMap[pgName] = newVPG(pgName, f.wl.Namespace, topology, ps.Count, "")
	return []string{pgName}, nil
}

// buildSlicedTrees builds the hierarchy for sliced topology, from leaf PodGroups up to parent CompositePodGroups
func (f *virtualPodGroupFactory) buildSlicedTrees(ps *kueue.PodSet, sliceConstraints []kueue.PodsetSliceRequiredTopologyConstraint) ([]string, error) {
	// Slice sizes must be positive and each child size must evenly divide parent size
	for i, constraint := range sliceConstraints {
		if constraint.Size <= 0 {
			return nil, fmt.Errorf("slice constraint %d has non-positive size %d", i, constraint.Size)
		}
		if i > 0 && sliceConstraints[i-1].Size%constraint.Size != 0 {
			return nil, fmt.Errorf("slice constraint size %d must evenly divide parent size %d", constraint.Size, sliceConstraints[i-1].Size)
		}
	}

	podSetName := string(ps.Name)
	var currentLevelNames []string
	for lvl, constraint := range slices.Backward(sliceConstraints) {
		sliceSize := constraint.Size
		numSlices := (ps.Count + sliceSize - 1) / sliceSize

		if lvl == len(sliceConstraints)-1 {
			// Leaf level: PodGroups mapped to individual pods
			for s := range numSlices {
				pgName := virtualGroupName("pg", f.wl.Name, fmt.Sprintf("s.ps%s-l%d-s%d", podSetName, lvl, s))
				start := s * sliceSize
				end := min((s+1)*sliceSize, ps.Count)
				f.vpgMap[pgName] = newVPG(pgName, f.wl.Namespace, constraint.Topology, end-start, "")
				currentLevelNames = append(currentLevelNames, pgName)
			}
			f.podToLeafPG[podSetName] = podSetLeafMapping{
				sliceSize: sliceSize,
				leafPGs:   currentLevelNames,
			}
		} else {
			// Higher level: CompositePodGroups grouping the children from the level below
			var nextLevelNames []string
			childSize := sliceConstraints[lvl+1].Size
			childrenPerParent := sliceSize / childSize

			for s := range numSlices {
				cpgName := virtualGroupName("cpg", f.wl.Name, fmt.Sprintf("s.ps%s-l%d-s%d", podSetName, lvl, s))
				startChild := int(s * childrenPerParent)
				endChild := min(int((s+1)*childrenPerParent), len(currentLevelNames))

				for _, child := range currentLevelNames[startChild:endChild] {
					f.setParent(child, cpgName)
				}
				f.vcpgMap[cpgName] = newVCPG(cpgName, f.wl.Namespace, constraint.Topology, int32(endChild-startChild), "")
				nextLevelNames = append(nextLevelNames, cpgName)
			}
			currentLevelNames = nextLevelNames
		}
	}
	return currentLevelNames, nil
}

// buildRootCPG creates a root CompositePodGroup if there is more than one top-level group
func (f *virtualPodGroupFactory) buildRootCPG(topLevelGroupNames []string) {
	if len(topLevelGroupNames) > 1 {
		rootCPGName := virtualGroupName("cpg", f.wl.Name, "r.root")
		for _, child := range topLevelGroupNames {
			f.setParent(child, rootCPGName)
		}
		f.vcpgMap[rootCPGName] = newVCPG(rootCPGName, f.wl.Namespace, "", int32(len(topLevelGroupNames)), "")
	}
}

// setParent sets the parent CompositePodGroup name on a child group and updates parent height
func (f *virtualPodGroupFactory) setParent(child, parent string) {
	if pg, ok := f.vpgMap[child]; ok {
		pg.Spec.ParentCompositePodGroupName = new(parent)
		f.cpgHeights[parent] = max(f.cpgHeights[parent], 2)
	}
	if cpg, ok := f.vcpgMap[child]; ok {
		cpg.Spec.ParentCompositePodGroupName = new(parent)
		f.cpgHeights[parent] = max(f.cpgHeights[parent], f.cpgHeights[child]+1)
	}
}
