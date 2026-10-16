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

// WorkloadVirtualPodGroups holds all virtual PodGroup and CompositePodGroup objects
// generated for a Workload, along with mapping functions for virtual pods.
type WorkloadVirtualPodGroups struct {
	PodGroups          []*schedulingv1alpha3.PodGroup
	CompositePodGroups []*schedulingv1alpha3.CompositePodGroup
	LeafPGForPod       func(podSetName string, replicaIdx int) string
}

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

// virtualGroupName generates a unique deterministic name
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

// newVirtualPG creates a leaf PodGroup with gang count and optional topology constraint.
func newVirtualPG(name, namespace, topology string, count int32, parent *string) *schedulingv1alpha3.PodGroup {
	pg := &schedulingv1alpha3.PodGroup{
		Name:      name,
		Namespace: namespace,
		Spec: schedulingv1alpha3.PodGroupSpec{
			ParentCompositePodGroupName: parent,
			SchedulingPolicy: schedulingv1alpha3.PodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha3.GangSchedulingPolicy{MinCount: count},
			},
		},
	}
	if topology != "" {
		pg.Spec.SchedulingConstraints = &schedulingv1alpha3.PodGroupSchedulingConstraints{
			Topology: []schedulingv1alpha3.TopologyConstraint{{Key: topology}},
		}
	}
	return pg
}

// newVirtualCPG creates a CompositePodGroup with gang groupCount and optional topology constraint.
func newVirtualCPG(name, namespace, topology string, groupCount int32, parent *string) *schedulingv1alpha3.CompositePodGroup {
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

// BuildVirtualPodGroups constructs the virtual PodGroup and CompositePodGroup hierarchy
// for a candidate Workload following WAS limitations and TAS requirements.
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

	b := newVirtualPodGroupBuilder(wl, podCounts)
	return b.build(groups, groupOrder)
}

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

type virtualPodGroupBuilder struct {
	wl          *kueue.Workload
	podCounts   map[kueue.PodSetReference]int32
	podToLeafPG map[string]map[int]string
	pgMap       map[string]*schedulingv1alpha3.PodGroup
	cpgMap      map[string]*schedulingv1alpha3.CompositePodGroup
}

func newVirtualPodGroupBuilder(wl *kueue.Workload, podCounts map[kueue.PodSetReference]int32) *virtualPodGroupBuilder {
	return &virtualPodGroupBuilder{
		wl:          wl,
		podCounts:   podCounts,
		podToLeafPG: make(map[string]map[int]string),
		pgMap:       make(map[string]*schedulingv1alpha3.PodGroup),
		cpgMap:      make(map[string]*schedulingv1alpha3.CompositePodGroup),
	}
}

