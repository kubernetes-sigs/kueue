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

package flavorassigner

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// WorkloadsTopologyRequests - returns the TopologyRequests of the workload
func (a *Assignment) WorkloadsTopologyRequests(wl *workload.Info, cq *schdcache.ClusterQueueSnapshot) schdcache.WorkloadTASRequests {
	tasRequests := make(schdcache.WorkloadTASRequests)
	for i, podSet := range wl.Obj.Spec.PodSets {
		if isTASRequested(&podSet, cq) {
			psAssignment := a.podSetAssignmentByName(podSet.Name)
			if psAssignment.Status.IsError() {
				// There is no resource quota assignment for the PodSet - no need to check TAS.
				continue
			}
			if psAssignment.TopologyAssignment != nil && !psAssignment.HasUnhealthyNode(wl) {
				// skip if already computed and doesn't need recomputing
				// if it already has an assignment but needs recomputing due to a failed node
				// we add it to the list of TASRequests
				continue
			}
			isTASImplied := isTASImplied(&podSet, cq)

			// Only unconstrained topology is supported with elastic workload slices.
			var previousAssignment *kueue.TopologyAssignment
			if features.Enabled(features.ElasticJobsViaWorkloadSlicesWithTAS) {
				if podSet.TopologyRequest != nil && podSet.TopologyRequest.Required != nil {
					psAssignment.error(errors.New("required topology is not supported with ElasticJobsViaWorkloadSlices"))
					continue
				}
				if podSet.TopologyRequest != nil && podSet.TopologyRequest.Preferred != nil {
					psAssignment.error(errors.New("preferred topology is not supported with ElasticJobsViaWorkloadSlices"))
					continue
				}
				previousAssignment = getPreviousTopologyAssignment(a.replaceWorkloadSlice, podSet.Name)
			}

			psTASRequest, err := podSetTopologyRequest(psAssignment, wl, cq, isTASImplied, i, previousAssignment)
			if err != nil {
				psAssignment.error(err)
			} else if psTASRequest != nil {
				tasRequests[psTASRequest.Flavor] = append(tasRequests[psTASRequest.Flavor], *psTASRequest)
			}
		}
	}
	return tasRequests
}

func (psa *PodSetAssignment) HasUnhealthyNode(wl *workload.Info) bool {
	return workload.HasUnhealthyNodes(wl.Obj) && slices.ContainsFunc(psa.TopologyAssignment.Domains, func(domain tas.TopologyDomainAssignment) bool {
		return workload.HasUnhealthyNode(wl.Obj, domain.Values[len(domain.Values)-1])
	})
}

func podSetTopologyRequest(psAssignment *PodSetAssignment,
	wl *workload.Info,
	cq *schdcache.ClusterQueueSnapshot,
	isTASImplied bool,
	podSetIndex int,
	previousAssignment *kueue.TopologyAssignment) (*schdcache.TASPodSetRequests, error) {
	if len(cq.TASFlavors) == 0 {
		return nil, errors.New("workload requires Topology, but there is no TAS cache information")
	}
	podCount := psAssignment.Count
	tasFlvr, err := onlyFlavor(psAssignment.Flavors)
	if err != nil {
		return nil, err
	}
	if cq.HasMultiKueueAdmissionCheck() || (!workload.HasQuotaReservation(wl.Obj) && cq.HasProvRequestAdmissionCheck(*tasFlvr)) {
		// Delay TAS when MultiKueue is used (topology always assigned on worker cluster).
		// For ProvisioningRequest, delay TAS on first scheduling pass only (topology assigned after provisioning).
		psAssignment.DelayedTopologyRequest = ptr.To(kueue.DelayedTopologyRequestStatePending)
		return nil, nil
	}
	if cq.TASFlavors[*tasFlvr] == nil {
		return nil, errors.New("workload requires Topology, but there is no TAS cache information for the assigned flavor")
	}
	podSet := &wl.Obj.Spec.PodSets[podSetIndex]
	// Use PodSpec directly for TAS placement, not quota-filtered admission values.
	singlePodRequests := resources.NewRequestsFromPodSpec(&podSet.Template.Spec)
	var podSetUpdates []*kueue.PodSetUpdate
	for _, ac := range wl.Obj.Status.AdmissionChecks {
		if ac.State == kueue.CheckStateReady {
			for _, psUpdate := range ac.PodSetUpdates {
				if psUpdate.Name == podSet.Name {
					podSetUpdates = append(podSetUpdates, &psUpdate)
				}
			}
		}
	}
	var podSetGroupName *string
	if podSet.TopologyRequest != nil {
		podSetGroupName = podSet.TopologyRequest.PodSetGroupName
	}

	return &schdcache.TASPodSetRequests{
		Count:              podCount,
		SinglePodRequests:  singlePodRequests,
		PodSet:             podSet,
		PodSetUpdates:      podSetUpdates,
		Flavor:             *tasFlvr,
		Implied:            isTASImplied,
		PodSetGroupName:    podSetGroupName,
		PreviousAssignment: previousAssignment,
	}, nil
}

