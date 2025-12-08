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

	// Run autoConvert first to populate out.FlavorFungibility.
	// We need out.FlavorFungibility to be non-nil before we can update its fields.
	if err := autoConvert_v1beta1_ClusterQueueSpec_To_v1beta2_ClusterQueueSpec(in, out, s); err != nil {
		return err
	}

	// Convert AdmissionChecks to AdmissionChecksStrategy after autoConvert.
	// This must happen after autoConvert to avoid being overwritten.
	if len(in.AdmissionChecks) > 0 {
		out.AdmissionChecksStrategy = &v1beta2.AdmissionChecksStrategy{
			AdmissionChecks: make([]v1beta2.AdmissionCheckStrategyRule, len(in.AdmissionChecks)),
		}
		for i, checkRef := range in.AdmissionChecks {
			out.AdmissionChecksStrategy.AdmissionChecks[i] = v1beta2.AdmissionCheckStrategyRule{
				Name: v1beta2.AdmissionCheckReference(checkRef),
			}
		}
	}

	// Convert deprecated FlavorFungibility values to their v1beta2 equivalents.
	// This must happen after autoConvert, which creates out.FlavorFungibility.
	// Preempt -> MayStopSearch
	// Borrow -> MayStopSearch
	if in.FlavorFungibility != nil && out.FlavorFungibility != nil {
		if in.FlavorFungibility.WhenCanPreempt == Preempt {
			out.FlavorFungibility.WhenCanPreempt = v1beta2.MayStopSearch
		}
		if in.FlavorFungibility.WhenCanBorrow == Borrow {
			out.FlavorFungibility.WhenCanBorrow = v1beta2.MayStopSearch
		}
	}

	return nil
}

func Convert_v1beta2_ClusterQueueSpec_To_v1beta1_ClusterQueueSpec(in *v1beta2.ClusterQueueSpec, out *ClusterQueueSpec, s conversionapi.Scope) error {
	out.Cohort = CohortReference(in.CohortName)
	return autoConvert_v1beta2_ClusterQueueSpec_To_v1beta1_ClusterQueueSpec(in, out, s)
}

func Convert_v1beta1_ClusterQueueStatus_To_v1beta2_ClusterQueueStatus(in *ClusterQueueStatus, out *v1beta2.ClusterQueueStatus, s conversionapi.Scope) error {
	return autoConvert_v1beta1_ClusterQueueStatus_To_v1beta2_ClusterQueueStatus(in, out, s)
}
