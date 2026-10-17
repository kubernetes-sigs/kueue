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
	"strings"

	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

const (
	maxWASChildrenPerGroup = 8
	maxWASHierarchyDepth   = 4
)

func maxSupportedWASLeafGroups() int64 {
	result := int64(1)
	for depth := 1; depth < maxWASHierarchyDepth; depth++ {
		result *= maxWASChildrenPerGroup
	}
	return result
}

type UnsupportableReason string

const (
	ReasonPreferredTopology      UnsupportableReason = "PreferredTopologyNotSupported"
	ReasonHierarchyDepthExceeded UnsupportableReason = "HierarchyDepthExceeded"
	ReasonChildLimitExceeded     UnsupportableReason = "ChildLimitExceeded"
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

// VirtualPodGroupName returns a deterministic name for the workload's virtual PodGroup
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

// virtualGroupName generates a unique group name, truncating if needed while keeping the hash suffix
func virtualGroupName(prefix, wlName, groupID string) string {
	h := sha1.New()
	h.Write([]byte(wlName + "\n" + groupID))
	hash := hex.EncodeToString(h.Sum(nil))[:hashLength]
	name := fmt.Sprintf("virtual-%s-%s-%s", prefix, wlName, groupID)
	suffix := "-" + hash
	if len(name) > maxPodNameLength-len(suffix) {
		name = strings.TrimRight(name[:maxPodNameLength-len(suffix)], ".-")
	}
	return name + suffix
}

// newVPG creates a leaf PodGroup with gang count and optional topology constraint
func newVPG(name, namespace, topology string, count int32, parent *string) *schedulingv1beta1.PodGroup {
	pg := &schedulingv1beta1.PodGroup{
		Name:      name,
		Namespace: namespace,
		Spec: schedulingv1beta1.PodGroupSpec{
			ParentCompositePodGroupName: parent,
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: count},
			},
		},
	}
	if topology != "" {
		pg.Spec.SchedulingConstraints = &schedulingv1beta1.PodGroupSchedulingConstraints{
			Topology: []schedulingv1beta1.TopologyConstraint{{Key: topology}},
		}
	}
	return pg
}

// newVCPG creates a CompositePodGroup with gang group count and optional topology constraint
func newVCPG(name, namespace, topology string, groupCount int32, parent *string) *schedulingv1alpha3.CompositePodGroup {
	cpg := &schedulingv1alpha3.CompositePodGroup{
		Name:      name,
		Namespace: namespace,
		Spec: schedulingv1alpha3.CompositePodGroupSpec{
			ParentCompositePodGroupName: parent,
			SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: groupCount},
			},
		},
	}
	if topology != "" {
		cpg.Spec.SchedulingConstraints = &schedulingv1alpha3.CompositePodGroupSchedulingConstraints{
			Topology: []schedulingv1alpha3.TopologyConstraint{{Key: topology}},
		}
	}
	return cpg
}

// podSetCounts returns the pod count for each PodSet, using admitted counts if available
func podSetCounts(wl *kueue.Workload) map[kueue.PodSetReference]int32 {
	counts := make(map[kueue.PodSetReference]int32, len(wl.Spec.PodSets))
	if wl.Status.Admission != nil {
		for _, psa := range wl.Status.Admission.PodSetAssignments {
			if psa.Count != nil {
				counts[psa.Name] = *psa.Count
			}
		}
	}
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if _, ok := counts[ps.Name]; !ok {
			counts[ps.Name] = ps.Count
		}
	}
	return counts
}

// BuildVirtualPodGroups creates the virtual PodGroup hierarchy for a workload
func BuildVirtualPodGroups(wl *kueue.Workload) (*WorkloadVirtualPodGroups, error) {
	if wl == nil {
		return nil, errors.New("workload must not be nil")
	}

	podCounts, err := validateAndCountPods(wl)
	if err != nil {
		return nil, err
	}

	groups, groupOrder, err := groupPodSets(wl, podCounts)
	if err != nil {
		return nil, err
	}

	return buildVirtualPodGroups(wl, podCounts, groups, groupOrder)
}

// buildVirtualPodGroups uses virtualPodGroupFactory to create the hierarchy for the grouped PodSets
func buildVirtualPodGroups(wl *kueue.Workload, podCounts map[kueue.PodSetReference]int32, groups map[string][]*kueue.PodSet, groupOrder []string) (*WorkloadVirtualPodGroups, error) {
	f := newVirtualPodGroupFactory(wl, podCounts)
	return f.build(groups, groupOrder)
}

