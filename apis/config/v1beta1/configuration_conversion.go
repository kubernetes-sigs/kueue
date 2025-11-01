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

	"sigs.k8s.io/kueue/apis/config/v1beta2"
)

//lint:file-ignore ST1003 "generated Convert_* calls below use underscores"
//revive:disable:var-naming

func (src *Configuration) ConvertTo(dstRaw conversion.Hub) error {
	dst := dstRaw.(*v1beta2.Configuration)
	return Convert_v1beta1_Configuration_To_v1beta2_Configuration(src, dst, nil)
}

func (dst *Configuration) ConvertFrom(srcRaw conversion.Hub) error {
	src := srcRaw.(*v1beta2.Configuration)
	return Convert_v1beta2_Configuration_To_v1beta1_Configuration(src, dst, nil)
}

func Convert_v1beta1_Configuration_To_v1beta2_Configuration(in *Configuration, out *v1beta2.Configuration, s conversionapi.Scope) error {
	return autoConvert_v1beta1_Configuration_To_v1beta2_Configuration(in, out, s)
}

// Convert_v1beta1_Integrations_To_v1beta2_Integrations is a conversion function that ignores deprecated PodOptions field.
func Convert_v1beta1_Integrations_To_v1beta2_Integrations(in *Integrations, out *v1beta2.Integrations, s conversionapi.Scope) error {
	return autoConvert_v1beta1_Integrations_To_v1beta2_Integrations(in, out, s)
}
