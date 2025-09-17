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
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

var (
	ErrDeviceClassNotMapped = errors.New("DeviceClass is not mapped in DRA configuration")
	ErrResourceClaimInUse   = errors.New("ResourceClaim is in use")
	ErrClaimSpecNotFound    = errors.New("failed to get claim spec")
)

// countDevicesPerClass returns a resources.Requests representing the
// total number of devices requested for each DeviceClass inside the provided
// ResourceClaimSpec.
func countDevicesPerClass(claimSpec *resourcev1beta2.ResourceClaimSpec) resources.Requests {
	out := resources.Requests{}
	if claimSpec == nil {
		return out
	}
	for _, req := range claimSpec.Devices.Requests {
		// v1beta2 DeviceRequest has Exactly or FirstAvailable. For Step 1, we
		// preserve existing semantics by only supporting Exactly with Count.
		var dcName string
		var q int64
		if req.Exactly != nil {
			dcName = req.Exactly.DeviceClassName
			if req.Exactly.AllocationMode == resourcev1beta2.DeviceAllocationModeExactCount {
				q = req.Exactly.Count
			}
		}
		dc := corev1.ResourceName(dcName)
		if dc == "" {
			continue
		}
		// TODO: handle other Allocation modes
		out[dc] += q
	}
	return out
}

// getClaimSpec resolves the ResourceClaim(Template) referenced by the PodResourceClaim
// and returns its *ResourceClaimSpec. A nil spec and nil error mean the reference is
// empty (both name pointers are nil) and should be skipped.
func getClaimSpec(ctx context.Context, cl client.Client, namespace string, prc corev1.PodResourceClaim) (*resourcev1beta2.ResourceClaimSpec, error) {
	switch {
	case prc.ResourceClaimTemplateName != nil:
		var tmpl resourcev1beta2.ResourceClaimTemplate
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimTemplateName}, &tmpl); err != nil {
			return nil, err
		}
		return &tmpl.Spec.Spec, nil
	case prc.ResourceClaimName != nil:
		var claim resourcev1beta2.ResourceClaim
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimName}, &claim); err != nil {
			return nil, err
		}
		return &claim.Spec, nil
	default:
		return nil, nil
	}
}

// GetResourceRequestsForResourceClaimTemplates walks all ResourceClaimTemplates referenced by each PodSet of the Workload,
// converts DeviceClass counts into logical resources using the provided lookup function and
// returns the aggregated quantities per PodSet.
//
// If at least one DeviceClass is not present in the DRA configuration the function
// returns an error.
func GetResourceRequestsForResourceClaimTemplates(
	ctx context.Context,
	cl client.Client,
	wl *kueue.Workload) (map[kueue.PodSetReference]corev1.ResourceList, error) {
	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		aggregated := corev1.ResourceList{}

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
				logical, found := Mapper().lookup(dc)
				if !found {
					return nil, fmt.Errorf("DeviceClass %s is not mapped in DRA configuration for workload %s podset %s: %w", dc, wl.Name, ps.Name, ErrDeviceClassNotMapped)
				}
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: resource.MustParse(strconv.FormatInt(qty, 10))})
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
	}

	return perPodSet, nil
}