func (b *virtualPodGroupBuilder) build(groups map[string][]*kueue.PodSet, groupOrder []string) (*WorkloadVirtualPodGroups, error) {
	var topLevelGroupNames []string

	for _, gName := range groupOrder {
		childNames, err := b.buildGroup(gName, groups[gName])
		if err != nil {
			return nil, err
		}
		topLevelGroupNames = append(topLevelGroupNames, childNames...)
	}

	b.buildRootCPG(topLevelGroupNames)

	allPGs := slices.Collect(maps.Values(b.pgMap))
	allCPGs := slices.Collect(maps.Values(b.cpgMap))
	slices.SortFunc(allPGs, func(a, b *schedulingv1alpha3.PodGroup) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(allCPGs, func(a, b *schedulingv1alpha3.CompositePodGroup) int {
		return strings.Compare(a.Name, b.Name)
	})

	if err := validateLimits(b.wl.Name, allPGs, allCPGs); err != nil {
		return nil, err
	}

	return &WorkloadVirtualPodGroups{
		PodGroups:          allPGs,
		CompositePodGroups: allCPGs,
		LeafPGForPod: func(podSetName string, replicaIdx int) string {
			if m, ok := b.podToLeafPG[podSetName]; ok {
				return m[replicaIdx]
			}
			return ""
		},
	}, nil
}

func (b *virtualPodGroupBuilder) buildGroup(gName string, podSets []*kueue.PodSet) ([]string, error) {
	var groupChildNames []string
	hasSlices := false

	for _, ps := range podSets {
		if len(utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)) > 0 {
			hasSlices = true
		}
		childNames, err := b.buildPodSet(ps)
		if err != nil {
			return nil, err
		}
		groupChildNames = append(groupChildNames, childNames...)
	}

	var groupTopology string
	for _, ps := range podSets {
		if ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil {
			groupTopology = *ps.TopologyRequest.Required
			break
		}
	}

	needsEnclosingCPG := (hasSlices && groupTopology != "") || (!hasSlices && len(groupChildNames) > 1)
	if !needsEnclosingCPG {
		return groupChildNames, nil
	}

	if len(groupChildNames) > maxWASChildrenPerGroup {
		return nil, &ErrUnsupportableWorkload{
			WorkloadName: b.wl.Name,
			Reason:       ReasonChildLimitExceeded,
			Message:      fmt.Sprintf("group %q has %d children (max %d)", gName, len(groupChildNames), maxWASChildrenPerGroup),
		}
	}
	cpgName := virtualGroupName("cpg", b.wl.Name, "g."+gName)
	for _, child := range groupChildNames {
		setParent(b.pgMap, b.cpgMap, child, cpgName)
	}
	b.cpgMap[cpgName] = newVirtualCPG(cpgName, b.wl.Namespace, groupTopology, int32(len(groupChildNames)), nil)
	return []string{cpgName}, nil
}

func (b *virtualPodGroupBuilder) buildPodSet(ps *kueue.PodSet) ([]string, error) {
	podCount := b.podCounts[ps.Name]
	podSetName := string(ps.Name)
	b.podToLeafPG[podSetName] = make(map[int]string)
	sliceConstraints := utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)

	switch {
	case len(sliceConstraints) > 0:
		return b.buildSlicedPodSet(ps, podCount, sliceConstraints)

	case ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil:
		pgName := virtualGroupName("pg", b.wl.Name, "p."+podSetName)
		for p := range int(podCount) {
			b.podToLeafPG[podSetName][p] = pgName
		}
		b.pgMap[pgName] = newVirtualPG(pgName, b.wl.Namespace, *ps.TopologyRequest.Required, podCount, nil)
		return []string{pgName}, nil

	default:
		pgName := virtualGroupName("pg", b.wl.Name, "p."+podSetName)
		for p := range int(podCount) {
			b.podToLeafPG[podSetName][p] = pgName
		}
		b.pgMap[pgName] = newVirtualPG(pgName, b.wl.Namespace, "", podCount, nil)
		return []string{pgName}, nil
	}
}