func onlyFlavor(ra ResourceAssignment) (*kueue.ResourceFlavorReference, error) {
	if len(ra) == 0 {
		return nil, errors.New("no flavor assigned")
	}

	flavors := sets.New[kueue.ResourceFlavorReference]()
	for _, v := range ra {
		flavors.Insert(v.Name)
	}

	if flavors.Len() == 1 {
		return ptr.To(sets.List(flavors)[0]), nil
	}

	list := sets.List(flavors)
	names := make([]string, len(list))
	for i, n := range list {
		names[i] = string(n)
	}
	return nil, fmt.Errorf("more than one flavor assigned: %s", strings.Join(names, ", "))
}

func checkPodSetAndFlavorMatchForTAS(cq *schdcache.ClusterQueueSnapshot, ps *kueue.PodSet, flavor *kueue.ResourceFlavor) *string {
	if isTASRequested(ps, cq) {
		if isTASImplied(ps, cq) {
			// If this is a TAS-only CQ, then we don't need to check the flavor because
			// all flavors in the ClusterQueue are TAS flavors, and all Workloads submitted
			// to this ClusterQueue are expected to use TAS, and it's a match.
			return nil
		}
		// PodSet explicitly requires TAS, so we need to check if the flavor supports it.
		if flavor.Spec.TopologyName == nil {
			return ptr.To(fmt.Sprintf("Flavor %q does not support TopologyAwareScheduling", flavor.Name))
		}
		s := cq.TASFlavors[kueue.ResourceFlavorReference(flavor.Name)]
		if s == nil {
			// Skip Flavors if they don't have TAS information. This should generally
			// not happen, but possible in race-situation when the ResourceFlavor
			// API object was recently added but is not cached yet.
			return ptr.To(fmt.Sprintf("Flavor %q information missing in TAS cache", flavor.Name))
		}
		if !s.HasLevel(ps.TopologyRequest) {
			// Skip flavors which don't have the requested level
			return ptr.To(fmt.Sprintf("Flavor %q does not contain the requested level", flavor.Name))
		}
		// PodSet requires TAS and the flavor supports it, so it's a match.
		return nil
	}
	// PodSet doesn't require TAS, but the flavor supports it.
	if flavor.Spec.TopologyName != nil {
		return ptr.To(fmt.Sprintf("Flavor %q supports only TopologyAwareScheduling", flavor.Name))
	}
	// PodSet doesn't require TAS and the flavor doesn't support it, so it's a match.
	return nil
}

// isTASImplied returns true if TAS is requested implicitly.
func isTASImplied(ps *kueue.PodSet, cq *schdcache.ClusterQueueSnapshot) bool {
	return !workload.IsExplicitlyRequestingTAS(*ps) && cq.IsTASOnly()
}

// isTASRequested checks if TAS is requested for the input PodSet, either
// explicitly or implicitly.
func isTASRequested(ps *kueue.PodSet, cq *schdcache.ClusterQueueSnapshot) bool {
	return workload.IsExplicitlyRequestingTAS(*ps) || isTASImplied(ps, cq)
}

// getPreviousTopologyAssignment extracts the topology assignment from a replaced workload slice.
func getPreviousTopologyAssignment(replaceWorkloadSlice *workload.Info, podSetName kueue.PodSetReference) *kueue.TopologyAssignment {
	if replaceWorkloadSlice == nil || replaceWorkloadSlice.Obj.Status.Admission == nil {
		return nil
	}
	for _, psa := range replaceWorkloadSlice.Obj.Status.Admission.PodSetAssignments {
		if psa.Name == podSetName && psa.TopologyAssignment != nil {
			return psa.TopologyAssignment
		}
	}
	return nil
}
