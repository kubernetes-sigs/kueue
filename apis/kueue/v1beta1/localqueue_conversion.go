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

func (src *LocalQueue) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1beta2.LocalQueue)
	return Convert_v1beta1_LocalQueue_To_v1beta2_LocalQueue(src, dst, nil)
}

func (dst *LocalQueue) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1beta2.LocalQueue)
	return Convert_v1beta2_LocalQueue_To_v1beta1_LocalQueue(src, dst, nil)
}

func Convert_v1beta2_LocalQueueStatus_To_v1beta1_LocalQueueStatus(in *v1beta2.LocalQueueStatus, out *LocalQueueStatus, s conversionapi.Scope) error {
	out.FlavorUsage = make([]LocalQueueFlavorUsage, len(in.FlavorsUsage))
	for i := range in.FlavorsUsage {
		if err := autoConvert_v1beta2_LocalQueueFlavorUsage_To_v1beta1_LocalQueueFlavorUsage(&in.FlavorsUsage[i], &out.FlavorUsage[i], s); err != nil {
			return err
		}
	}
	return autoConvert_v1beta2_LocalQueueStatus_To_v1beta1_LocalQueueStatus(in, out, s)
}

func Convert_v1beta1_LocalQueueStatus_To_v1beta2_LocalQueueStatus(in *LocalQueueStatus, out *v1beta2.LocalQueueStatus, s conversionapi.Scope) error {
	out.FlavorsUsage = make([]v1beta2.LocalQueueFlavorUsage, len(in.FlavorUsage))
	for i := range in.FlavorUsage {
		if err := autoConvert_v1beta1_LocalQueueFlavorUsage_To_v1beta2_LocalQueueFlavorUsage(&in.FlavorUsage[i], &out.FlavorsUsage[i], s); err != nil {
			return err
		}
	}
	return autoConvert_v1beta1_LocalQueueStatus_To_v1beta2_LocalQueueStatus(in, out, s)
}