func (b *virtualPodGroupBuilder) buildSlicedPodSet(ps *kueue.PodSet, podCount int32, sliceConstraints []kueue.PodsetSliceRequiredTopologyConstraint) ([]string, error) {
	for i, constraint := range sliceConstraints {
		if constraint.Size <= 0 {
			return nil, fmt.Errorf("slice constraint %d has non-positive size %d", i, constraint.Size)
		}
		if i > 0 && sliceConstraints[i-1].Size%constraint.Size != 0 {
			return nil, fmt.Errorf("slice constraint size %d must evenly divide parent size %d", constraint.Size, sliceConstraints[i-1].Size)
		}
	}

	leafSize := int64(sliceConstraints[len(sliceConstraints)-1].Size)
	leafGroups := (int64(podCount) + leafSize - 1) / leafSize
	if leafGroups > maxSupportedWASLeafGroups() {
		return nil, &ErrUnsupportableWorkload{
			WorkloadName: b.wl.Name,
			Reason:       ReasonChildLimitExceeded,
			Message:      fmt.Sprintf("podset %q requires %d leaf groups (max %d)", ps.Name, leafGroups, maxSupportedWASLeafGroups()),
		}
	}

	podSetName := string(ps.Name)
	var currentLevelNames []string
	for lvl, constraint := range slices.Backward(sliceConstraints) {
		sliceSize := max(1, constraint.Size)
		numSlices := (int64(podCount) + int64(sliceSize) - 1) / int64(sliceSize)

		if lvl == len(sliceConstraints)-1 {
			for s := range numSlices {
				pgName := virtualGroupName("pg", b.wl.Name, fmt.Sprintf("s.%s-l%d-s%d", podSetName, lvl, s))
				start := s * int64(sliceSize)
				end := min((s+1)*int64(sliceSize), int64(podCount))
				for p := start; p < end; p++ {
					b.podToLeafPG[podSetName][int(p)] = pgName
				}
				b.pgMap[pgName] = newVirtualPG(pgName, b.wl.Namespace, constraint.Topology, int32(end-start), nil)
				currentLevelNames = append(currentLevelNames, pgName)
			}
		} else {
			var nextLevelNames []string
			childSize := sliceConstraints[lvl+1].Size
			childrenPerParent := max(1, sliceSize/childSize)
			if childrenPerParent > maxWASChildrenPerGroup {
				return nil, &ErrUnsupportableWorkload{
					WorkloadName: b.wl.Name,
					Reason:       ReasonChildLimitExceeded,
					Message:      fmt.Sprintf("level %d requires %d children per parent (max %d)", lvl, childrenPerParent, maxWASChildrenPerGroup),
				}
			}

			for s := range numSlices {
				cpgName := virtualGroupName("cpg", b.wl.Name, fmt.Sprintf("s.%s-l%d-s%d", podSetName, lvl, s))
				startChild := int(s * int64(childrenPerParent))
				endChild := min(int((s+1)*int64(childrenPerParent)), len(currentLevelNames))

				for _, child := range currentLevelNames[startChild:endChild] {
					setParent(b.pgMap, b.cpgMap, child, cpgName)
				}
				b.cpgMap[cpgName] = newVirtualCPG(cpgName, b.wl.Namespace, constraint.Topology, int32(endChild-startChild), nil)
				nextLevelNames = append(nextLevelNames, cpgName)
			}
			currentLevelNames = nextLevelNames
		}
	}
	return currentLevelNames, nil
}

func (b *virtualPodGroupBuilder) buildRootCPG(topLevelGroupNames []string) {
	if len(topLevelGroupNames) > 1 {
		rootCPGName := virtualGroupName("cpg", b.wl.Name, "r.root")
		for _, child := range topLevelGroupNames {
			setParent(b.pgMap, b.cpgMap, child, rootCPGName)
		}
		b.cpgMap[rootCPGName] = newVirtualCPG(rootCPGName, b.wl.Namespace, "", int32(len(topLevelGroupNames)), nil)
	}
}

// setParent sets ParentCompositePodGroupName on the target child group
func setParent(pgMap map[string]*schedulingv1alpha3.PodGroup, cpgMap map[string]*schedulingv1alpha3.CompositePodGroup, child, parent string) {
	if pg, ok := pgMap[child]; ok {
		pg.Spec.ParentCompositePodGroupName = new(parent)
		return
	}
	if cpg, ok := cpgMap[child]; ok {
		cpg.Spec.ParentCompositePodGroupName = new(parent)
		return
	}
}

// validateLimits verifies the limits: max children per CPG (<=8) and hierarchy depth (<=4)
func validateLimits(wlName string, pgs []*schedulingv1alpha3.PodGroup, cpgs []*schedulingv1alpha3.CompositePodGroup) error {
	children := make(map[string]int)
	parentOf := make(map[string]string)

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

	for parent, count := range children {
		if count > maxWASChildrenPerGroup {
			return &ErrUnsupportableWorkload{
				WorkloadName: wlName,
				Reason:       ReasonChildLimitExceeded,
				Message:      fmt.Sprintf("group %q has %d children (max %d)", parent, count, maxWASChildrenPerGroup),
			}
		}
	}

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
