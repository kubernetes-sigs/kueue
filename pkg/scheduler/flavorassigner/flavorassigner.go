/*
Copyright 2022 The Kubernetes Authors.

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

package flavorassigner

import (
	"fmt"
	"sort"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Assignment struct {
	PodSets     []PodSetAssignment
	TotalBorrow cache.ResourceQuantities

	// usedResources is the accumulated usage of resources as podSets get
	// flavors assigned.
	usage cache.ResourceQuantities
}

func (a *Assignment) Borrows() bool {
	return len(a.TotalBorrow) > 0
}

func (a *Assignment) ToAPI() []kueue.PodSetFlavors {
	psFlavors := make([]kueue.PodSetFlavors, len(a.PodSets))
	for i := range psFlavors {
		psFlavors[i] = a.PodSets[i].toAPI()
	}
	return psFlavors
}

type Status struct {
	podSet  string
	reasons []string
	err     error
}

func (s *Status) IsSuccess() bool {
	return s == nil
}

func (s *Status) IsError() bool {
	return s != nil && s.err != nil
}

func (s *Status) append(r string) {
	s.reasons = append(s.reasons, r)
}

func (s *Status) Message() string {
	if s.IsSuccess() {
		return ""
	}
	if s.err != nil {
		return fmt.Sprintf("Couldn't assign flavors for podSet %s: %v", s.podSet, s.err)
	}
	sort.Strings(s.reasons)
	msg := strings.Join(s.reasons, "; ")
	return fmt.Sprintf("Workload's %q podSet didn't fit: %s", s.podSet, msg)
}

type PodSetAssignment struct {
	Name    string
	Flavors ResourceAssignment
}

type ResourceAssignment map[corev1.ResourceName]*FlavorAssignment

func (psa *PodSetAssignment) toAPI() kueue.PodSetFlavors {
	flavors := make(map[corev1.ResourceName]string, len(psa.Flavors))
	for res, flvAssignment := range psa.Flavors {
		flavors[res] = flvAssignment.Name
	}
	return kueue.PodSetFlavors{
		Name:    psa.Name,
		Flavors: flavors,
	}
}

type FlavorAssignmentMode int

const (
	// CohortFit means that there are enough unused resources in the cohort to
	// assign this flavor.
	CohortFit FlavorAssignmentMode = iota
)

type FlavorAssignment struct {
	Name   string
	Mode   FlavorAssignmentMode
	borrow int64
}

func AssignFlavors(log logr.Logger, wl *workload.Info, resourceFlavors map[string]*kueue.ResourceFlavor, cq *cache.ClusterQueue) (*Assignment, *Status) {
	assignment := Assignment{
		TotalBorrow: make(cache.ResourceQuantities),
		PodSets:     make([]PodSetAssignment, 0, len(wl.TotalRequests)),
		usage:       make(cache.ResourceQuantities),
	}
	for i, podSet := range wl.TotalRequests {
		psAssignment := PodSetAssignment{
			Name:    podSet.Name,
			Flavors: make(ResourceAssignment, len(podSet.Requests)),
		}
		for resName := range podSet.Requests {
			if _, found := psAssignment.Flavors[resName]; found {
				// This resource got assigned the same flavor as a codependent resource.
				// No need to compute again.
				continue
			}
			if _, ok := cq.RequestableResources[resName]; !ok {
				return nil, &Status{
					podSet:  podSet.Name,
					reasons: []string{fmt.Sprintf("resource %s unavailable in ClusterQueue", resName)},
				}
			}
			codepResources := cq.RequestableResources[resName].CodependentResources
			if codepResources.Len() == 0 {
				codepResources = sets.NewString(string(resName))
			}
			codepReq := filterRequestedResources(podSet.Requests, codepResources)
			flavors, status := assignment.findFlavorForCodepResources(log, codepReq, resourceFlavors, cq, &wl.Obj.Spec.PodSets[i].Spec)
			if !status.IsSuccess() {
				status.podSet = podSet.Name
				return nil, status
			}
			psAssignment.append(flavors)
		}
		assignment.append(podSet.Requests, &psAssignment)
	}
	if len(assignment.TotalBorrow) == 0 {
		assignment.TotalBorrow = nil
	}
	return &assignment, nil
}

func (psa *PodSetAssignment) append(flavors ResourceAssignment) {
	for resource, assignment := range flavors {
		psa.Flavors[resource] = assignment
	}
}

func (a *Assignment) append(requests workload.Requests, psAssignment *PodSetAssignment) {
	a.PodSets = append(a.PodSets, *psAssignment)
	for resource, flvAssignment := range psAssignment.Flavors {
		if flvAssignment.borrow > 0 {
			if a.TotalBorrow[resource] == nil {
				a.TotalBorrow[resource] = make(map[string]int64)
			}
			// Don't accumulate borrowing. The returned `borrow` already considers
			// usage from previous pod sets.
			a.TotalBorrow[resource][flvAssignment.Name] = flvAssignment.borrow
		}
		if a.usage[resource] == nil {
			a.usage[resource] = make(map[string]int64)
		}
		a.usage[resource][flvAssignment.Name] += requests[resource]
	}
}

// findFlavorForCodepResources finds the flavor which can satisfy the resource
// request, along with the information about resources that need to be borrowed.
// If no flavor can be found, it returns a status with reasons or failure.
func (a *Assignment) findFlavorForCodepResources(
	log logr.Logger,
	requests workload.Requests,
	resourceFlavors map[string]*kueue.ResourceFlavor,
	cq *cache.ClusterQueue,
	spec *corev1.PodSpec) (ResourceAssignment, *Status) {
	var status Status

	// Keep any resource name as an anchor to gather flavors for.
	var rName corev1.ResourceName
	for rName = range requests {
		break
	}
	// We will only check against the flavors' labels for the resource.
	// Since all the resources share the same flavors, they use the same selector.
	selector := flavorSelector(spec, cq.LabelKeys[rName])
	for i, flvLimit := range cq.RequestableResources[rName].Flavors {
		flavor, exist := resourceFlavors[flvLimit.Name]
		if !exist {
			log.Error(nil, "Flavor not found", "Flavor", flvLimit.Name)
			status.append(fmt.Sprintf("flavor %s not found", flvLimit.Name))
			continue
		}
		taint, untolerated := corev1helpers.FindMatchingUntoleratedTaint(flavor.Taints, spec.Tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		})
		if untolerated {
			status.append(fmt.Sprintf("untolerated taint %s in flavor %s", taint, flvLimit.Name))
			continue
		}
		if match, err := selector.Match(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: flavor.NodeSelector}}); !match || err != nil {
			if err != nil {
				status.err = err
				return nil, &status
			}
			status.append(fmt.Sprintf("flavor %s doesn't match with node affinity", flvLimit.Name))
			continue
		}

		assignments := make(ResourceAssignment, len(requests))
		for name, val := range requests {
			codepFlvLimit := cq.RequestableResources[name].Flavors[i]
			// Check considering the flavor usage by previous pod sets.
			borrow, s := fitsFlavorLimits(name, val+a.usage[name][flavor.Name], cq, &codepFlvLimit)
			if s.IsError() {
				return nil, s
			}
			if !s.IsSuccess() {
				status.reasons = append(status.reasons, s.reasons...)
				break
			}
			assignments[name] = &FlavorAssignment{
				Name:   flavor.Name,
				Mode:   CohortFit,
				borrow: borrow,
			}
		}
		if len(assignments) == len(requests) {
			return assignments, nil
		}
	}
	return nil, &status
}

func flavorSelector(spec *corev1.PodSpec, allowedKeys sets.String) nodeaffinity.RequiredNodeAffinity {
	// This function generally replicates the implementation of kube-scheduler's NodeAffintiy
	// Filter plugin as of v1.24.
	var specCopy corev1.PodSpec

	// Remove affinity constraints with irrelevant keys.
	if len(spec.NodeSelector) != 0 {
		specCopy.NodeSelector = map[string]string{}
		for k, v := range spec.NodeSelector {
			if allowedKeys.Has(k) {
				specCopy.NodeSelector[k] = v
			}
		}
	}

	affinity := spec.Affinity
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		var termsCopy []corev1.NodeSelectorTerm
		for _, t := range affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			var expCopy []corev1.NodeSelectorRequirement
			for _, e := range t.MatchExpressions {
				if allowedKeys.Has(e.Key) {
					expCopy = append(expCopy, e)
				}
			}
			// If a term becomes empty, it means node affinity matches any flavor since those terms are ORed,
			// and so matching gets reduced to spec.NodeSelector
			if len(expCopy) == 0 {
				termsCopy = nil
				break
			}
			termsCopy = append(termsCopy, corev1.NodeSelectorTerm{MatchExpressions: expCopy})
		}
		if len(termsCopy) != 0 {
			specCopy.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: termsCopy,
					},
				},
			}
		}
	}
	return nodeaffinity.GetRequiredNodeAffinity(&corev1.Pod{Spec: specCopy})
}

// fitsFlavorLimits returns whether a requested resource fits in a specific flavor's quota limits.
// If it fits, also returns any borrowing required.
func fitsFlavorLimits(rName corev1.ResourceName, val int64, cq *cache.ClusterQueue, flavor *cache.FlavorLimits) (int64, *Status) {
	var status Status
	used := cq.UsedResources[rName][flavor.Name]
	if flavor.Max != nil && used+val > *flavor.Max {
		status.append(fmt.Sprintf("borrowing limit for %s flavor %s exceeded", rName, flavor.Name))
		return 0, &status
	}
	cohortUsed := used
	cohortTotal := flavor.Min
	if cq.Cohort != nil {
		cohortUsed = cq.Cohort.UsedResources[rName][flavor.Name]
		cohortTotal = cq.Cohort.RequestableResources[rName][flavor.Name]
	}
	borrow := used + val - flavor.Min
	if borrow < 0 {
		borrow = 0
	}

	lack := cohortUsed + val - cohortTotal
	if lack > 0 {
		lackQuantity := workload.ResourceQuantity(rName, lack)
		if cq.Cohort == nil {
			status.append(fmt.Sprintf("insufficient quota for %s flavor %s, %s more needed", rName, flavor.Name, &lackQuantity))
		} else {
			status.append(fmt.Sprintf("insufficient quota for %s flavor %s, %s more needed after borrowing", rName, flavor.Name, &lackQuantity))
		}
		// TODO(PostMVP): preemption could help if borrow == 0
		return 0, &status
	}
	return borrow, nil
}

func filterRequestedResources(req workload.Requests, allowList sets.String) workload.Requests {
	filtered := make(workload.Requests)
	for n, v := range req {
		if allowList.Has(string(n)) {
			filtered[n] = v
		}
	}
	return filtered
}