// validateAndCountPods checks that total pods > 0 and returns the count for each PodSet
func validateAndCountPods(wl *kueue.Workload) (map[kueue.PodSetReference]int32, error) {
	podCounts := podSetCounts(wl)

	var totalPods int64
	for i := range wl.Spec.PodSets {
		totalPods += int64(podCounts[wl.Spec.PodSets[i].Name])
	}
	if totalPods <= 0 {
		return nil, fmt.Errorf("workload %q has non-positive total pods count %d", wl.Name, totalPods)
	}
	return podCounts, nil
}

// groupPodSets groups non-empty PodSets by group name and rejects preferred topology
func groupPodSets(wl *kueue.Workload, podCounts map[kueue.PodSetReference]int32) (map[string][]*kueue.PodSet, []string, error) {
	groups := make(map[string][]*kueue.PodSet)
	var groupOrder []string

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if tr := ps.TopologyRequest; tr != nil && tr.Preferred != nil {
			return nil, nil, &ErrUnsupportableWorkload{
				WorkloadName: wl.Name,
				Reason:       ReasonPreferredTopology,
				Message:      fmt.Sprintf("podset %q specifies preferred topology", ps.Name),
			}
		}
		if podCounts[ps.Name] == 0 {
			continue
		}

		gName := string(ps.Name)
		if ps.TopologyRequest != nil && ps.TopologyRequest.PodSetGroupName != nil {
			gName = *ps.TopologyRequest.PodSetGroupName
		}
		if _, ok := groups[gName]; !ok {
			groupOrder = append(groupOrder, gName)
		}
		groups[gName] = append(groups[gName], ps)
	}
	return groups, groupOrder, nil
}

// virtualPodGroupFactory builds and tracks the virtual PodGroup hierarchy for a workload
type virtualPodGroupFactory struct {
	wl          *kueue.Workload
	podCounts   map[kueue.PodSetReference]int32
	podToLeafPG map[string]map[int]string
	vpgMap      map[string]*schedulingv1beta1.PodGroup
	vcpgMap     map[string]*schedulingv1alpha3.CompositePodGroup
}

func newVirtualPodGroupFactory(wl *kueue.Workload, podCounts map[kueue.PodSetReference]int32) *virtualPodGroupFactory {
	return &virtualPodGroupFactory{
		wl:          wl,
		podCounts:   podCounts,
		podToLeafPG: make(map[string]map[int]string),
		vpgMap:      make(map[string]*schedulingv1beta1.PodGroup),
		vcpgMap:     make(map[string]*schedulingv1alpha3.CompositePodGroup),
	}
}

