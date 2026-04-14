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

package handlers

import (
	"k8s.io/apimachinery/pkg/api/resource"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// quantityToFloat64 converts a resource.Quantity to a float64 in base units
// (cores for CPU, bytes for memory, etc.) using the canonical Kubernetes parser.
func quantityToFloat64(q resource.Quantity) float64 {
	return q.AsApproximateFloat64()
}

// convertResourceGroups converts Kueue ResourceGroups to frontend-friendly maps
// with numeric values for all resource quantities.
func convertResourceGroups(rgs []kueueapi.ResourceGroup) []map[string]any {
	result := make([]map[string]any, 0, len(rgs))
	for _, rg := range rgs {
		flavors := make([]map[string]any, 0, len(rg.Flavors))
		for _, f := range rg.Flavors {
			resources := make([]map[string]any, 0, len(f.Resources))
			for _, r := range f.Resources {
				res := map[string]any{
					"name":         string(r.Name),
					"nominalQuota": quantityToFloat64(r.NominalQuota),
				}
				if r.BorrowingLimit != nil {
					res["borrowingLimit"] = quantityToFloat64(*r.BorrowingLimit)
				}
				if r.LendingLimit != nil {
					res["lendingLimit"] = quantityToFloat64(*r.LendingLimit)
				}
				resources = append(resources, res)
			}
			flavors = append(flavors, map[string]any{
				"name":      string(f.Name),
				"resources": resources,
			})
		}
		coveredResources := make([]string, 0, len(rg.CoveredResources))
		for _, cr := range rg.CoveredResources {
			coveredResources = append(coveredResources, string(cr))
		}
		result = append(result, map[string]any{
			"coveredResources": coveredResources,
			"flavors":          flavors,
		})
	}
	return result
}

// convertLocalQueueFlavorsUsage converts LocalQueueFlavorUsage slices to
// frontend-friendly maps with numeric values.
func convertLocalQueueFlavorsUsage(fus []kueueapi.LocalQueueFlavorUsage) []map[string]any {
	result := make([]map[string]any, 0, len(fus))
	for _, fu := range fus {
		resources := make([]map[string]any, 0, len(fu.Resources))
		for _, r := range fu.Resources {
			resources = append(resources, map[string]any{
				"name":  string(r.Name),
				"total": quantityToFloat64(r.Total),
			})
		}
		result = append(result, map[string]any{
			"name":      string(fu.Name),
			"resources": resources,
		})
	}
	return result
}

// convertFlavorsUsage converts Kueue FlavorUsage slices to frontend-friendly maps
// with numeric values for total and borrowed quantities.
func convertFlavorsUsage(fus []kueueapi.FlavorUsage) []map[string]any {
	result := make([]map[string]any, 0, len(fus))
	for _, fu := range fus {
		resources := make([]map[string]any, 0, len(fu.Resources))
		for _, r := range fu.Resources {
			resources = append(resources, map[string]any{
				"name":     string(r.Name),
				"total":    quantityToFloat64(r.Total),
				"borrowed": quantityToFloat64(r.Borrowed),
			})
		}
		result = append(result, map[string]any{
			"name":      string(fu.Name),
			"resources": resources,
		})
	}
	return result
}
