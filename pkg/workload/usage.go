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

package workload

import (
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
)

type TASFlavorUsage []TopologyDomainRequests
type TASUsage map[kueue.ResourceFlavorReference]TASFlavorUsage

type Usage struct {
	Quota resources.FlavorResourceQuantities
	TAS   TASUsage
}

// Subtract subtracts the given usage from this usage and returns the result (Quota only, no TAS)
func (u *Usage) Subtract(val Usage) Usage {
	result := Usage{}

	if u.Quota != nil {
		result.Quota = make(resources.FlavorResourceQuantities)
		for flavorResource, aAmount := range u.Quota {
			remaining := aAmount
			if val.Quota != nil {
				if bAmount, exists := val.Quota[flavorResource]; exists {
					remaining -= bAmount
				}
			}
			if remaining > 0 {
				result.Quota[flavorResource] = remaining
			}
		}
	}

	return result
}

// IsEmpty checks if usage has no positive values (Quota only, no TAS)
func (u *Usage) IsEmpty() bool {
	// Check if Quota is empty or has no positive values
	if u.Quota != nil {
		for _, quantity := range u.Quota {
			if quantity > 0 {
				return false
			}
		}
	}

	return true
}

// Add adds the given usage to this usage and returns the result (Quota only, no TAS)
func (u *Usage) Add(val Usage) Usage {
	result := Usage{}

	// Add Quota resources
	if u.Quota != nil || val.Quota != nil {
		result.Quota = make(resources.FlavorResourceQuantities)

		// Copy from u
		if u.Quota != nil {
			for flavorResource, quantity := range u.Quota {
				result.Quota[flavorResource] = quantity
			}
		}

		// Add from val
		if val.Quota != nil {
			for flavorResource, quantity := range val.Quota {
				result.Quota[flavorResource] += quantity
			}
		}
	}

	return result
}

// Copy creates a copy of the Usage struct (Quota only, no TAS)
func (u *Usage) Copy() Usage {
	result := Usage{}

	if u.Quota != nil {
		result.Quota = make(resources.FlavorResourceQuantities)
		for flavorResource, amount := range u.Quota {
			result.Quota[flavorResource] = amount
		}
	}

	return result
}
