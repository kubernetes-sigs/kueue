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

package v1beta1

import (
	conversionapi "k8s.io/apimachinery/pkg/conversion"
	"sigs.k8s.io/controller-runtime/pkg/conversion"

	"sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

//lint:file-ignore ST1003 "generated Convert_* calls below use underscores"
//revive:disable:var-naming

func (src *ClusterQueue) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1beta2.ClusterQueue)
	return Convert_v1beta1_ClusterQueue_To_v1beta2_ClusterQueue(src, dst, nil)
}

func (dst *ClusterQueue) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1beta2.ClusterQueue)
	return Convert_v1beta2_ClusterQueue_To_v1beta1_ClusterQueue(src, dst, nil)
}

func Convert_v1beta1_ClusterQueueSpec_To_v1beta2_ClusterQueueSpec(in *ClusterQueueSpec, out *v1beta2.ClusterQueueSpec, s conversionapi.Scope) error {
	out.CohortName = v1beta2.CohortReference(in.Cohort)
	if err := autoConvert_v1beta1_ClusterQueueSpec_To_v1beta2_ClusterQueueSpec(in, out, s); err != nil {
		return err
	}

	// Convert AdmissionChecks to AdmissionChecksStrategy
	// If v1beta1 has AdmissionChecks but v1beta2 doesn't have AdmissionChecksStrategy,
	// convert the old field to the new structure
	if len(in.AdmissionChecks) > 0 && out.AdmissionChecksStrategy == nil {
		out.AdmissionChecksStrategy = &v1beta2.AdmissionChecksStrategy{
			AdmissionChecks: make([]v1beta2.AdmissionCheckStrategyRule, len(in.AdmissionChecks)),
		}
		for i, checkRef := range in.AdmissionChecks {
			out.AdmissionChecksStrategy.AdmissionChecks[i] = v1beta2.AdmissionCheckStrategyRule{
				Name:      v1beta2.AdmissionCheckReference(checkRef),
				OnFlavors: []v1beta2.ResourceFlavorReference{}, // Empty means all flavors
			}
		}
	}

	return nil
}

func Convert_v1beta2_ClusterQueueSpec_To_v1beta1_ClusterQueueSpec(in *v1beta2.ClusterQueueSpec, out *ClusterQueueSpec, s conversionapi.Scope) error {
	out.Cohort = CohortReference(in.CohortName)
	if err := autoConvert_v1beta2_ClusterQueueSpec_To_v1beta1_ClusterQueueSpec(in, out, s); err != nil {
		return err
	}

	// Convert AdmissionChecksStrategy to AdmissionChecks if all OnFlavors are empty
	if in.AdmissionChecksStrategy != nil && len(in.AdmissionChecksStrategy.AdmissionChecks) > 0 {
		allEmpty := true
		for _, rule := range in.AdmissionChecksStrategy.AdmissionChecks {
			if len(rule.OnFlavors) > 0 {
				allEmpty = false
				break
			}
		}

		// If all OnFlavors are empty, convert to simple AdmissionChecks list
		if allEmpty {
			out.AdmissionChecks = make([]AdmissionCheckReference, len(in.AdmissionChecksStrategy.AdmissionChecks))
			for i, rule := range in.AdmissionChecksStrategy.AdmissionChecks {
				out.AdmissionChecks[i] = AdmissionCheckReference(rule.Name)
			}
			// Clear AdmissionChecksStrategy since we converted to AdmissionChecks
			out.AdmissionChecksStrategy = nil
		}
	}

	return nil
}

func Convert_v1beta1_ClusterQueueStatus_To_v1beta2_ClusterQueueStatus(in *ClusterQueueStatus, out *v1beta2.ClusterQueueStatus, s conversionapi.Scope) error {
	return autoConvert_v1beta1_ClusterQueueStatus_To_v1beta2_ClusterQueueStatus(in, out, s)
}
