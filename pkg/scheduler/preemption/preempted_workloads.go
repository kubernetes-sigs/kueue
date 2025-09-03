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

package preemption

import (
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

type PreemptedWorkloads map[workload.Reference]*workload.Info

func (p PreemptedWorkloads) HasAny(newTargets []*Target) bool {
	for _, target := range newTargets {
		if _, found := p[workload.Key(target.WorkloadInfo.Obj)]; found {
			return true
		}
	}
	return false
}

func (p PreemptedWorkloads) Insert(newTargets []*Target) {
	for _, target := range newTargets {
		p[workload.Key(target.WorkloadInfo.Obj)] = target.WorkloadInfo
	}
}

// PreemptedWorkloadUsageTracker represents a preemption target with usage tracking
type PreemptedWorkloadUsageTracker struct {
	WorkloadInfo *workload.Info
	// OriginalUsage is the original usage of this workload
	OriginalUsage workload.Usage
	// ClaimedUsage tracks how much of this workload's usage has been claimed by preemptors
	ClaimedUsage workload.Usage
}

// PreemptedWorkloadsV2 allows multiple preemptors to target the same workload
type PreemptedWorkloadsV2 map[workload.Reference]*PreemptedWorkloadUsageTracker

// HasAny checks if any of the new targets have already been claimed
func (p PreemptedWorkloadsV2) HasAny(newTargets []*Target) bool {
	for _, target := range newTargets {
		if _, found := p[workload.Key(target.WorkloadInfo.Obj)]; found {
			return true
		}
	}
	return false
}

// CanClaim checks if the specified usage can be claimed from the targets
// Returns true if there's sufficient remaining capacity across all targets combined
func (p PreemptedWorkloadsV2) CanClaim(newTargets []*Target, requiredUsage workload.Usage) bool {
	// Create a copy of required usage to track what still needs to be satisfied
	remainingUsage := requiredUsage.Copy()

	// Try to satisfy the remaining usage from each target
	for _, target := range newTargets {
		key := workload.Key(target.WorkloadInfo.Obj)
		targetUsage := target.WorkloadInfo.Usage()

		// Get current claimed usage for this target
		var claimedUsage workload.Usage
		if existing, found := p[key]; found {
			claimedUsage = existing.ClaimedUsage
		}

		// Calculate what we can claim from this target
		claimable := calculateClaimableUsage(targetUsage, claimedUsage, remainingUsage)

		// Subtract what we can claim from remaining usage
		remainingUsage = remainingUsage.Subtract(claimable)

		// If we've satisfied all requirements, we can proceed
		if remainingUsage.IsEmpty() {
			return true
		}
	}

	// If we still have unmet requirements, we can't claim
	return remainingUsage.IsEmpty()
}

// Insert adds new targets to PreemptedWorkloadsV2 and adds new usage claimed by preemptors to PreemptedWorkloadsV2
// The claimedUsage is distributed greedily across targets, ie fill up each target completely before moving to the next
func (p PreemptedWorkloadsV2) Insert(newTargets []*Target, claimedUsage workload.Usage) {
	if len(newTargets) == 0 {
		return
	}

	// Greedily claim usage from targets - fill up each target before moving to the next
	remainingUsage := claimedUsage.Copy()

	for _, target := range newTargets {
		if remainingUsage.IsEmpty() {
			break
		}

		key := workload.Key(target.WorkloadInfo.Obj)
		targetUsage := target.WorkloadInfo.Usage()

		// Get existing claimed usage for this target
		var existingClaimedUsage workload.Usage
		if existing, found := p[key]; found {
			existingClaimedUsage = existing.ClaimedUsage
		} else {
			existingClaimedUsage = workload.Usage{}
		}

		// Calculate available capacity in this target (what hasn't been claimed yet)
		availableInTarget := targetUsage.Subtract(existingClaimedUsage)

		// Claim as much as possible from this target (min of what we need and what's available)
		claimFromTarget := minUsage(remainingUsage, availableInTarget)

		if !claimFromTarget.IsEmpty() {
			if existing, found := p[key]; found {
				// Add to existing claimed usage
				existing.ClaimedUsage = existing.ClaimedUsage.Add(claimFromTarget)
			} else {
				// Create new entry, workload is found as a preemption target for the first time
				p[key] = &PreemptedWorkloadUsageTracker{
					WorkloadInfo:  target.WorkloadInfo,
					OriginalUsage: targetUsage,
					ClaimedUsage:  claimFromTarget,
				}
			}

			// Subtract what we claimed from remaining usage
			remainingUsage = remainingUsage.Subtract(claimFromTarget)
		}
	}
}

// GetWorkloads returns a list of workload.Info for all preempted workloads
func (p PreemptedWorkloadsV2) GetWorkloads() []*workload.Info {
	workloads := make([]*workload.Info, 0, len(p))
	for _, entry := range p {
		workloads = append(workloads, entry.WorkloadInfo)
	}
	return workloads
}

// GetFullyClaimedWorkloads returns a list of workload.Info for workloads whose usage has been entirely claimed
func (p PreemptedWorkloadsV2) GetFullyClaimedWorkloads() []*workload.Info {
	workloads := make([]*workload.Info, 0, len(p))
	for _, entry := range p {
		if isFullyClaimed(entry.OriginalUsage, entry.ClaimedUsage) {
			workloads = append(workloads, entry.WorkloadInfo)
		}
	}
	return workloads
}

// calculateClaimableUsage calculates how much usage can be claimed from a target
func calculateClaimableUsage(targetUsage, claimedUsage, requiredUsage workload.Usage) workload.Usage {
	result := workload.Usage{}

	if requiredUsage.Quota != nil && targetUsage.Quota != nil {
		result.Quota = make(resources.FlavorResourceQuantities)

		for flavorResource, requiredAmount := range requiredUsage.Quota {
			targetAmount := targetUsage.Quota[flavorResource]
			claimedAmount := int64(0)
			if claimedUsage.Quota != nil {
				claimedAmount = claimedUsage.Quota[flavorResource]
			}

			availableAmount := targetAmount - claimedAmount
			claimableAmount := min(availableAmount, requiredAmount)

			if claimableAmount > 0 {
				result.Quota[flavorResource] = claimableAmount
			}
		}
	}

	return result
}

// isFullyClaimed checks if the claimed usage equals or exceeds the original usage
func isFullyClaimed(originalUsage, claimedUsage workload.Usage) bool {
	// Check if all quota resources are fully claimed
	if originalUsage.Quota != nil {
		for flavorResource, originalAmount := range originalUsage.Quota {
			claimedAmount := int64(0)
			if claimedUsage.Quota != nil {
				claimedAmount = claimedUsage.Quota[flavorResource]
			}

			if claimedAmount < originalAmount {
				return false // Still has remaining capacity
			}
		}
	}

	return true
}

// minUsage returns the minimum usage between two usages (element-wise minimum)
// This works with the workload.Usage structure which uses FlavorResourceQuantities for Quota
func minUsage(usage1, usage2 workload.Usage) workload.Usage {
	result := workload.Usage{}

	// Handle Quota resources (FlavorResourceQuantities)
	if usage1.Quota != nil && usage2.Quota != nil {
		result.Quota = make(resources.FlavorResourceQuantities)

		// Iterate through all flavor-resource combinations in usage1
		for flavorResource, quantity1 := range usage1.Quota {
			if quantity2, exists := usage2.Quota[flavorResource]; exists {
				// Take the minimum of the two quantities
				if quantity1 <= quantity2 {
					result.Quota[flavorResource] = quantity1
				} else {
					result.Quota[flavorResource] = quantity2
				}
			}
			// If flavorResource doesn't exist in usage2, it's effectively 0, so we don't include it
		}
	}

	return result
}
