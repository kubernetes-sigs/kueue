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

package dra

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	resourcev1beta1 "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// Sentinel errors for specific conditions that callers can check with errors.Is()
var (
	ErrDeviceClassNotMapped = errors.New("DeviceClass is not mapped in DynamicResourceAllocationConfig")
	ErrResourceClaimInUse   = errors.New("ResourceClaim is used by another workload")
	ErrClaimSpecNotFound    = errors.New("failed to get claim spec")
)

// countDevicesPerClass returns a map[DeviceClass]→Quantity representing the
// total number of devices requested for each DeviceClass inside the provided
// ResourceClaimSpec.
//
// When structured-parameters are used (beta in k8s 1.32/1.33) **every entry**
// in `spec.devices.requests` represents one device, so the quantity is the
// sum of all `exactly` entries that reference the same DeviceClass.
func countDevicesPerClass(claimSpec *resourcev1beta1.ResourceClaimSpec) map[corev1.ResourceName]resource.Quantity {
	out := make(map[corev1.ResourceName]resource.Quantity)
	if claimSpec == nil {
		return out
	}
	for _, req := range claimSpec.Devices.Requests {
		dc := corev1.ResourceName(req.DeviceClassName)
		if dc == "" {
			continue
		}
		var q int64
		if req.AllocationMode == resourcev1beta1.DeviceAllocationModeExactCount {
			q = req.Count
		}
		// TODO: handle other Allocation modes
		if existing, found := out[dc]; found {
			existing.Add(resource.MustParse(strconv.FormatInt(q, 10)))
			out[dc] = existing
		} else {
			out[dc] = resource.MustParse(strconv.FormatInt(q, 10))
		}
	}
	return out
}

// getClaimSpec resolves the ResourceClaim(Template) referenced by the PodResourceClaim
// and returns its *ResourceClaimSpec. A nil spec and nil error mean the reference is
// empty (both name pointers are nil) and should be skipped.
func getClaimSpec(ctx context.Context, cl client.Client, namespace string, prc corev1.PodResourceClaim) (*resourcev1beta1.ResourceClaimSpec, error) {
	switch {
	case prc.ResourceClaimTemplateName != nil:
		var tmpl resourcev1beta1.ResourceClaimTemplate
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimTemplateName}, &tmpl); err != nil {
			return nil, err
		}
		return &tmpl.Spec.Spec, nil
	case prc.ResourceClaimName != nil:
		var claim resourcev1beta1.ResourceClaim
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimName}, &claim); err != nil {
			return nil, err
		}
		return &claim.Spec, nil
	default:
		return nil, nil
	}
}

// GetResourceRequestsForResourceClaimTemplates walks all ResourceClaims referenced by each PodSet of the Workload,
// converts DeviceClass counts into logical resources using the provided lookup function and
// returns the aggregated quantities per PodSet.
//
// If at least one DeviceClass is not present in the DynamicResourceAllocationConfig the function
// returns an error.
func GetResourceRequestsForResourceClaimTemplates(
	ctx context.Context,
	cl client.Client,
	wl *kueue.Workload,
	lookup func(dc corev1.ResourceName) (corev1.ResourceName, bool),
) (map[kueue.PodSetReference]corev1.ResourceList, error) {
	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		aggregated := corev1.ResourceList{}

		// Resolve every ResourceClaim reference in the PodSet template.
		for _, prc := range ps.Template.Spec.ResourceClaims {
			if prc.ResourceClaimTemplateName == nil {
				continue
			}
			spec, err := getClaimSpec(ctx, cl, wl.Namespace, prc)
			if err != nil {
				return nil, fmt.Errorf("failed to get claim spec for ResourceClaimTemplate %s in workload %s podset %s: %w", *prc.ResourceClaimTemplateName, wl.Name, ps.Name, fmt.Errorf("%w: %v", ErrClaimSpecNotFound, err))
			}
			if spec == nil {
				continue
			}

			for dc, qty := range countDevicesPerClass(spec) {
				logical, found := lookup(dc)
				if !found {
					return nil, fmt.Errorf("DeviceClass %s is not mapped in DynamicResourceAllocationConfig for workload %s podset %s: %w", dc, wl.Name, ps.Name, ErrDeviceClassNotMapped)
				}
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: qty})
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
	}

	return perPodSet, nil
}

func GetResourceRequestsForResourceClaims(ctx context.Context,
	cl client.Client,
	wl *kueue.Workload,
	cqName string,
	lookup func(dc corev1.ResourceName) (corev1.ResourceName, bool)) (map[kueue.PodSetReference]corev1.ResourceList, error) {
	log := ctrl.LoggerFrom(ctx)

	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		aggregated := corev1.ResourceList{}

		for _, prc := range ps.Template.Spec.ResourceClaims {
			if prc.ResourceClaimName == nil {
				continue
			}
			spec, err := getClaimSpec(ctx, cl, wl.Namespace, prc)
			if err != nil {
				return nil, fmt.Errorf("failed to get claim spec for ResourceClaim %s/%s in workload %s podset %s: %w", wl.Namespace, *prc.ResourceClaimName, wl.Name, ps.Name, fmt.Errorf("%w: %v", ErrClaimSpecNotFound, err))
			}
			if spec == nil {
				continue
			}
			claimKey := ClaimKey(fmt.Sprintf("%s/%s", wl.Namespace, *prc.ResourceClaimName))

			allowed := claims.addAndCheckWorkLoad(claimKey, wl.Name)
			if !allowed {
				log.Info("claim is used by another workload", "claimKey", claimKey, "workload", wl.Name)
				return nil, fmt.Errorf("ResourceClaim %s/%s is used by another workload: %w", wl.Namespace, *prc.ResourceClaimName, ErrResourceClaimInUse)
			}
			log.Info("workload is allowed to use resource claims", "claimKey", claimKey, "workload", wl.Name)
			for dc, qty := range countDevicesPerClass(spec) {
				logical, found := lookup(dc)
				if !found {
					return nil, fmt.Errorf("DeviceClass %s is not mapped in DynamicResourceAllocationConfig for workload %s podset %s in cluster queue %s: %w", dc, wl.Name, ps.Name, cqName, ErrDeviceClassNotMapped)
				}
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: qty})
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
	}
	return perPodSet, nil
}
