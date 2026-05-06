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

package concurrentadmission

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
)

// SetParentVariantLabel sets the label indicating the workload is a parent variant.
func SetParentVariantLabel(workload *kueue.Workload) {
	if workload.Labels == nil {
		workload.Labels = make(map[string]string, 1)
	}
	workload.Labels[controllerconstants.ConcurrentAdmissionParentLabelKey] = "true"
}

// IsParent returns true if the workload is a parent variant.
func IsParent(wl *kueue.Workload) bool {
	return wl != nil && wl.Labels[controllerconstants.ConcurrentAdmissionParentLabelKey] == "true"
}

// IsVariant returns true if the workload is a variant.
func IsVariant(wl *kueue.Workload) bool {
	if wl == nil {
		return false
	}
	return GetParentWorkloadName(wl) != ""
}

// GetVariantFlavor returns the allowed flavor for a variant from annotations.
func GetVariantFlavor(wl *kueue.Workload) kueue.ResourceFlavorReference {
	flavors := parseAllowedFlavorsString(wl.GetAnnotations()[controllerconstants.WorkloadAllowedResourceFlavorAnnotation])
	if len(flavors) == 0 {
		return ""
	}
	return kueue.ResourceFlavorReference(flavors[0])
}

// GetParentWorkloadName returns the name of the parent workload from owner references.
func GetParentWorkloadName(wl *kueue.Workload) string {
	if wl == nil {
		return ""
	}
	for _, owner := range wl.OwnerReferences {
		if owner.Kind == "Workload" && owner.APIVersion == kueue.GroupVersion.String() {
			return owner.Name
		}
	}
	return ""
}

// GetParentWorkloadUID returns the UID of the parent workload from owner references.
func GetParentWorkloadUID(wl *kueue.Workload) types.UID {
	if wl == nil {
		return ""
	}
	for _, ref := range wl.OwnerReferences {
		if ref.Kind == "Workload" && ref.APIVersion == kueue.GroupVersion.String() {
			return ref.UID
		}
	}
	return ""
}

func IsFlavorAllowedForVariant(wl *kueue.Workload, flavor kueue.ResourceFlavorReference) bool {
	if !features.Enabled(features.ConcurrentAdmission) {
		return true
	}
	val, ok := wl.GetAnnotations()[controllerconstants.WorkloadAllowedResourceFlavorAnnotation]
	if !ok {
		return true
	}
	allowedFlavors := parseAllowedFlavorsString(val)
	return slices.Contains(allowedFlavors, string(flavor))
}

// parseAllowedFlavorsString parses a comma-separated string of flavor names
// and returns a slice of flavor names with leading and trailing spaces removed.
func parseAllowedFlavorsString(s string) []string {
	if strings.TrimSpace(s) == "" {
		return []string{}
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func SerializeAllowedFlavors(flavors []string) string {
	return strings.Join(flavors, ",")
}