// build creates subtrees for all groups, adds a root CompositePodGroup if needed, and checks WAS limits
func (f *virtualPodGroupFactory) build(groups map[string][]*kueue.PodSet, groupOrder []string) (*WorkloadVirtualPodGroups, error) {
	var topLevelGroupNames []string

	for _, gName := range groupOrder {
		roots, err := f.buildSubtree(gName, groups[gName])
		if err != nil {
			return nil, err
		}
		topLevelGroupNames = append(topLevelGroupNames, roots...)
	}

	f.buildRootCPG(topLevelGroupNames)

	allPGs := slices.Collect(maps.Values(f.vpgMap))
	allCPGs := slices.Collect(maps.Values(f.vcpgMap))
	slices.SortFunc(allPGs, func(a, b *schedulingv1beta1.PodGroup) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(allCPGs, func(a, b *schedulingv1alpha3.CompositePodGroup) int {
		return strings.Compare(a.Name, b.Name)
	})

	if err := validateLimits(f.wl.Name, allPGs, allCPGs); err != nil {
		return nil, err
	}

	return &WorkloadVirtualPodGroups{
		PodGroups:          allPGs,
		CompositePodGroups: allCPGs,
		LeafPGForPod: func(podSetName string, replicaIdx int) string {
			if m, ok := f.podToLeafPG[podSetName]; ok {
				return m[replicaIdx]
			}
			return ""
		},
	}, nil
}

// buildSubtree builds the PodGroups for a group of PodSets, wrapping them in a CompositePodGroup if needed
func (f *virtualPodGroupFactory) buildSubtree(gName string, podSets []*kueue.PodSet) ([]string, error) {
	var roots []string
	hasSlices := false

	// Build groups for each PodSet
	for _, ps := range podSets {
		if len(utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)) > 0 {
			hasSlices = true
		}
		psRoots, err := f.buildPodSet(ps)
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

	if len(roots) > maxWASChildrenPerGroup {
		return nil, &ErrUnsupportableWorkload{
			WorkloadName: f.wl.Name,
			Reason:       ReasonChildLimitExceeded,
			Message:      fmt.Sprintf("group %q has %d children (max %d)", gName, len(roots), maxWASChildrenPerGroup),
		}
	}
	cpgName := virtualGroupName("cpg", f.wl.Name, "g."+gName)
	for _, child := range roots {
		setParent(f.vpgMap, f.vcpgMap, child, cpgName)
	}
	f.vcpgMap[cpgName] = newVCPG(cpgName, f.wl.Namespace, groupTopology, int32(len(roots)), nil)
	return []string{cpgName}, nil
}

// buildPodSet creates the PodGroups for one PodSet (either sliced or a single leaf)
func (f *virtualPodGroupFactory) buildPodSet(ps *kueue.PodSet) ([]string, error) {
	podCount := f.podCounts[ps.Name]
	podSetName := string(ps.Name)
	f.podToLeafPG[podSetName] = make(map[int]string)
	sliceConstraints := utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)

	switch {
	case len(sliceConstraints) > 0:
		return f.buildSlicedPodSet(ps, podCount, sliceConstraints)

	case ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil:
		pgName := virtualGroupName("pg", f.wl.Name, "p."+podSetName)
		for p := range int(podCount) {
			f.podToLeafPG[podSetName][p] = pgName
		}
		f.vpgMap[pgName] = newVPG(pgName, f.wl.Namespace, *ps.TopologyRequest.Required, podCount, nil)
		return []string{pgName}, nil

	default:
		pgName := virtualGroupName("pg", f.wl.Name, "p."+podSetName)
		for p := range int(podCount) {
			f.podToLeafPG[podSetName][p] = pgName
		}
		f.vpgMap[pgName] = newVPG(pgName, f.wl.Namespace, "", podCount, nil)
		return []string{pgName}, nil
	}
}

