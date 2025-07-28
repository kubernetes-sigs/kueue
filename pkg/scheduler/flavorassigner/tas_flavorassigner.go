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

	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/workload"
)

// WorkloadsTopologyRequests - returns the TopologyRequests of the workload
func (a *Assignment) WorkloadsTopologyRequests(wl *workload.Info, cq *cache.ClusterQueueSnapshot) cache.WorkloadTASRequests {
	tasRequests := make(cache.WorkloadTASRequests)
	for i, podSet := range wl.Obj.Spec.PodSets {
		if isTASRequested(&podSet, cq) {
			psAssignment := a.podSetAssignmentByName(podSet.Name)
			if psAssignment.Status.IsError() {
				// There is no resource quota assignment for the PodSet - no need to check TAS.
				continue
			}
			if psAssignment.TopologyAssignment != nil && !psAssignment.HasFailedNode(wl) {
				// skip if already computed and doesn't need recomputing
				// if it already has an assignment but needs recomputing due to a failed node
				// we add it to the list of TASRequests
				continue
			}
			isTASImplied := isTASImplied(&podSet, cq)
			psTASRequest, err := podSetTopologyRequest(psAssignment, wl, cq, isTASImplied, i)
			if err != nil {
				psAssignment.error(err)
			} else if psTASRequest != nil {
				tasRequests[psTASRequest.Flavor] = append(tasRequests[psTASRequest.Flavor], *psTASRequest)
			}
		}
	}
	return tasRequests
}

func (psa *PodSetAssignment) HasFailedNode(wl *workload.Info) bool {
	if !workload.HasNodeToReplace(wl.Obj) {
		return false
	}
	failedNode := wl.Obj.Annotations[kueuealpha.NodeToReplaceAnnotation]
	for _, domain := range psa.TopologyAssignment.Domains {
		if domain.Values[len(domain.Values)-1] == failedNode {
			return true
		}
	}
	return false
}

func podSetTopologyRequest(psAssignment *PodSetAssignment,
	wl *workload.Info,
	cq *cache.ClusterQueueSnapshot,
	isTASImplied bool,
	podSetIndex int) (*cache.TASPodSetRequests, error) {
	if len(cq.TASFlavors) == 0 {
		return nil, errors.New("workload requires Topology, but there is no TAS cache information")
	}
	psResources := wl.TotalRequests[podSetIndex]
	singlePodRequests := psResources.SinglePodRequests()
	podCount := psAssignment.Count
	tasFlvr, err := onlyFlavor(psAssignment.Flavors)
	if err != nil {
		return nil, err
	}
	if !workload.HasQuotaReservation(wl.Obj) && cq.HasProvRequestAdmissionCheck(*tasFlvr) {
		// We delay TAS as this is the first scheduling pass, and there is a
		// ProvisioningRequest admission check used for the flavor.
		psAssignment.DelayedTopologyRequest = ptr.To(kueue.DelayedTopologyRequestStatePending)
		return nil, nil
	}
	if cq.TASFlavors[*tasFlvr] == nil {
		return nil, errors.New("workload requires Topology, but there is no TAS cache information for the assigned flavor")
	}
	podSet := &wl.Obj.Spec.PodSets[podSetIndex]
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

	return &cache.TASPodSetRequests{
		Count:             podCount,
		SinglePodRequests: singlePodRequests,
		PodSet:            podSet,
		PodSetUpdates:     podSetUpdates,
		Flavor:            *tasFlvr,
		Implied:           isTASImplied,
		PodSetGroupName:   podSetGroupName,
	}, nil
}

func onlyFlavor(ra ResourceAssignment) (*kueue.ResourceFlavorReference, error) {
	var result *kueue.ResourceFlavorReference
	for _, v := range ra {
		if result == nil {
			result = &v.Name
		} else if *result != v.Name {
			return nil, fmt.Errorf("more than one flavor assigned: %s, %s", v.Name, *result)
		}
	}
	if result != nil {
		return result, nil
	}
	return nil, errors.New("no flavor assigned")
}

func checkPodSetAndFlavorMatchForTAS(cq *cache.ClusterQueueSnapshot, ps *kueue.PodSet, flavor *kueue.ResourceFlavor) *string {
	// For PodSets which require TAS skip resource flavors which don't support it
	if ps.TopologyRequest != nil {
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
	}
	// If this is a TAS-only CQ, then no TopologyRequest is ok
	if isTASImplied(ps, cq) {
		return nil
	}
	// For PodSets which don't use TAS skip resource flavors which are only for TAS
	if ps.TopologyRequest == nil && flavor.Spec.TopologyName != nil {
		return ptr.To(fmt.Sprintf("Flavor %q supports only TopologyAwareScheduling", flavor.Name))
	}
	return nil
}

// isTASImplied returns true if TAS is requested implicitly - there is no
// explicit
func isTASImplied(ps *kueue.PodSet, cq *cache.ClusterQueueSnapshot) bool {
	return ps.TopologyRequest == nil && cq.IsTASOnly()
}

// isTASRequested checks if TAS is requested for the input PodSet, either
// explicitly or implicitly.
func isTASRequested(ps *kueue.PodSet, cq *cache.ClusterQueueSnapshot) bool {
	return ps.TopologyRequest != nil || cq.IsTASOnly()
}
