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
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// elasticWorkloadResult holds the result of elastic workload placement.
type elasticWorkloadResult struct {
	handled bool
	result  map[kueue.PodSetReference]tasPodSetAssignmentResult
}

// handleElasticWorkload processes delta-only placement for elastic workloads.
// It keeps previous pods fixed and places only new pods during scale-up,
// or truncates the assignment during scale-down.
func (s *TASFlavorSnapshot) handleElasticWorkload(
	workers TASPodSetRequests,
	leader *TASPodSetRequests,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
	opts *findTopologyAssignmentsOption,
) elasticWorkloadResult {
	if workers.PreviousAssignment == nil {
		return elasticWorkloadResult{handled: false}
	}

	prevAssignment := utiltas.InternalFrom(workers.PreviousAssignment)

	if isStale, staleDomain := s.IsTopologyAssignmentStale(prevAssignment); isStale {
		s.log.V(3).Info("previous TAS assignment is stale, doing fresh placement",
			"staleDomain", staleDomain)
		return elasticWorkloadResult{handled: false}
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
		return elasticWorkloadResult{handled: true, result: result}
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
) elasticWorkloadResult {
	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)

	deltaCount := workers.Count - previousCount
	deltaRequest := workers
	deltaRequest.Count = deltaCount
	deltaRequest.PreviousAssignment = nil

	// Previous pods consume capacity
	prevAssumedUsage := computeAssumedUsageFromAssignment(prevAssignment, workers.SinglePodRequests)
	for domainID, usage := range prevAssumedUsage {
		if assumedUsage[domainID] == nil {
			assumedUsage[domainID] = resources.Requests{}
		}
		assumedUsage[domainID].Add(usage)
	}

	deltaAssignments, reason := s.findTopologyAssignment(deltaRequest, leader, assumedUsage, opts.simulateEmpty, "")
	if reason != "" {
		result[workers.PodSet.Name] = tasPodSetAssignmentResult{FailureReason: reason}
		return elasticWorkloadResult{handled: true, result: result}
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
	return elasticWorkloadResult{handled: true, result: result}
}

// handleScaleDown truncates the previous assignment to fit fewer pods.
func (s *TASFlavorSnapshot) handleScaleDown(
	workers TASPodSetRequests,
	prevAssignment *utiltas.TopologyAssignment,
	assumedUsage map[utiltas.TopologyDomainID]resources.Requests,
) elasticWorkloadResult {
	result := make(map[kueue.PodSetReference]tasPodSetAssignmentResult)

	truncatedAssignment := utiltas.TruncateAssignment(prevAssignment, workers.Count)
	result[workers.PodSet.Name] = tasPodSetAssignmentResult{TopologyAssignment: truncatedAssignment}
	addAssumedUsage(assumedUsage, truncatedAssignment, &workers)
	return elasticWorkloadResult{handled: true, result: result}
}

// computeAssumedUsageFromAssignment calculates the usage map from an assignment.
func computeAssumedUsageFromAssignment(ta *utiltas.TopologyAssignment, singlePodRequests resources.Requests) map[utiltas.TopologyDomainID]resources.Requests {
	usage := make(map[utiltas.TopologyDomainID]resources.Requests)
	for _, domain := range ta.Domains {
		domainID := utiltas.DomainID(domain.Values)
		domainUsage := singlePodRequests.ScaledUp(int64(domain.Count))
		domainUsage.Add(resources.Requests{corev1.ResourcePods: int64(domain.Count)})
		usage[domainID] = domainUsage
	}
	return usage
}