// buildSlicedPodSet builds the hierarchy for sliced topology, from leaf PodGroups up to parent CompositePodGroups
func (f *virtualPodGroupFactory) buildSlicedPodSet(ps *kueue.PodSet, podCount int32, sliceConstraints []kueue.PodsetSliceRequiredTopologyConstraint) ([]string, error) {
	// Slice sizes must be positive and each child size must evenly divide parent size
	for i, constraint := range sliceConstraints {
		if constraint.Size <= 0 {
			return nil, fmt.Errorf("slice constraint %d has non-positive size %d", i, constraint.Size)
		}
		if i > 0 && sliceConstraints[i-1].Size%constraint.Size != 0 {
			return nil, fmt.Errorf("slice constraint size %d must evenly divide parent size %d", constraint.Size, sliceConstraints[i-1].Size)
		}
	}

	// Fail early if total leaf groups exceed maximum WAS capacity
	leafSize := int64(sliceConstraints[len(sliceConstraints)-1].Size)
	leafGroups := (int64(podCount) + leafSize - 1) / leafSize
	if leafGroups > maxSupportedWASLeafGroups() {
		return nil, &ErrUnsupportableWorkload{
			WorkloadName: f.wl.Name,
			Reason:       ReasonChildLimitExceeded,
			Message:      fmt.Sprintf("podset %q requires %d leaf groups (max %d)", ps.Name, leafGroups, maxSupportedWASLeafGroups()),
		}
	}

	// Build levels bottom-up from leaves to roots
	podSetName := string(ps.Name)
	var currentLevelNames []string
	for lvl, constraint := range slices.Backward(sliceConstraints) {
		sliceSize := max(1, constraint.Size)
		numSlices := (int64(podCount) + int64(sliceSize) - 1) / int64(sliceSize)

		if lvl == len(sliceConstraints)-1 {
			// Leaf level: PodGroups mapped to individual pods
			for s := range numSlices {
				pgName := virtualGroupName("pg", f.wl.Name, fmt.Sprintf("s.%s-l%d-s%d", podSetName, lvl, s))
				start := s * int64(sliceSize)
				end := min((s+1)*int64(sliceSize), int64(podCount))
				for p := start; p < end; p++ {
					f.podToLeafPG[podSetName][int(p)] = pgName
				}
				f.vpgMap[pgName] = newVPG(pgName, f.wl.Namespace, constraint.Topology, int32(end-start), nil)
				currentLevelNames = append(currentLevelNames, pgName)
			}
		} else {
			// Higher level: CompositePodGroups grouping the children from the level below
			var nextLevelNames []string
			childSize := sliceConstraints[lvl+1].Size
			childrenPerParent := max(1, sliceSize/childSize)
			if childrenPerParent > maxWASChildrenPerGroup {
				return nil, &ErrUnsupportableWorkload{
					WorkloadName: f.wl.Name,
					Reason:       ReasonChildLimitExceeded,
					Message:      fmt.Sprintf("level %d requires %d children per parent (max %d)", lvl, childrenPerParent, maxWASChildrenPerGroup),
				}
			}

			for s := range numSlices {
				cpgName := virtualGroupName("cpg", f.wl.Name, fmt.Sprintf("s.%s-l%d-s%d", podSetName, lvl, s))
				startChild := int(s * int64(childrenPerParent))
				endChild := min(int((s+1)*int64(childrenPerParent)), len(currentLevelNames))

				for _, child := range currentLevelNames[startChild:endChild] {
					setParent(f.vpgMap, f.vcpgMap, child, cpgName)
				}
				f.vcpgMap[cpgName] = newVCPG(cpgName, f.wl.Namespace, constraint.Topology, int32(endChild-startChild), nil)
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
			setParent(f.vpgMap, f.vcpgMap, child, rootCPGName)
		}
		f.vcpgMap[rootCPGName] = newVCPG(rootCPGName, f.wl.Namespace, "", int32(len(topLevelGroupNames)), nil)
	}
}

// setParent sets the parent CompositePodGroup name on a child group
func setParent(vpgMap map[string]*schedulingv1beta1.PodGroup, vcpgMap map[string]*schedulingv1alpha3.CompositePodGroup, child, parent string) {
	if pg, ok := vpgMap[child]; ok {
		pg.Spec.ParentCompositePodGroupName = new(parent)
		return
	}
	if cpg, ok := vcpgMap[child]; ok {
		cpg.Spec.ParentCompositePodGroupName = new(parent)
		return
	}
}

// validateLimits checks WAS limits: at most 8 children per group and max depth 4
func validateLimits(wlName string, pgs []*schedulingv1beta1.PodGroup, cpgs []*schedulingv1alpha3.CompositePodGroup) error {
	children := make(map[string]int)
	parentOf := make(map[string]string)

	// Count children per CompositePodGroup and record parent links
	for _, pg := range pgs {
		if pg.Spec.ParentCompositePodGroupName != nil {
			children[*pg.Spec.ParentCompositePodGroupName]++
		}
	}
	for _, cpg := range cpgs {
		if cpg.Spec.ParentCompositePodGroupName != nil {
			children[*cpg.Spec.ParentCompositePodGroupName]++
			parentOf[cpg.Name] = *cpg.Spec.ParentCompositePodGroupName
		}
	}

	// Check child limit (<= 8) on every CompositePodGroup
	for parent, count := range children {
		if count > maxWASChildrenPerGroup {
			return &ErrUnsupportableWorkload{
				WorkloadName: wlName,
				Reason:       ReasonChildLimitExceeded,
				Message:      fmt.Sprintf("group %q has %d children (max %d)", parent, count, maxWASChildrenPerGroup),
			}
		}
	}

	// Check hierarchy depth (<= 4) from every leaf to the root
	for _, pg := range pgs {
		depth := 1
		curr := pg.Spec.ParentCompositePodGroupName
		for curr != nil {
			depth++
			if depth > maxWASHierarchyDepth {
				return &ErrUnsupportableWorkload{
					WorkloadName: wlName,
					Reason:       ReasonHierarchyDepthExceeded,
					Message:      fmt.Sprintf("depth %d exceeds maximum of %d", depth, maxWASHierarchyDepth),
				}
			}
			if p, ok := parentOf[*curr]; ok {
				curr = new(p)
			} else {
				curr = nil
			}
		}
	}
	return nil
}
