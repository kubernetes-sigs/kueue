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

	counts := podSetCounts(wl)

	var totalPods int64
	for i := range wl.Spec.PodSets {
		totalPods += int64(counts[wl.Spec.PodSets[i].Name])
	}
	if totalPods <= 0 {
		return nil, fmt.Errorf("workload %q has non-positive total pods count %d", wl.Name, totalPods)
	}

	// 1: Group active PodSets sharing podSetGroupName and reject preferred topology
	groups := make(map[string][]*kueue.PodSet)
	var groupOrder []string

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		if counts[ps.Name] == 0 {
			continue
		}
		if tr := ps.TopologyRequest; tr != nil && tr.Preferred != nil {
			return nil, &ErrUnsupportableWorkload{
				WorkloadName: wl.Name,
				Reason:       ReasonPreferredTopology,
				Message:      fmt.Sprintf("podset %q specifies preferred topology", ps.Name),
			}
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

	podToLeafPG := make(map[string]map[int]string)
	pgMap := make(map[string]*schedulingv1alpha3.PodGroup)
	cpgMap := make(map[string]*schedulingv1alpha3.CompositePodGroup)

	var topLevelGroupNames []string

	// 2: Build hierarchy bottom-up per PodSet group
	for _, gName := range groupOrder {
		podSets := groups[gName]
		var groupChildNames []string

		for _, ps := range podSets {
			count := counts[ps.Name]
			podSetName := string(ps.Name)
			podToLeafPG[podSetName] = make(map[int]string)
			sliceConstraints := utiltas.PodSetSliceRequiredTopologyConstraints(ps.TopologyRequest)

			switch {
			case len(sliceConstraints) > 0:
				for i, constraint := range sliceConstraints {
					if constraint.Size <= 0 {
						return nil, fmt.Errorf("slice constraint %d has non-positive size %d", i, constraint.Size)
					}
					if i > 0 && sliceConstraints[i-1].Size%constraint.Size != 0 {
						return nil, fmt.Errorf("slice constraint size %d must evenly divide parent size %d", constraint.Size, sliceConstraints[i-1].Size)
					}
				}

				leafSize := int64(sliceConstraints[len(sliceConstraints)-1].Size)
				leafGroups := (int64(count) + leafSize - 1) / leafSize
				if leafGroups > maxSupportedWASLeafGroups() {
					return nil, &ErrUnsupportableWorkload{
						WorkloadName: wl.Name,
						Reason:       ReasonChildLimitExceeded,
						Message:      fmt.Sprintf("podset %q requires %d leaf groups (max %d)", ps.Name, leafGroups, maxSupportedWASLeafGroups()),
					}
				}

				var currentLevelNames []string
				// Multi-level slices: innermost leaf PGs, intermediate CPGs
				for lvl, constraint := range slices.Backward(sliceConstraints) {
					sliceSize := max(1, constraint.Size)
					numSlices := (int64(count) + int64(sliceSize) - 1) / int64(sliceSize)

					if lvl == len(sliceConstraints)-1 {
						// Innermost level: leaf PodGroups
						for s := range numSlices {
							pgName := virtualGroupName("pg", wl.Name, fmt.Sprintf("s.%s-l%d-s%d", podSetName, lvl, s))
							start := s * int64(sliceSize)
							end := min((s+1)*int64(sliceSize), int64(count))
							for p := start; p < end; p++ {
								podToLeafPG[podSetName][int(p)] = pgName
							}
							pgMap[pgName] = newVirtualPG(pgName, wl.Namespace, constraint.Topology, int32(end-start), nil)
							currentLevelNames = append(currentLevelNames, pgName)
						}
					} else {
						// Intermediate levels: CompositePodGroups
						var nextLevelNames []string
						childSize := sliceConstraints[lvl+1].Size
						childrenPerParent := max(1, sliceSize/childSize)
						if childrenPerParent > maxWASChildrenPerGroup {
							return nil, &ErrUnsupportableWorkload{
								WorkloadName: wl.Name,
								Reason:       ReasonChildLimitExceeded,
								Message:      fmt.Sprintf("level %d requires %d children per parent (max %d)", lvl, childrenPerParent, maxWASChildrenPerGroup),
							}
						}

						for s := range numSlices {
							cpgName := virtualGroupName("cpg", wl.Name, fmt.Sprintf("s.%s-l%d-s%d", podSetName, lvl, s))
							startChild := int(s * int64(childrenPerParent))
							endChild := min(int((s+1)*int64(childrenPerParent)), len(currentLevelNames))

							for _, child := range currentLevelNames[startChild:endChild] {
								setParent(pgMap, cpgMap, child, cpgName)
							}
							cpgMap[cpgName] = newVirtualCPG(cpgName, wl.Namespace, constraint.Topology, int32(endChild-startChild), nil)
							nextLevelNames = append(nextLevelNames, cpgName)
						}
						currentLevelNames = nextLevelNames
					}
				}
				groupChildNames = append(groupChildNames, currentLevelNames...)

			case ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil:
				// Single-level required: leaf PodGroup with topology constraint
				pgName := virtualGroupName("pg", wl.Name, "p."+podSetName)
				for p := range int(count) {
					podToLeafPG[podSetName][p] = pgName
				}
				pgMap[pgName] = newVirtualPG(pgName, wl.Namespace, *ps.TopologyRequest.Required, count, nil)
				groupChildNames = append(groupChildNames, pgName)

			default:
				// Plain PodSet: leaf PodGroup without topology constraint
				pgName := virtualGroupName("pg", wl.Name, "p."+podSetName)
				for p := range int(count) {
					podToLeafPG[podSetName][p] = pgName
				}
				pgMap[pgName] = newVirtualPG(pgName, wl.Namespace, "", count, nil)
				groupChildNames = append(groupChildNames, pgName)
			}
		}

		// Enclosing CPG for PodSet group if multiple children or slices with required topology
		var groupTopology string
		for _, ps := range podSets {
			if ps.TopologyRequest != nil && ps.TopologyRequest.Required != nil {
				groupTopology = *ps.TopologyRequest.Required
				break
			}
		}

		needsEnclosingCPG := len(groupChildNames) > 1 || (len(utiltas.PodSetSliceRequiredTopologyConstraints(podSets[0].TopologyRequest)) > 0 && groupTopology != "")
		if !needsEnclosingCPG && len(groupChildNames) == 1 {
			topLevelGroupNames = append(topLevelGroupNames, groupChildNames[0])
		} else if len(groupChildNames) > 0 {
			if len(groupChildNames) > maxWASChildrenPerGroup {
				return nil, &ErrUnsupportableWorkload{
					WorkloadName: wl.Name,
					Reason:       ReasonChildLimitExceeded,
					Message:      fmt.Sprintf("group %q has %d children (max %d)", gName, len(groupChildNames), maxWASChildrenPerGroup),
				}
			}
			cpgName := virtualGroupName("cpg", wl.Name, "g."+gName)
			for _, child := range groupChildNames {
				setParent(pgMap, cpgMap, child, cpgName)
			}
			cpgMap[cpgName] = newVirtualCPG(cpgName, wl.Namespace, groupTopology, int32(len(groupChildNames)), nil)
			topLevelGroupNames = append(topLevelGroupNames, cpgName)
		}
	}

	// 3: Root CompositePodGroup when multiple top-level groups exist
	if len(topLevelGroupNames) > 1 {
		rootCPGName := virtualGroupName("cpg", wl.Name, "r.root")
		for _, child := range topLevelGroupNames {
			setParent(pgMap, cpgMap, child, rootCPGName)
		}
		cpgMap[rootCPGName] = newVirtualCPG(rootCPGName, wl.Namespace, "", int32(len(topLevelGroupNames)), nil)
	}

	allPGs := slices.Collect(maps.Values(pgMap))
	allCPGs := slices.Collect(maps.Values(cpgMap))
	slices.SortFunc(allPGs, func(a, b *schedulingv1alpha3.PodGroup) int {
		return strings.Compare(a.Name, b.Name)
	})
	slices.SortFunc(allCPGs, func(a, b *schedulingv1alpha3.CompositePodGroup) int {
		return strings.Compare(a.Name, b.Name)
	})

	// 4: Enforce WAS limits
	if err := validateLimits(wl.Name, allPGs, allCPGs); err != nil {
		return nil, err
	}

	return &WorkloadVirtualPodGroups{
		PodGroups:          allPGs,
		CompositePodGroups: allCPGs,
		LeafPGForPod: func(podSetName string, replicaIdx int) string {
			if m, ok := podToLeafPG[podSetName]; ok {
				return m[replicaIdx]
			}
			return ""
		},
	}, nil
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
