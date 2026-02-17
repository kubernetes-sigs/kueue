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

package scheduler

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// elasticPlacementResult holds the result of elastic workload placement.
type elasticPlacementResult struct {
	// applied indicates delta placement was used; false means use standard placement.
	applied     bool
	assignments map[kueue.PodSetReference]tasPodSetAssignmentResult
}

// handleElasticWorkload processes delta-only placement for elastic workloads.
// It keeps previous pods fixed and places only new pods during scale-up,
// or truncates the assignment during scale-down.
func (s *TASFlavorSnapshot) handleElasticWorkload(
	workers TASPodSetRequests,
	leader *TASPodSetRequests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	opts *findTopologyAssignmentsOption,
) elasticPlacementResult {
	if workers.PreviousAssignment == nil {
		return elasticPlacementResult{applied: false}
	}

	prevAssignment := utiltas.InternalFrom(workers.PreviousAssignment)

	if isStale, staleDomain := s.IsTopologyAssignmentStale(prevAssignment); isStale {
		s.log.V(3).Info("previous TAS assignment is stale, doing fresh placement",
			"staleDomain", staleDomain)
		return elasticPlacementResult{applied: false}
	}

	previousCount := utiltas.CountPodsInAssignment(prevAssignment)
	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)

	switch {
	case workers.Count > previousCount:
		return s.handleScaleUp(workers, leader, prevAssignment, previousCount, assumedUsage, opts)
	case workers.Count < previousCount:
		return s.handleScaleDown(workers, prevAssignment, assumedUsage)
	default:
		// Same count: reuse previous assignment
		result[workers.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: prevAssignment}
		addAssumedUsage(assumedUsage, prevAssignment, &workers)
		return elasticPlacementResult{applied: true, assignments: result}
	}
}

// handleScaleUp places only delta pods while keeping previous pods fixed.
func (s *TASFlavorSnapshot) handleScaleUp(
	workers TASPodSetRequests,
	leader *TASPodSetRequests,
	prevAssignment *utiltas.TopologyAssignment,
	previousCount int32,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	opts *findTopologyAssignmentsOption,
) elasticPlacementResult {
	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)

	deltaCount := workers.Count - previousCount
	deltaRequest := workers
	deltaRequest.Count = deltaCount
	deltaRequest.PreviousAssignment = nil

	// Previous pods consume capacity
	prevAssumedUsage := utiltas.ComputeUsagePerDomain(prevAssignment, workers.SinglePodRequests)
	for domainID, usage := range prevAssumedUsage {
		if assumedUsage[domainID] == nil {
			assumedUsage[domainID] = resources.Requests{}
		}
		assumedUsage[domainID].Add(usage)
	}

	deltaAssignments, reason := s.findTopologyAssignment(deltaRequest, leader, assumedUsage, opts.simulateEmpty, "")
	if reason != "" {
		result[workers.PodSet.Name] = tasPodSetAssignmentResult{FailureReason: reason}
		return elasticPlacementResult{applied: true, assignments: result}
	}

	deltaAssignment := deltaAssignments[workers.PodSet.Name]
	finalAssignment := s.mergeTopologyAssignments(deltaAssignment, prevAssignment)
	result[workers.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: finalAssignment}

	if leader != nil {
		result[leader.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: deltaAssignments[leader.PodSet.Name]}
		addAssumedUsage(assumedUsage, deltaAssignments[leader.PodSet.Name], leader)
	}

	// Add only delta to avoid double-counting previous pods.
	addAssumedUsage(assumedUsage, deltaAssignment, &workers)
	return elasticPlacementResult{applied: true, assignments: result}
}

// handleScaleDown truncates the previous assignment to fit fewer pods.
func (s *TASFlavorSnapshot) handleScaleDown(
	workers TASPodSetRequests,
	prevAssignment *utiltas.TopologyAssignment,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
) elasticPlacementResult {
	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)

	truncatedAssignment := utiltas.TruncateAssignment(prevAssignment, workers.Count)
	result[workers.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: truncatedAssignment}
	addAssumedUsage(assumedUsage, truncatedAssignment, &workers)
	return elasticPlacementResult{applied: true, assignments: result}
}
