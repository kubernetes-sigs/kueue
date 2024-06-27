/*
Copyright 2024 The Kubernetes Authors.
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
package builder

import "sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"

func mergeBundles(bundles []v1alpha1.VolumeBundle) v1alpha1.VolumeBundle {
	var volumeBundle v1alpha1.VolumeBundle
	for _, b := range bundles {
		volumeBundle.Spec.Volumes = append(volumeBundle.Spec.Volumes, b.Spec.Volumes...)
		volumeBundle.Spec.ContainerVolumeMounts = append(volumeBundle.Spec.ContainerVolumeMounts, b.Spec.ContainerVolumeMounts...)
		volumeBundle.Spec.EnvVars = append(volumeBundle.Spec.EnvVars, b.Spec.EnvVars...)
	}

	return volumeBundle
}
