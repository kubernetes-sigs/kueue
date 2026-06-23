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

	"sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

//lint:file-ignore ST1003 "generated Convert_* calls below use underscores"
//revive:disable:var-naming

func Convert_v1beta1_FairSharingStatus_To_v1beta2_FairSharingStatus(in *FairSharingStatus, out *v1beta2.FairSharingStatus, s conversionapi.Scope) error {
	return autoConvert_v1beta1_FairSharingStatus_To_v1beta2_FairSharingStatus(in, out, s)
}
