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

func (src *Workload) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1beta2.Workload)
	return Convert_v1beta1_Workload_To_v1beta2_Workload(src, dst, nil)
}

func (dst *Workload) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1beta2.Workload)
	return Convert_v1beta2_Workload_To_v1beta1_Workload(src, dst, nil)
}

func Convert_v1beta2_WorkloadStatus_To_v1beta1_WorkloadStatus(in *v1beta2.WorkloadStatus, out *WorkloadStatus, s conversionapi.Scope) error {
	out.AccumulatedPastExexcutionTimeSeconds = in.AccumulatedPastExecutionTimeSeconds
	return autoConvert_v1beta2_WorkloadStatus_To_v1beta1_WorkloadStatus(in, out, s)
}

func Convert_v1beta1_WorkloadStatus_To_v1beta2_WorkloadStatus(in *WorkloadStatus, out *v1beta2.WorkloadStatus, s conversionapi.Scope) error {
	out.AccumulatedPastExecutionTimeSeconds = in.AccumulatedPastExexcutionTimeSeconds
	return autoConvert_v1beta1_WorkloadStatus_To_v1beta2_WorkloadStatus(in, out, s)
}
